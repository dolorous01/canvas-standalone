#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

slot=''
while test "$#" -gt 0; do
  case "$1" in
    --slot) slot="${2:-}"; shift 2 ;;
    *) die "usage: canvas-backup.sh --slot <stable|candidate>" ;;
  esac
done
validate_slot "$slot"
load_slot_environment "$slot"
require_command docker
require_command sha256sum
require_command tar

container="canvas_${slot}_postgres"
docker inspect "$container" >/dev/null 2>&1 || die "database container does not exist: $container"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup_root="${CANVAS_BACKUP_ROOT:-$REPOSITORY_ROOT/state/$slot/backups}"
destination="$backup_root/$timestamp"
mkdir -p -- "$destination"
chmod 700 "$destination"

database_dump="$destination/sub2api_canvas.dump"
docker exec "$container" pg_dump -Fc -U canvas -d sub2api_canvas >"$database_dump"
test -s "$database_dump" || die "database dump is empty"
docker exec -i "$container" pg_restore -l <"$database_dump" >/dev/null

object_archive="$destination/canvas-objects.tar"
tar --create --file "$object_archive" --directory "$CANVAS_OBJECT_DIR" .
(
  cd -- "$CANVAS_OBJECT_DIR"
  find . -type f -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum
) >"$destination/object-manifest.sha256"

sha256sum "$database_dump" "$object_archive" "$destination/object-manifest.sha256" >"$destination/backup-files.sha256"
current="$(current_release_file "$slot")"
if test -f "$current"; then
  cp -- "$current" "$destination/release.env"
fi
printf '{"slot":"%s","created_at":"%s","database":"sub2api_canvas.dump","objects":"canvas-objects.tar"}\n' \
  "$slot" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$destination/evidence.json"

printf '%s\n' "$destination"
