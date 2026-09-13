#!/usr/bin/env bash

set -Eeuo pipefail

repository_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
route_file="${1:-$repository_root/state/proxy-routes.rehearsal.json}"
port="${2:-18082}"
temporary_dir="$(mktemp -d /tmp/canvas-maintenance-smoke.XXXXXX)"
marker="$temporary_dir/legacy-canvas-frozen"
log_file="$temporary_dir/proxy.log"
proxy_pid=''

cleanup() {
  if test -n "$proxy_pid"; then
    kill "$proxy_pid" 2>/dev/null || true
    wait "$proxy_pid" 2>/dev/null || true
  fi
  rm -rf -- "$temporary_dir"
}
trap cleanup EXIT

test -f "$route_file"
python3 "$repository_root/deploy/proxy/sub2api_path_proxy.py" \
  --host 127.0.0.1 \
  --port "$port" \
  --sub2api http://127.0.0.1:18080 \
  --auto-clean http://127.0.0.1:8093 \
  --canvas-routes-file "$route_file" \
  --canvas-source-maintenance-file "$marker" \
  --log-file "$log_file" &
proxy_pid=$!

for _attempt in $(seq 1 50); do
  if curl --fail --silent --show-error --max-time 1 "http://127.0.0.1:${port}/health" >/dev/null; then
    break
  fi
  sleep 0.1
done
curl --fail --silent --show-error --max-time 2 "http://127.0.0.1:${port}/health" >/dev/null

status="$(curl --silent --show-error --max-time 2 --output /dev/null --write-out '%{http_code}' \
  -X POST "http://127.0.0.1:${port}/api/v1/image-canvas/projects")"
test "$status" != 503

touch "$marker"
chmod 600 "$marker"
for path in /api/v1/image-canvas/projects /api/v1/admin/image-canvas/model-policy; do
  response="$temporary_dir/response.json"
  status="$(curl --silent --show-error --max-time 2 --output "$response" --write-out '%{http_code}' \
    -X POST "http://127.0.0.1:${port}${path}")"
  test "$status" = 503
  python3 -c 'import json,sys; payload=json.load(open(sys.argv[1], encoding="utf-8")); assert payload["reason"] == "canvas_source_maintenance"' "$response"
done

status="$(curl --silent --show-error --max-time 2 --output /dev/null --write-out '%{http_code}' \
  "http://127.0.0.1:${port}/api/v1/image-canvas/projects")"
test "$status" != 503

rm -f -- "$marker"
status="$(curl --silent --show-error --max-time 2 --output /dev/null --write-out '%{http_code}' \
  -X POST "http://127.0.0.1:${port}/api/v1/image-canvas/projects")"
test "$status" != 503

printf 'legacy Canvas maintenance proxy smoke passed on port %s\n' "$port"
