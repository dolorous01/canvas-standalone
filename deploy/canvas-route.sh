#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

slot=''
route_file=''
while test "$#" -gt 0; do
  case "$1" in
    --slot) slot="${2:-}"; shift 2 ;;
    --route-file) route_file="${2:-}"; shift 2 ;;
    *) die "usage: canvas-route.sh --slot <stable|candidate> --route-file PATH" ;;
  esac
done
validate_slot "$slot"
test -n "$route_file" || die "route file is required"
[[ "$route_file" = /* ]] || die "route file must be an absolute path"
if test -e "$route_file"; then
  test -f "$route_file" || die "route file is not a regular file"
  test ! -L "$route_file" || die "route file must not be a symlink"
fi
load_slot_environment "$slot"
current="$(current_release_file "$slot")"
test -f "$current" || die "Canvas $slot has no current release"
set -a
# shellcheck disable=SC1090
. "$current"
set +a

require_command flock
acquire_global_deploy_lock
python3 "$DEPLOY_DIR/proxy/update_routes.py" \
  --file "$route_file" \
  --slot "$slot" \
  --web "http://127.0.0.1:${CANVAS_WEB_PORT}" \
  --api "http://127.0.0.1:${CANVAS_API_PORT}" \
  --build-id "$CANVAS_BUILD_ID"
