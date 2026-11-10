#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/fountain-policy.sh"
expected=success
if [[ ${FOUNTAIN_CI_PROFILE:-push} != full ]]; then
  [[ ${REUSE_RESULT:-missing} == success ]]
  case ${REUSED_CHECKS:-missing} in
    false) ;;
    true) expected=skipped ;;
    *) echo 'Missing or invalid exact-commit admission result' >&2; exit 1 ;;
  esac
  [[ ${GATE_STARTED_AT:-} =~ ^[0-9]+$ ]]
  elapsed=$(( $(date +%s) - GATE_STARTED_AT ))
  if (( elapsed < 0 || elapsed >= fountain_push_budget_seconds )); then
    printf 'Push qualification exceeded its five-minute budget (%ss)\n' "$elapsed" >&2
    exit 1
  fi
fi
for lane in "${fountain_required_lanes[@]}"; do
  variable="${lane}_RESULT"
  if [[ ${!variable:-missing} != "$expected" ]]; then
    printf '%s must be %s (received %s)\n' "$lane" "$expected" "${!variable:-missing}" >&2
    exit 1
  fi
done
