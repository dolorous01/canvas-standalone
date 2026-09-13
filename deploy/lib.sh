#!/usr/bin/env bash

set -Eeuo pipefail

DEPLOY_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
REPOSITORY_ROOT="$(cd -- "$DEPLOY_DIR/.." && pwd -P)"
COMPOSE_FILE="$DEPLOY_DIR/compose.yml"

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command is unavailable: $1"
}

global_deploy_lock_file() {
  printf '%s/state/deployments.lock\n' "$REPOSITORY_ROOT"
}

acquire_global_deploy_lock() {
  local state_dir="$REPOSITORY_ROOT/state"
  local lock_file

  test ! -L "$state_dir" || die "state directory must not be a symlink: $state_dir"
  mkdir -p -- "$state_dir"
  lock_file="$(global_deploy_lock_file)"
  if test -e "$lock_file"; then
    test -f "$lock_file" || die "global deployment lock is not a regular file: $lock_file"
    test ! -L "$lock_file" || die "global deployment lock must not be a symlink: $lock_file"
  fi
  exec {CANVAS_GLOBAL_DEPLOY_LOCK_FD}>>"$lock_file"
  flock -n "$CANVAS_GLOBAL_DEPLOY_LOCK_FD" || die "another Canvas or Sub2API deployment is running"
}

validate_slot() {
  case "${1:-}" in
    stable|candidate) ;;
    *) die "slot must be stable or candidate" ;;
  esac
}

slot_env_file() {
  printf '%s/environments/%s.env\n' "$DEPLOY_DIR" "$1"
}

load_slot_environment() {
  local slot="$1"
  SLOT_ENV_FILE="$(slot_env_file "$slot")"
  test -f "$SLOT_ENV_FILE" || die "missing environment file: $SLOT_ENV_FILE (run deploy/init-environment.sh --slot $slot)"
  set -a
  # shellcheck disable=SC1090
  . "$SLOT_ENV_FILE"
  set +a
  test "${CANVAS_SLOT:-}" = "$slot" || die "CANVAS_SLOT in $SLOT_ENV_FILE does not match $slot"
  test -n "${CANVAS_OBJECT_DIR:-}" || die "CANVAS_OBJECT_DIR is empty"
  test -n "${CANVAS_SECRETS_DIR:-}" || die "CANVAS_SECRETS_DIR is empty"
}

state_release_dir() {
  printf '%s/state/%s/releases\n' "$REPOSITORY_ROOT" "$1"
}

current_release_file() {
  printf '%s/current.env\n' "$(state_release_dir "$1")"
}

previous_release_file() {
  printf '%s/previous.env\n' "$(state_release_dir "$1")"
}

is_digest_image() {
  [[ "$1" =~ ^[^[:space:]@]+@sha256:[a-f0-9]{64}$ ]]
}

is_local_image_id() {
  [[ "$1" =~ ^sha256:[a-f0-9]{64}$ ]]
}

require_image_reference() {
  local image="$1"
  local allow_local="${2:-false}"
  if is_digest_image "$image"; then
    return
  fi
  if test "$allow_local" = true && is_local_image_id "$image"; then
    return
  fi
  die "image must use repository@sha256:<64 hex> (local rehearsals additionally accept sha256:<64 hex>)"
}

validate_reconcile_options() {
  local bearer_file="${1:-}"
  local report="${2:-}"

  if test -z "$bearer_file"; then
    test -z "$report" || die "--reconcile-report requires --reconcile-bearer-file"
    return
  fi
  require_command stat
  [[ "$bearer_file" = /* ]] || die "reconciliation bearer file path must be absolute"
  test -f "$bearer_file" || die "reconciliation bearer file is missing or is not a regular file"
  test ! -L "$bearer_file" || die "reconciliation bearer file must not be a symlink"
  test "$(stat -c '%a' -- "$bearer_file")" = 600 || die "reconciliation bearer file permissions must be exactly 0600"
  test "$(stat -c '%u' -- "$bearer_file")" = "$EUID" || die "reconciliation bearer file must be owned by the current user"
  if test -n "$report"; then
    [[ "$report" = /* ]] || die "reconciliation report path must be absolute"
    test ! -e "$report" || die "reconciliation report already exists: $report"
    test ! -L "$report" || die "reconciliation report path must not be a symlink"
    test -d "$(dirname -- "$report")" || die "reconciliation report parent directory does not exist"
  fi
}

run_post_update_reconcile() {
  local slot="$1"
  local route_file="$2"
  local bearer_file="$3"
  local report="${4:-}"
  local arguments=(
    --slot "$slot"
    --route-file "$route_file"
    --bearer-file "$bearer_file"
  )
  if test -n "$report"; then
    arguments+=(--report "$report")
  fi
  CANVAS_RECONCILE_PARENT_LOCK_HELD=1 "$DEPLOY_DIR/post-update-reconcile.sh" "${arguments[@]}"
}

canvas_compose() {
  local release_file="$1"
  shift
  docker compose \
    --project-name "canvas-${CANVAS_SLOT}" \
    --env-file "$SLOT_ENV_FILE" \
    --env-file "$release_file" \
    -f "$COMPOSE_FILE" "$@"
}

wait_container_healthy() {
  local container="$1"
  local attempts="${2:-40}"
  local state
  for ((index = 1; index <= attempts; index++)); do
    state="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container" 2>/dev/null || true)"
    case "$state" in
      healthy) return 0 ;;
      unhealthy|exited|dead) die "$container entered state $state" ;;
    esac
    sleep 2
  done
  die "$container did not become healthy"
}

wait_http_ready() {
  local url="$1"
  local successes=0
  for ((index = 1; index <= 30; index++)); do
    if curl --fail --silent --show-error --max-time 5 "$url" >/dev/null; then
      successes=$((successes + 1))
      if test "$successes" -ge 3; then
        return 0
      fi
    else
      successes=0
    fi
    sleep 2
  done
  die "readiness did not pass three consecutive checks: $url"
}

image_id() {
  docker image inspect --format '{{.Id}}' "$1"
}

container_image_id() {
  docker inspect --format '{{.Image}}' "$1"
}
