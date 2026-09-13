#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

official_ops_dir() {
  printf '%s\n' '/home/ubuntu/ResearchWang13/blue-green'
}

official_guard_state_dir() {
  printf '%s/state/official-guard\n' "$REPOSITORY_ROOT"
}

official_run() {
  if test "$EUID" -eq 0; then
    "$@"
  else
    require_command sudo
    sudo -- "$@"
  fi
}

verify_canvas_stable() (
  local route_file="$1"

  load_slot_environment stable
  test "$CANVAS_WEB_PORT" = 18100 || die "Canvas stable web port must be 18100"
  test "$CANVAS_API_PORT" = 18101 || die "Canvas stable API port must be 18101"
  "$DEPLOY_DIR/canvas-verify.sh" --slot stable --route-file "$route_file"
  test "$(curl --silent --show-error --max-time 5 --output /dev/null --write-out '%{http_code}' \
    -X POST http://127.0.0.1:8080/api/v1/admin/system/update)" = 403 || \
    die "public lifecycle update endpoint is not blocked"
)

capture_canvas_stable_identity() (
  local route_file="$1"
  local output_file="$2"
  local current
  local temporary

  current="$(current_release_file stable)"
  test -f "$current" || die "Canvas stable has no current release"
  temporary="$(mktemp "$(dirname -- "$output_file")/.canvas-identity.XXXXXX")"
  {
    printf 'route_sha256 %s\n' "$(sha256sum -- "$route_file" | awk '{print $1}')"
    printf 'release_sha256 %s\n' "$(sha256sum -- "$current" | awk '{print $1}')"
    for component in postgres api worker web; do
      docker inspect --format \
        'container {{.Name}} {{.Id}} {{.Image}} {{.State.StartedAt}} {{.RestartCount}}' \
        "canvas_stable_${component}"
    done
  } >"$temporary"
  chmod 0600 "$temporary"
  mv -f -- "$temporary" "$output_file"
)

write_guard_result() {
  local output_file="$1"
  local operation="$2"
  local operation_status="$3"
  local official_status="$4"
  local canvas_status="$5"
  local canvas_unchanged="$6"

  {
    printf 'operation=%s\n' "$operation"
    printf 'operation_status=%s\n' "$operation_status"
    printf 'official_status=%s\n' "$official_status"
    printf 'canvas_status=%s\n' "$canvas_status"
    printf 'canvas_unchanged=%s\n' "$canvas_unchanged"
    printf 'finished_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } >"$output_file"
  chmod 0600 "$output_file"
}

run_guarded_official_command() {
  local operation="$1"
  local route_file="$2"
  local command_name="$3"
  shift 3

  local ops_dir
  local evidence_root
  local evidence_dir
  local before_file
  local after_file
  local operation_status=0
  local official_status=0
  local canvas_status=0
  local canvas_unchanged=false

  case "$command_name" in
    deploy.sh|rollback.sh) ;;
    *) die "unsupported official operation command: $command_name" ;;
  esac
  [[ "$route_file" = /* ]] || die "route file must be an absolute path"
  test -f "$route_file" || die "route file is missing: $route_file"
  test ! -L "$route_file" || die "route file must not be a symlink"

  require_command awk
  require_command cmp
  require_command curl
  require_command date
  require_command diff
  require_command docker
  require_command flock
  require_command mktemp
  require_command sha256sum

  acquire_global_deploy_lock
  ops_dir="$(official_ops_dir)"
  evidence_root="$(official_guard_state_dir)"
  mkdir -p -- "$evidence_root"
  chmod 0700 "$evidence_root"
  evidence_dir="$(mktemp -d "$evidence_root/${operation}-$(date -u +%Y%m%dT%H%M%SZ).XXXXXX")"
  chmod 0700 "$evidence_dir"
  before_file="$evidence_dir/canvas.before"
  after_file="$evidence_dir/canvas.after"

  if ! verify_canvas_stable "$route_file"; then
    write_guard_result "$evidence_dir/result.env" "$operation" 125 125 125 false
    die "Canvas stable preflight failed; official Sub2API was not changed (evidence: $evidence_dir)"
  fi
  capture_canvas_stable_identity "$route_file" "$before_file"
  if ! official_run "$ops_dir/status.sh"; then
    write_guard_result "$evidence_dir/result.env" "$operation" 125 125 0 true
    die "official Sub2API preflight failed; no update was started (evidence: $evidence_dir)"
  fi

  if official_run "$ops_dir/$command_name" "$@"; then
    operation_status=0
  else
    operation_status=$?
  fi

  if official_run "$ops_dir/status.sh"; then
    official_status=0
  else
    official_status=$?
  fi
  if verify_canvas_stable "$route_file" && capture_canvas_stable_identity "$route_file" "$after_file"; then
    canvas_status=0
    if cmp -s -- "$before_file" "$after_file"; then
      canvas_unchanged=true
    else
      printf 'Canvas identity changed during the official operation:\n' >&2
      diff -u -- "$before_file" "$after_file" >&2 || true
    fi
  else
    canvas_status=1
  fi

  write_guard_result "$evidence_dir/result.env" "$operation" "$operation_status" \
    "$official_status" "$canvas_status" "$canvas_unchanged"
  printf 'guard evidence: %s\n' "$evidence_dir"

  test "$operation_status" -eq 0 || die "official $operation failed with status $operation_status"
  test "$official_status" -eq 0 || die "official Sub2API post-check failed"
  test "$canvas_status" -eq 0 || die "Canvas stable post-check failed"
  test "$canvas_unchanged" = true || die "Canvas stable changed during the official $operation"
  printf 'official Sub2API %s passed; Canvas stable is unchanged\n' "$operation"
}
