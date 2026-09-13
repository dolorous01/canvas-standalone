#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

slot=''
route_file=''
bearer_file=''
report=''
entry_base='http://127.0.0.1:8080'

while test "$#" -gt 0; do
  case "$1" in
    --slot) slot="${2:-}"; shift 2 ;;
    --route-file) route_file="${2:-}"; shift 2 ;;
    --bearer-file) bearer_file="${2:-}"; shift 2 ;;
    --report) report="${2:-}"; shift 2 ;;
    --entry-base) entry_base="${2:-}"; shift 2 ;;
    *) die "usage: post-update-reconcile.sh --slot <stable|candidate> --route-file PATH --bearer-file PATH [--report PATH] [--entry-base LOOPBACK_URL]" ;;
  esac
done

validate_slot "$slot"
require_command date
require_command flock
require_command python3
require_command stat
test -n "$route_file" || die "--route-file is required"
test -n "$bearer_file" || die "--bearer-file is required"
[[ "$route_file" = /* ]] || die "route file must be an absolute path"
[[ "$bearer_file" = /* ]] || die "bearer file must be an absolute path"
test -f "$bearer_file" || die "bearer file is missing or is not a regular file"
test ! -L "$bearer_file" || die "bearer file must not be a symlink"
test "$(stat -c '%a' -- "$bearer_file")" = 600 || die "bearer file permissions must be exactly 0600"
test "$(stat -c '%u' -- "$bearer_file")" = "$EUID" || die "bearer file must be owned by the current user"

if test -z "$report"; then
  report_dir="$REPOSITORY_ROOT/state/$slot/reconcile"
  test ! -L "$report_dir" || die "reconciliation report directory must not be a symlink"
  mkdir -p -- "$report_dir"
  chmod 0700 "$report_dir"
  report="$report_dir/reconcile-$(date -u +%Y%m%dT%H%M%SZ)-$$.json"
fi
[[ "$report" = /* ]] || die "report path must be absolute"
test ! -e "$report" || die "report already exists; refusing to overwrite evidence: $report"
test ! -L "$report" || die "report path must not be a symlink"
test -d "$(dirname -- "$report")" || die "report parent directory does not exist"

if test "${CANVAS_RECONCILE_PARENT_LOCK_HELD:-0}" != 1; then
  acquire_global_deploy_lock
fi

"$DEPLOY_DIR/canvas-verify.sh" \
  --slot "$slot" \
  --route-file "$route_file" \
  --entry-base "$entry_base"

python3 "$DEPLOY_DIR/reconcile/post_update_reconcile.py" \
  --slot "$slot" \
  --entry-base "$entry_base" \
  --bearer-file "$bearer_file" \
  --report "$report"
