#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

mode=''
mode_count=0
route_file=''
operator_external_id=''

while test "$#" -gt 0; do
  case "$1" in
    --enable) mode=true; mode_count=$((mode_count + 1)); shift ;;
    --disable) mode=false; mode_count=$((mode_count + 1)); shift ;;
    --route-file) route_file="${2:-}"; shift 2 ;;
    --operator-external-id) operator_external_id="${2:-}"; shift 2 ;;
    *) die "usage: canvas-writes.sh <--enable|--disable> --route-file PATH --operator-external-id POSITIVE_INTEGER" ;;
  esac
done

test "$mode_count" -eq 1 || die "exactly one of --enable or --disable is required"
test -n "$route_file" || die "--route-file is required"
[[ "$route_file" = /* ]] || die "route file must be an absolute path"
test -f "$route_file" || die "route file is missing: $route_file"
test ! -L "$route_file" || die "route file must not be a symlink"
[[ "$operator_external_id" =~ ^[1-9][0-9]*$ ]] || die "--operator-external-id must be a positive integer"

load_slot_environment stable
require_command docker
require_command flock
require_command mktemp

current="$(current_release_file stable)"
test -f "$current" || die "Canvas stable has no current release"

acquire_global_deploy_lock
release_dir="$(state_release_dir stable)"
exec 9>"$release_dir/deploy.lock"
flock -n 9 || die "another Canvas release is running for stable"

current_mode="$(sed -n 's/^CANVAS_WRITES_ENABLED=//p' "$SLOT_ENV_FILE")"
case "$current_mode" in
  true|false) ;;
  *) die "stable environment has an invalid CANVAS_WRITES_ENABLED value" ;;
esac
if test "$current_mode" = "$mode"; then
  printf 'Canvas stable writes are already %s\n' "$mode"
  exit 0
fi

environment_backup="$(mktemp "$release_dir/stable.env.before.XXXXXX")"
cp -- "$SLOT_ENV_FILE" "$environment_backup"
chmod 600 "$environment_backup"
temporary_environment="$(mktemp "$(dirname -- "$SLOT_ENV_FILE")/.stable.env.XXXXXX")"
cleanup() { rm -f -- "$temporary_environment" "$environment_backup"; }
trap cleanup EXIT

awk -v mode="$mode" '
  BEGIN { found = 0 }
  /^CANVAS_WRITES_ENABLED=/ { print "CANVAS_WRITES_ENABLED=" mode; found = 1; next }
  { print }
  END { if (!found) print "CANVAS_WRITES_ENABLED=" mode }
' "$SLOT_ENV_FILE" >"$temporary_environment"
chmod 600 "$temporary_environment"

rollback_needed=true
restore_mode() {
  local status=$?
  trap - ERR
  set +e
  cp -- "$environment_backup" "${SLOT_ENV_FILE}.restore"
  chmod 600 "${SLOT_ENV_FILE}.restore"
  mv -f -- "${SLOT_ENV_FILE}.restore" "$SLOT_ENV_FILE"
  load_slot_environment stable
  canvas_compose "$current" up -d --force-recreate api worker
  printf 'write-mode change failed; restored CANVAS_WRITES_ENABLED=%s\n' "$current_mode" >&2
  exit "$status"
}
trap restore_mode ERR

mv -f -- "$temporary_environment" "$SLOT_ENV_FILE"
load_slot_environment stable
canvas_compose "$current" up -d --force-recreate api worker
wait_container_healthy canvas_stable_api
wait_container_healthy canvas_stable_worker
wait_http_ready "http://127.0.0.1:${CANVAS_API_PORT}/canvas-api/health/ready"

for component in api worker; do
  docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "canvas_stable_${component}" |
    grep -Fx "CANVAS_WRITES_ENABLED=$mode" >/dev/null || die "$component write mode does not match $mode"
done
"$DEPLOY_DIR/canvas-verify.sh" --slot stable --route-file "$route_file"

printf '{"event":"write_mode","slot":"stable","writes_enabled":%s,"operator_external_user_id":%s,"release":"%s","build_id":"%s","created_at":"%s"}\n' \
  "$mode" "$operator_external_id" "$CANVAS_RELEASE" "$CANVAS_BUILD_ID" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >>"$release_dir/audit.jsonl"
rollback_needed=false
trap - ERR
printf 'Canvas stable writes_enabled=%s is healthy\n' "$mode"
