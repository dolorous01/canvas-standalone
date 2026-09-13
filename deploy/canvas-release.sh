#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

slot=''
web_image=''
api_image=''
release=''
build_id=''
route_file=''
reconcile_bearer_file=''
reconcile_report=''
dry_run=false
allow_local=false

while test "$#" -gt 0; do
  case "$1" in
    --slot) slot="${2:-}"; shift 2 ;;
    --web-image) web_image="${2:-}"; shift 2 ;;
    --api-image) api_image="${2:-}"; shift 2 ;;
    --release) release="${2:-}"; shift 2 ;;
    --build-id) build_id="${2:-}"; shift 2 ;;
    --route-file) route_file="${2:-}"; shift 2 ;;
    --reconcile-bearer-file) reconcile_bearer_file="${2:-}"; shift 2 ;;
    --reconcile-report) reconcile_report="${2:-}"; shift 2 ;;
    --dry-run) dry_run=true; shift ;;
    --allow-local-image) allow_local=true; shift ;;
    *) die "usage: canvas-release.sh --slot <stable|candidate> --web-image IMAGE --api-image IMAGE --route-file PATH [--release VALUE] [--build-id VALUE] [--reconcile-bearer-file PATH] [--reconcile-report PATH] [--dry-run] [--allow-local-image]" ;;
  esac
done

validate_slot "$slot"
load_slot_environment "$slot"
require_command docker
require_command curl
require_command flock
require_command python3
require_image_reference "$web_image" "$allow_local"
require_image_reference "$api_image" "$allow_local"
validate_reconcile_options "$reconcile_bearer_file" "$reconcile_report"
test -n "$route_file" || die "--route-file is required so static assets follow the released build"
[[ "$route_file" = /* ]] || die "route file must be an absolute path"
test "$route_file" != / || die "route file must not be the filesystem root"
test -d "$(dirname -- "$route_file")" || die "route file parent directory does not exist"
if test -e "$route_file"; then
  test -f "$route_file" || die "route file is not a regular file"
  test ! -L "$route_file" || die "route file must not be a symlink"
fi

if is_digest_image "$web_image"; then docker pull "$web_image" >/dev/null; fi
if is_digest_image "$api_image"; then docker pull "$api_image" >/dev/null; fi
docker image inspect "$web_image" >/dev/null
docker image inspect "$api_image" >/dev/null

image_release="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$web_image")"
image_build_id="$(docker image inspect --format '{{index .Config.Labels "io.canvas.build-id"}}' "$web_image")"
release="${release:-$image_release}"
build_id="${build_id:-$image_build_id}"
test -n "$release" || die "release is empty and web image has no version label"
test -n "$build_id" || die "build ID is empty and web image has no io.canvas.build-id label"
[[ "$release" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || die "invalid release"
[[ "$build_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || die "invalid build ID"

release_dir="$(state_release_dir "$slot")"
mkdir -p -- "$release_dir"
chmod 700 "$release_dir"
transaction_dir="$(mktemp -d "$release_dir/transaction.XXXXXX")"
proposed="$transaction_dir/proposed.env"
route_candidate="$transaction_dir/routes.json"
current_backup="$transaction_dir/current.before.env"
previous_backup="$transaction_dir/previous.before.env"
route_backup="$transaction_dir/routes.before.json"
cleanup() { rm -rf -- "$transaction_dir"; }
trap cleanup EXIT
{
  printf 'CANVAS_API_IMAGE=%s\n' "$api_image"
  printf 'CANVAS_WEB_IMAGE=%s\n' "$web_image"
  printf 'CANVAS_RELEASE=%s\n' "$release"
  printf 'CANVAS_BUILD_ID=%s\n' "$build_id"
} >"$proposed"
chmod 600 "$proposed"

if test -f "$route_file"; then
  cp -- "$route_file" "$route_candidate"
fi
python3 "$DEPLOY_DIR/proxy/update_routes.py" \
  --file "$route_candidate" \
  --slot "$slot" \
  --web "http://127.0.0.1:${CANVAS_WEB_PORT}" \
  --api "http://127.0.0.1:${CANVAS_API_PORT}" \
  --build-id "$build_id" >/dev/null

canvas_compose "$proposed" config --quiet
if test "$dry_run" = true; then
  printf 'dry-run passed\nslot=%s\nrelease=%s\nbuild_id=%s\napi_image=%s\nweb_image=%s\nroute_file=%s\n' "$slot" "$release" "$build_id" "$api_image" "$web_image" "$route_file"
  printf 'next: database start -> migration -> forced service recreation -> three readiness rounds -> image identity check -> atomic route update\n'
  if test -n "$reconcile_bearer_file"; then
    printf 'post-update reconciliation is configured and will run only during the real release\n'
  fi
  exit 0
fi

acquire_global_deploy_lock
exec 9>"$release_dir/deploy.lock"
flock -n 9 || die "another Canvas release is running for $slot"
current="$(current_release_file "$slot")"
previous="$(previous_release_file "$slot")"
had_current=false
had_previous=false
had_route=false
if test -f "$current"; then
  cp -- "$current" "$current_backup"
  had_current=true
fi
if test -f "$previous"; then
  cp -- "$previous" "$previous_backup"
  had_previous=true
fi
if test -f "$route_file"; then
  cp -- "$route_file" "$route_backup"
  had_route=true
fi
rollback_needed=false
route_updated=false
state_updated=false

restore_file() {
  local existed="$1"
  local backup="$2"
  local destination="$3"
  local temporary
  if test "$existed" = true; then
    temporary="$(mktemp "$(dirname -- "$destination")/.canvas-restore.XXXXXX")"
    cp -- "$backup" "$temporary"
    chmod --reference="$backup" "$temporary"
    mv -f -- "$temporary" "$destination"
  else
    rm -f -- "$destination"
  fi
}

restore_previous() {
  local status=$?
  trap - ERR
  set +e
  if test "$route_updated" = true; then
    restore_file "$had_route" "$route_backup" "$route_file"
  fi
  if test "$rollback_needed" = true && test "$had_current" = true; then
    printf 'release failed; recreating the previous %s services\n' "$slot" >&2
    canvas_compose "$current_backup" up -d --force-recreate api worker web
  fi
  if test "$state_updated" = true; then
    restore_file "$had_current" "$current_backup" "$current"
    restore_file "$had_previous" "$previous_backup" "$previous"
  fi
  exit "$status"
}
trap restore_previous ERR

if test "$slot" = stable && docker inspect "canvas_${slot}_postgres" >/dev/null 2>&1; then
  "$DEPLOY_DIR/canvas-backup.sh" --slot "$slot" >/dev/null
fi

canvas_compose "$proposed" up -d database
wait_container_healthy "canvas_${slot}_postgres"
canvas_compose "$proposed" run --rm migrate
rollback_needed=true
canvas_compose "$proposed" up -d --force-recreate api worker web
wait_container_healthy "canvas_${slot}_api"
wait_container_healthy "canvas_${slot}_worker"
wait_container_healthy "canvas_${slot}_web"
wait_http_ready "http://127.0.0.1:${CANVAS_API_PORT}/canvas-api/health/ready"
wait_http_ready "http://127.0.0.1:${CANVAS_WEB_PORT}/health/ready"

test "$(container_image_id "canvas_${slot}_api")" = "$(image_id "$api_image")" || die "API container image ID mismatch"
test "$(container_image_id "canvas_${slot}_worker")" = "$(image_id "$api_image")" || die "worker container image ID mismatch"
test "$(container_image_id "canvas_${slot}_web")" = "$(image_id "$web_image")" || die "web container image ID mismatch"

python3 "$DEPLOY_DIR/proxy/update_routes.py" \
  --file "$route_file" \
  --slot "$slot" \
  --web "http://127.0.0.1:${CANVAS_WEB_PORT}" \
  --api "http://127.0.0.1:${CANVAS_API_PORT}" \
  --build-id "$build_id" >/dev/null
route_updated=true

state_updated=true
if test "$had_current" = true; then
  cp -- "$current" "${previous}.new"
  mv -f -- "${previous}.new" "$previous"
fi
cp -- "$proposed" "${current}.new"
mv -f -- "${current}.new" "$current"

"$DEPLOY_DIR/canvas-verify.sh" --slot "$slot" --route-file "$route_file"

audit="$release_dir/audit.jsonl"
printf '{"event":"release","slot":"%s","release":"%s","build_id":"%s","api_image_id":"%s","web_image_id":"%s","created_at":"%s"}\n' \
  "$slot" "$release" "$build_id" "$(image_id "$api_image")" "$(image_id "$web_image")" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >>"$audit"
rollback_needed=false
trap - ERR
printf 'Canvas %s release %s is healthy\n' "$slot" "$release"
if test -n "$reconcile_bearer_file"; then
  if ! run_post_update_reconcile "$slot" "$route_file" "$reconcile_bearer_file" "$reconcile_report"; then
    die "Canvas release is healthy and remains active, but post-update reconciliation failed"
  fi
fi
