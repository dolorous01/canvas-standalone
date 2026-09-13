#!/usr/bin/env bash

set -Eeuo pipefail

DEPLOY_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
route_file="${1:-}"
port="${CANVAS_PROXY_TEST_PORT:-18088}"
test -f "$route_file" || { printf 'usage: smoke-proxy.sh ROUTE_FILE\n' >&2; exit 2; }

log_file="$(mktemp /tmp/canvas-proxy-smoke.XXXXXX.log)"
asset_headers="$(mktemp /tmp/canvas-proxy-asset.XXXXXX.headers)"
asset_body="$(mktemp /tmp/canvas-proxy-asset.XXXXXX.body)"
build_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1], encoding="utf-8"))["candidate"]["build_id"])' "$route_file")"
test -n "$build_id"
python3 "$DEPLOY_DIR/proxy/sub2api_path_proxy.py" \
  --host 127.0.0.1 \
  --port "$port" \
  --sub2api http://127.0.0.1:18080 \
  --auto-clean http://127.0.0.1:8093 \
  --canvas-routes-file "$route_file" \
  --log-file "$log_file" &
proxy_pid=$!
cleanup() {
  kill "$proxy_pid" 2>/dev/null || true
  wait "$proxy_pid" 2>/dev/null || true
  rm -f -- "$log_file" "$asset_headers" "$asset_body"
}
trap cleanup EXIT

for _ in $(seq 1 30); do
  if curl --fail --silent --max-time 2 "http://127.0.0.1:${port}/health" >/dev/null; then
    break
  fi
  sleep 0.2
done
curl --fail --silent --show-error "http://127.0.0.1:${port}/health" >/dev/null

html="$(curl --fail --silent --show-error "http://127.0.0.1:${port}/studio-next")"
grep -F "/canvas-static/${build_id}/assets/" <<<"$html" >/dev/null
asset_path="$(sed -n 's#.*src="\([^\"]*canvas-[^\"]*\.js\)".*#\1#p' <<<"$html" | head -1)"
test -n "$asset_path"
curl --fail --silent --show-error --range 0-127 --dump-header "$asset_headers" --output "$asset_body" "http://127.0.0.1:${port}${asset_path}"
grep -Eiq '^content-type:[[:space:]]*(application|text)/(javascript|x-javascript)' "$asset_headers"
if grep -Eiq '<!doctype[[:space:]]+html|<html' "$asset_body"; then
  printf 'candidate JavaScript path returned HTML: %s\n' "$asset_path" >&2
  exit 1
fi
curl --fail --silent --show-error "http://127.0.0.1:${port}/canvas-api-next/health/ready" >/dev/null

test "$(curl --silent --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:${port}/canvas-api-next/v1/session")" = 401
test "$(curl --silent --output /dev/null --write-out '%{http_code}' -H 'Transfer-Encoding: chunked' --data-binary 'bounded-body' "http://127.0.0.1:${port}/canvas-api-next/v1/session")" = 401
test "$(curl --silent --output /dev/null --write-out '%{http_code}' -X POST "http://127.0.0.1:${port}/api/v1/admin/system/update?source=smoke")" = 403
test "$(curl --silent --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:${port}/studioevil")" != 502

printf 'proxy smoke passed on 127.0.0.1:%s\n' "$port"
