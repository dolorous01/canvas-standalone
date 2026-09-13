#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

require_command docker
version="$(tr -d '[:space:]' <"$REPOSITORY_ROOT/VERSION")"
build_id="$version"
if ! revision="$(git -C "$REPOSITORY_ROOT" rev-parse --verify HEAD 2>/dev/null)"; then
  revision='uncommitted'
fi
source_url="https://github.com/dolorous01/sub2api/tree/6b390391c7d418567f2342c0b99fa0d558eaaece"

while test "$#" -gt 0; do
  case "$1" in
    --version) version="${2:-}"; shift 2 ;;
    --build-id) build_id="${2:-}"; shift 2 ;;
    --revision) revision="${2:-}"; shift 2 ;;
    --source-url) source_url="${2:-}"; shift 2 ;;
    *) die "usage: build-local.sh [--version VALUE] [--build-id VALUE] [--revision VALUE] [--source-url HTTPS_URL]" ;;
  esac
done

[[ "$version" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || die "invalid version"
[[ "$build_id" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || die "invalid build ID"
[[ "$revision" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || die "invalid revision"
[[ "$source_url" =~ ^https:// ]] || die "source URL must use HTTPS"
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

docker build --pull=false \
  --file "$DEPLOY_DIR/images/api.Dockerfile" \
  --build-arg "CANVAS_VERSION=$version" \
  --build-arg "CANVAS_REVISION=$revision" \
  --build-arg "CANVAS_BUILD_TIME=$build_time" \
  --build-arg "CANVAS_SOURCE_URL=$source_url" \
  --tag "canvas-standalone-api:$build_id" \
  "$REPOSITORY_ROOT"

docker build --pull=false \
  --file "$DEPLOY_DIR/images/web.Dockerfile" \
  --build-arg "CANVAS_VERSION=$version" \
  --build-arg "CANVAS_BUILD_ID=$build_id" \
  --build-arg "CANVAS_REVISION=$revision" \
  --build-arg "CANVAS_SOURCE_URL=$source_url" \
  --tag "canvas-standalone-web:$build_id" \
  "$REPOSITORY_ROOT"

api_id="$(image_id "canvas-standalone-api:$build_id")"
web_id="$(image_id "canvas-standalone-web:$build_id")"
output="$DEPLOY_DIR/local-images.env"
umask 077
{
  printf 'CANVAS_API_IMAGE=%s\n' "$api_id"
  printf 'CANVAS_WEB_IMAGE=%s\n' "$web_id"
  printf 'CANVAS_RELEASE=%s\n' "$version"
  printf 'CANVAS_BUILD_ID=%s\n' "$build_id"
} >"$output"

printf 'local rehearsal images built\nAPI: %s\nWeb: %s\nEnvironment: %s\n' "$api_id" "$web_id" "$output"
