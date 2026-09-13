#!/usr/bin/env bash

set -Eeuo pipefail
# shellcheck source=sub2api-guard-lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/sub2api-guard-lib.sh"

image=''
route_file=''
reconcile_bearer_file=''
reconcile_report=''
dry_run=false

while test "$#" -gt 0; do
  case "$1" in
    --image) image="${2:-}"; shift 2 ;;
    --route-file) route_file="${2:-}"; shift 2 ;;
    --reconcile-bearer-file) reconcile_bearer_file="${2:-}"; shift 2 ;;
    --reconcile-report) reconcile_report="${2:-}"; shift 2 ;;
    --dry-run) dry_run=true; shift ;;
    *) die "usage: sub2api-release.sh --image IMAGE_DIGEST --route-file PATH [--reconcile-bearer-file PATH] [--reconcile-report PATH] [--dry-run]" ;;
  esac
done

require_image_reference "$image" false
test -n "$route_file" || die "--route-file is required"
validate_reconcile_options "$reconcile_bearer_file" "$reconcile_report"

arguments=("$image")
if test "$dry_run" = true; then
  arguments=(--dry-run "${arguments[@]}")
fi
run_guarded_official_command release "$route_file" deploy.sh "${arguments[@]}"
if test "$dry_run" = false && test -n "$reconcile_bearer_file"; then
  if ! run_post_update_reconcile stable "$route_file" "$reconcile_bearer_file" "$reconcile_report"; then
    die "official Sub2API release passed and remains active, but post-update reconciliation failed"
  fi
fi
