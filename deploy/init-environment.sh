#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

slot=''
while test "$#" -gt 0; do
  case "$1" in
    --slot) slot="${2:-}"; shift 2 ;;
    *) die "usage: init-environment.sh --slot <stable|candidate>" ;;
  esac
done
validate_slot "$slot"
require_command openssl

target="$(slot_env_file "$slot")"
template="${target}.example"
test -f "$template" || die "missing template: $template"
if test ! -f "$target"; then
  cp -- "$template" "$target"
  chmod 600 "$target"
fi

load_slot_environment "$slot"
mkdir -p -- "$CANVAS_OBJECT_DIR" "$CANVAS_SECRETS_DIR" "$(state_release_dir "$slot")"
chmod 700 "$CANVAS_OBJECT_DIR" "$CANVAS_SECRETS_DIR" "$(state_release_dir "$slot")"

password_file="$CANVAS_SECRETS_DIR/postgres-password"
database_url_file="$CANVAS_SECRETS_DIR/database-url"
credential_file="$CANVAS_SECRETS_DIR/credential-key-v1"

if test ! -e "$password_file"; then
  umask 077
  openssl rand -hex 32 >"$password_file"
fi
if test ! -e "$credential_file"; then
  umask 077
  openssl rand -base64 32 | tr -d '\n' >"$credential_file"
fi
if test ! -e "$database_url_file"; then
  password="$(tr -d '\r\n' <"$password_file")"
  umask 077
  printf 'postgresql://canvas:%s@database:5432/sub2api_canvas?sslmode=disable\n' "$password" >"$database_url_file"
  unset password
fi

chmod 600 "$target" "$password_file" "$database_url_file" "$credential_file"
for secret in "$password_file" "$database_url_file" "$credential_file"; do
  test -f "$secret" || die "secret is not a regular file: $secret"
  test ! -L "$secret" || die "secret must not be a symlink: $secret"
  mode="$(stat -c '%a' "$secret")"
  test "$mode" = 600 || die "secret mode must be 600: $secret"
done

printf 'initialized Canvas %s environment\nconfig: %s\nstate: %s\n' "$slot" "$target" "$REPOSITORY_ROOT/state/$slot"
