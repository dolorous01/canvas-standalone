#!/usr/bin/env bash

set -Eeuo pipefail

DEPLOY_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
test_root="$(mktemp -d /tmp/canvas-writes-test.XXXXXX)"
cleanup() { rm -rf -- "$test_root"; }
trap cleanup EXIT

repository="$test_root/repository"
fake_bin="$test_root/bin"
mkdir -p \
  "$repository/deploy/environments" \
  "$repository/state/stable/releases" \
  "$repository/state/stable/objects" \
  "$repository/state/stable/secrets" \
  "$fake_bin"
cp -- "$DEPLOY_DIR/canvas-writes.sh" "$DEPLOY_DIR/lib.sh" "$repository/deploy/"
touch "$repository/deploy/compose.yml"

cat >"$repository/deploy/environments/stable.env" <<EOF
CANVAS_SLOT=stable
CANVAS_WEB_PORT=18100
CANVAS_API_PORT=18101
CANVAS_OBJECT_DIR=$repository/state/stable/objects
CANVAS_SECRETS_DIR=$repository/state/stable/secrets
CANVAS_WRITES_ENABLED=false
EOF
cat >"$repository/state/stable/releases/current.env" <<'EOF'
CANVAS_API_IMAGE=example.invalid/canvas-api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
CANVAS_WEB_IMAGE=example.invalid/canvas-web@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
CANVAS_RELEASE=0.1.0
CANVAS_BUILD_ID=build.test
EOF
printf '{}\n' >"$repository/routes.json"

cat >"$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
if test "${1:-}" = inspect && test "${2:-}" = --format; then
  case "${3:-}" in
    *State.Health*) printf 'healthy\n' ;;
    *Config.Env*) printf 'CANVAS_WRITES_ENABLED=%s\n' "$CANVAS_WRITES_ENABLED" ;;
  esac
fi
EOF
cat >"$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat >"$repository/deploy/canvas-verify.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod 0755 "$fake_bin/docker" "$fake_bin/curl" "$repository/deploy/canvas-verify.sh"

PATH="$fake_bin:$PATH" "$repository/deploy/canvas-writes.sh" \
  --enable \
  --route-file "$repository/routes.json" \
  --operator-external-id 1
grep -Fx 'CANVAS_WRITES_ENABLED=true' "$repository/deploy/environments/stable.env" >/dev/null
grep -F '"writes_enabled":true' "$repository/state/stable/releases/audit.jsonl" >/dev/null
grep -F '"release":"0.1.0","build_id":"build.test"' "$repository/state/stable/releases/audit.jsonl" >/dev/null

PATH="$fake_bin:$PATH" "$repository/deploy/canvas-writes.sh" \
  --disable \
  --route-file "$repository/routes.json" \
  --operator-external-id 1
grep -Fx 'CANVAS_WRITES_ENABLED=false' "$repository/deploy/environments/stable.env" >/dev/null
test "$(wc -l <"$repository/state/stable/releases/audit.jsonl")" -eq 2

printf 'Canvas write-mode shell tests passed\n'
