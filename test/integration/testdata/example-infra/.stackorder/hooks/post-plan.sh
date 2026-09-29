#!/usr/bin/env bash
set -euo pipefail

stack="${STACKORDER_STACK:-unknown stack}"
plan_json="${STACKORDER_PLAN_JSON:-}"

count() {
  jq -r --arg stack "$stack" '
    (.resource_changes // []) as $all
    | ($all | map(select(.change.actions != ["no-op"] and .change.actions != ["read"]))) as $changed
    | "post-plan: \($stack): \($all | length) resources in plan, \($changed | length) with changes"
  ' "$@"
}

if [[ -z "$plan_json" ]]; then
  echo "post-plan: ${stack}: STACKORDER_PLAN_JSON is not set; nothing to count"
elif ! command -v jq >/dev/null 2>&1; then
  echo "post-plan: ${stack}: jq is not installed; skipping the resource count"
elif [[ -f "$plan_json" ]]; then
  count "$plan_json"
else
  echo "post-plan: ${stack}: ${plan_json} is not a file; nothing to count"
fi
