#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=sub2api-guard-lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/sub2api-guard-lib.sh"

route_file=''
dry_run=false

while test "$#" -gt 0; do
  case "$1" in
    --route-file) route_file="${2:-}"; shift 2 ;;
    --dry-run) dry_run=true; shift ;;
    *) die "usage: sub2api-rollback.sh --route-file PATH [--dry-run]" ;;
  esac
done

test -n "$route_file" || die "--route-file is required"
arguments=()
if test "$dry_run" = true; then
  arguments+=(--dry-run)
fi
run_guarded_official_command rollback "$route_file" rollback.sh "${arguments[@]}"
