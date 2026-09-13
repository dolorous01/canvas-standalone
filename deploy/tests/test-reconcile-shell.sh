#!/usr/bin/env bash

set -Eeuo pipefail

TEST_DEPLOY_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
# shellcheck source=../lib.sh
. "$TEST_DEPLOY_DIR/lib.sh"

test_root="$(mktemp -d /tmp/canvas-reconcile-shell-test.XXXXXX)"
cleanup() { rm -rf -- "$test_root"; }
trap cleanup EXIT

bearer_file="$test_root/bearer"
report_file="$test_root/report.json"
printf 'opaque-test-token\n' >"$bearer_file"
chmod 0600 "$bearer_file"

expect_failure() {
  if ("$@") >"$test_root/expected-failure.log" 2>&1; then
    printf 'expected command to fail: %s\n' "$*" >&2
    return 1
  fi
}

validate_reconcile_options "$bearer_file" "$report_file"
expect_failure validate_reconcile_options '' "$report_file"
chmod 0640 "$bearer_file"
expect_failure validate_reconcile_options "$bearer_file" "$report_file"
chmod 0600 "$bearer_file"
ln -s "$test_root/unused" "$report_file"
expect_failure validate_reconcile_options "$bearer_file" "$report_file"
rm -- "$report_file"

fake_deploy_dir="$test_root/deploy"
mkdir -p -- "$fake_deploy_dir"
cat >"$fake_deploy_dir/post-update-reconcile.sh" <<'SH'
#!/usr/bin/env bash
set -Eeuo pipefail
test "${CANVAS_RECONCILE_PARENT_LOCK_HELD:-}" = 1
printf '%s\n' "$@" >"$RECONCILE_ARGUMENT_LOG"
SH
chmod 0755 "$fake_deploy_dir/post-update-reconcile.sh"

original_deploy_dir="$DEPLOY_DIR"
DEPLOY_DIR="$fake_deploy_dir"
export RECONCILE_ARGUMENT_LOG="$test_root/arguments"
run_post_update_reconcile stable /absolute/routes.json "$bearer_file" "$report_file"
DEPLOY_DIR="$original_deploy_dir"

expected_arguments="$test_root/expected-arguments"
cat >"$expected_arguments" <<EOF
--slot
stable
--route-file
/absolute/routes.json
--bearer-file
$bearer_file
--report
$report_file
EOF
cmp -- "$expected_arguments" "$test_root/arguments"

printf 'reconciliation shell tests passed\n'
