#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

slot="${2:-${1:-}}"
if test "${1:-}" = --slot; then
  slot="${2:-}"
fi
validate_slot "$slot"
load_slot_environment "$slot"
current="$(current_release_file "$slot")"

printf 'Canvas slot: %s\n' "$slot"
printf 'Release state: %s\n' "$current"
if test -f "$current"; then
  sed -n -E '/^(CANVAS_RELEASE|CANVAS_BUILD_ID|CANVAS_API_IMAGE|CANVAS_WEB_IMAGE)=/p' "$current"
else
  printf 'not deployed\n'
fi
printf '\n%-32s %-12s %-12s %s\n' CONTAINER STATUS HEALTH IMAGE_ID
for component in postgres api worker web; do
  container="canvas_${slot}_${component}"
  if docker inspect "$container" >/dev/null 2>&1; then
    docker inspect --format '{{.Name}} {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}n/a{{end}} {{.Image}}' "$container" | sed 's#^/##'
  else
    printf '%-32s %-12s %-12s %s\n' "$container" missing n/a n/a
  fi
done
