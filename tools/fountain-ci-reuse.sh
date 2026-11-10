#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
# A maintained-branch push reuses only its exact commit's successful push CI.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/fountain-policy.sh"
attempt=${GITHUB_RUN_ATTEMPT:-1}
[[ $attempt =~ ^[1-9][0-9]*$ ]]
started=$(gh api "repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/attempts/$attempt" --jq .run_started_at)
printf 'started_at=%s\n' "$(date --date="$started" +%s)" >> "$GITHUB_OUTPUT"
reused=false
if [[ ${GITHUB_EVENT_NAME:-} == "$fountain_ci_event" && ${GITHUB_REF:-} == "refs/heads/$fountain_maintained_branch" && ${GITHUB_RUN_ATTEMPT:-} == 1 && ${GITHUB_SHA:-} =~ ^[0-9a-f]{40}$ ]]; then
  tag="$fountain_ci_tag_prefix$GITHUB_SHA"
  result=$(mktemp "${TMPDIR:-$HOME}/fountain-ci-reuse.XXXXXXXX")
  trap 'rm -f "$result"' EXIT
  if gh api "repos/$GITHUB_REPOSITORY/actions/workflows/$fountain_ci_workflow/runs?head_sha=$GITHUB_SHA&branch=$tag&event=$fountain_ci_event&per_page=1" > "$result"; then
    if jq -e --arg sha "$GITHUB_SHA" --arg tag "$tag" --arg event "$fountain_ci_event" --arg workflow "$fountain_ci_workflow_path" '
      .workflow_runs | length == 1 and
      (.[0] | (.id | type == "number" and . > 0) and .head_sha == $sha and .head_branch == $tag and
        .event == $event and .path == $workflow and
        .status == "completed" and .conclusion == "success")
    ' "$result" >/dev/null; then
      reused=true
      printf 'Reusing push CI run %s for exact commit %s\n' "$(jq -r '.workflow_runs[0].id' "$result")" "$GITHUB_SHA"
    fi
  fi
fi
printf 'reused=%s\n' "$reused" >> "$GITHUB_OUTPUT"
