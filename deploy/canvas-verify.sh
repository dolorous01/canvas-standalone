#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

slot=''
route_file=''
entry_base='http://127.0.0.1:8080'

while test "$#" -gt 0; do
  case "$1" in
    --slot) slot="${2:-}"; shift 2 ;;
    --route-file) route_file="${2:-}"; shift 2 ;;
    --entry-base) entry_base="${2:-}"; shift 2 ;;
    *) die "usage: canvas-verify.sh --slot <stable|candidate> --route-file PATH [--entry-base LOOPBACK_URL]" ;;
  esac
done

validate_slot "$slot"
load_slot_environment "$slot"
require_command curl
require_command docker
require_command jq
require_command python3
test -f "$route_file" || die "route file is missing: $route_file"
[[ "$route_file" = /* ]] || die "route file must be an absolute path"
[[ "$entry_base" =~ ^http://(127\.0\.0\.1|localhost)(:[0-9]{1,5})?$ ]] || die "entry base must be a loopback HTTP URL"

current="$(current_release_file "$slot")"
test -f "$current" || die "Canvas $slot has no current release"
set -a
# shellcheck disable=SC1090
. "$current"
set +a
[[ "$CANVAS_BUILD_ID" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || die "current release has an invalid build ID"

python3 - "$route_file" "$slot" "$CANVAS_BUILD_ID" "$CANVAS_WEB_PORT" "$CANVAS_API_PORT" <<'PY'
import json
import sys

path, slot, build_id, web_port, api_port = sys.argv[1:]
with open(path, encoding="utf-8") as handle:
    payload = json.load(handle)
expected = {
    "web": f"http://127.0.0.1:{web_port}",
    "api": f"http://127.0.0.1:{api_port}",
    "build_id": build_id,
}
if payload.get(slot) != expected:
    raise SystemExit(f"route mismatch for {slot}: expected {expected!r}")
PY

test "$(container_image_id "canvas_${slot}_api")" = "$(image_id "$CANVAS_API_IMAGE")" || die "API container image ID mismatch"
test "$(container_image_id "canvas_${slot}_worker")" = "$(image_id "$CANVAS_API_IMAGE")" || die "worker container image ID mismatch"
test "$(container_image_id "canvas_${slot}_web")" = "$(image_id "$CANVAS_WEB_IMAGE")" || die "web container image ID mismatch"

direct_ready="$(curl --fail --silent --show-error --max-time 5 "http://127.0.0.1:${CANVAS_API_PORT}/canvas-api/health/ready")"
printf '%s' "$direct_ready" | jq -e --arg release "$CANVAS_RELEASE" '.status == "ready" and .release == $release' >/dev/null
curl --fail --silent --show-error --max-time 5 "${entry_base}/health" | jq -e '.status == "ok"' >/dev/null

if test "$slot" = candidate; then
  page_path='/studio-next'
  api_prefix='/canvas-api-next'
else
  page_path='/studio'
  api_prefix='/canvas-api'
fi

public_ready="$(curl --fail --silent --show-error --max-time 5 "${entry_base}${api_prefix}/health/ready")"
printf '%s' "$public_ready" | jq -e --arg release "$CANVAS_RELEASE" '.status == "ready" and .release == $release' >/dev/null
test "$(curl --silent --show-error --max-time 5 --output /dev/null --write-out '%{http_code}' "${entry_base}${api_prefix}/v1/session")" = 401

temporary_dir="$(mktemp -d /tmp/canvas-verify.XXXXXX)"
cleanup() { rm -rf -- "$temporary_dir"; }
trap cleanup EXIT
html_file="$temporary_dir/studio.html"
headers_file="$temporary_dir/asset.headers"
asset_file="$temporary_dir/asset.body"
curl --fail --silent --show-error --max-time 10 --output "$html_file" "${entry_base}${page_path}"
grep -F "/canvas-static/${CANVAS_BUILD_ID}/assets/" "$html_file" >/dev/null
asset_path="$(sed -n 's#.*src="\([^\"]*canvas-[^\"]*\.js\)".*#\1#p' "$html_file" | head -1)"
test -n "$asset_path" || die "Canvas entry HTML has no versioned JavaScript asset"
curl --fail --silent --show-error --max-time 10 --range 0-127 \
  --dump-header "$headers_file" --output "$asset_file" "${entry_base}${asset_path}"
grep -Eiq '^content-type:[[:space:]]*(application|text)/(javascript|x-javascript)' "$headers_file" || die "Canvas JavaScript path returned a non-JavaScript content type"
if grep -Eiq '<!doctype[[:space:]]+html|<html' "$asset_file"; then
  die "Canvas JavaScript path returned HTML"
fi

printf 'Canvas %s verification passed\nrelease=%s\nbuild_id=%s\nentry=%s%s\n' \
  "$slot" "$CANVAS_RELEASE" "$CANVAS_BUILD_ID" "$entry_base" "$page_path"
