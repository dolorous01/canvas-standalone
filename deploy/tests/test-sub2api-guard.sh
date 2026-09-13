#!/usr/bin/env bash

set -Eeuo pipefail

DEPLOY_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
# shellcheck source=../sub2api-guard-lib.sh
. "$DEPLOY_DIR/sub2api-guard-lib.sh"

test_root="$(mktemp -d /tmp/sub2api-guard-test.XXXXXX)"
cleanup() { rm -rf -- "$test_root"; }
trap cleanup EXIT
route_file="$test_root/routes.json"
identity_source="$test_root/identity"
operation_mode=success
operation_calls=0
status_calls=0
printf '{}\n' >"$route_file"
printf 'stable-identity\n' >"$identity_source"

global_deploy_lock_file() { printf '%s/deployments.lock\n' "$test_root"; }
official_guard_state_dir() { printf '%s/evidence\n' "$test_root"; }
official_ops_dir() { printf '%s/ops\n' "$test_root"; }
verify_canvas_stable() {
  test "$operation_mode" != pre_verify_failure || return 1
  test "$operation_mode" != post_verify_failure || test "$operation_calls" -eq 0
}
capture_canvas_stable_identity() { cp -- "$identity_source" "$2"; }
official_run() {
  case "$1" in
    */status.sh)
      status_calls=$((status_calls + 1))
      test "$operation_mode" != post_status_failure || test "$operation_calls" -eq 0
      ;;
    */deploy.sh|*/rollback.sh)
      operation_calls=$((operation_calls + 1))
      touch "$operation_marker"
      case "$operation_mode" in
        success|post_status_failure|post_verify_failure) return 0 ;;
        failure) return 23 ;;
        drift) printf 'changed-identity\n' >"$identity_source"; return 0 ;;
      esac
      ;;
    *) return 99 ;;
  esac
}

run_case() {
  local mode="$1"
  local expected_status="$2"
  local expected_operation="$3"
  local case_dir="$test_root/$mode"
  local actual_status=0

  mkdir -p "$case_dir"
  (
    operation_mode="$mode"
    operation_calls=0
    status_calls=0
    operation_marker="$case_dir/operation-called"
    official_guard_state_dir() { printf '%s/evidence\n' "$case_dir"; }
    global_deploy_lock_file() { printf '%s/deployments.lock\n' "$case_dir"; }
    printf 'stable-identity\n' >"$identity_source"
    run_guarded_official_command release "$route_file" deploy.sh --dry-run example-image
  ) >"$case_dir/output.log" 2>&1 || actual_status=$?
  test "$actual_status" -eq "$expected_status" || {
    printf 'case %s: expected status %s, got %s\n' "$mode" "$expected_status" "$actual_status" >&2
    sed -n '1,200p' "$case_dir/output.log" >&2
    return 1
  }
  if test "$expected_operation" = called; then
    test -f "$case_dir/operation-called"
  else
    test ! -e "$case_dir/operation-called"
  fi
}

run_case success 0 called
run_case failure 1 called
run_case drift 1 called
run_case post_status_failure 1 called
run_case post_verify_failure 1 called
run_case pre_verify_failure 1 not-called

printf 'Sub2API guard tests passed\n'
