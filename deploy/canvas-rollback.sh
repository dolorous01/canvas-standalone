#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

slot=''
route_file=''
dry_run=false
while test "$#" -gt 0; do
  case "$1" in
    --slot) slot="${2:-}"; shift 2 ;;
    --route-file) route_file="${2:-}"; shift 2 ;;
    --dry-run) dry_run=true; shift ;;
    *) die "usage: canvas-rollback.sh --slot <stable|candidate> --route-file PATH [--dry-run]" ;;
  esac
done
validate_slot "$slot"
test -n "$route_file" || die "--route-file is required so static assets follow the rolled-back build"
previous="$(previous_release_file "$slot")"
test -f "$previous" || die "no previous release is recorded for $slot"

set -a
# shellcheck disable=SC1090
. "$previous"
set +a
arguments=(
  --slot "$slot"
  --web-image "$CANVAS_WEB_IMAGE"
  --api-image "$CANVAS_API_IMAGE"
  --release "$CANVAS_RELEASE"
  --build-id "$CANVAS_BUILD_ID"
  --route-file "$route_file"
)
if is_local_image_id "$CANVAS_WEB_IMAGE" || is_local_image_id "$CANVAS_API_IMAGE"; then
  arguments+=(--allow-local-image)
fi
if test "$dry_run" = true; then
  arguments+=(--dry-run)
fi
exec "$DEPLOY_DIR/canvas-release.sh" "${arguments[@]}"
