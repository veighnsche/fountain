#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Qualify and fast-forward the maintained branch within one bounded operation.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/fountain-policy.sh"
budget=${FOUNTAIN_PUSH_TIMEOUT_SECONDS:-$fountain_push_budget_seconds}
[[ $budget =~ ^[0-9]+$ ]] && (( budget >= 2 && budget <= fountain_push_budget_seconds )) || {
  printf 'Push budget must be between 2 and %s seconds.\n' "$fountain_push_budget_seconds" >&2; exit 2;
}
if [[ ${FOUNTAIN_PUSH_BOUNDED:-} != 1 ]]; then
  if timeout --signal=TERM --kill-after=1s "$((budget - 1))s" env FOUNTAIN_PUSH_BOUNDED=1 bash "$0" "$@"; then
    exit 0
  else
    result=$?
    if (( result == 124 || result == 137 )); then
      echo 'Push budget expired. Inspect origin before retrying if publication had started.' >&2
    fi
    exit "$result"
  fi
fi
[[ $# == 0 ]]
cd "$(git rev-parse --show-toplevel)"
bash tools/fountain-check-clean.sh
candidate=$(git rev-parse HEAD)
branch=$fountain_maintained_branch
repository=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
tag="$fountain_ci_tag_prefix$candidate"
mkdir -p .artifacts/fountain-ci
receipt=$(mktemp .artifacts/fountain-ci/push.XXXXXXXX)
trap 'rm -f "$receipt"' EXIT
if git rev-parse --verify "refs/tags/$tag" >/dev/null 2>&1; then
  [[ $(git rev-parse "$tag^{commit}") == "$candidate" ]]
else
  git tag "$tag" "$candidate"
fi
git push origin "refs/tags/$tag"
printf 'Qualifying %s; total push budget %ss.\n' "$candidate" "$budget"
while (( SECONDS < budget - 15 )); do
  gh api "repos/$repository/actions/workflows/$fountain_ci_workflow/runs?head_sha=$candidate&branch=$tag&event=$fountain_ci_event&per_page=1" > "$receipt"
  if jq -e --arg sha "$candidate" --arg tag "$tag" --arg event "$fountain_ci_event" --arg workflow "$fountain_ci_workflow_path" '
    .workflow_runs | length == 1 and
    (.[0] | .head_sha == $sha and .head_branch == $tag and .event == $event and .path == $workflow)
  ' "$receipt" >/dev/null; then
    run=$(jq -r '.workflow_runs[0].id' "$receipt")
    url=$(jq -r '.workflow_runs[0].html_url' "$receipt")
    if [[ $(jq -r '.workflow_runs[0].status' "$receipt") == completed ]]; then
      [[ $(jq -r '.workflow_runs[0].conclusion' "$receipt") == success ]] || {
        printf 'Qualification failed: %s\n' "$url" >&2; exit 1;
      }
      gh api "repos/$repository/commits/$candidate/check-runs?filter=all&per_page=100" > "$receipt"
      jq -e --arg sha "$candidate" --arg run "/actions/runs/$run/" --arg gate "$fountain_ci_gate" --argjson app "$fountain_actions_app_id" '
        any(.check_runs[]; .name == $gate and .head_sha == $sha and .app.id == $app and
          .status == "completed" and .conclusion == "success" and (.details_url | contains($run)))
      ' "$receipt" >/dev/null
      remote=$(git ls-remote origin "refs/heads/$branch" | cut -f1)
      [[ $remote =~ ^[0-9a-f]{40}$ ]]
      git merge-base --is-ancestor "$remote" "$candidate"
      printf 'Publishing qualified commit after %ss: %s\n' "$SECONDS" "$url"
      git push origin "$candidate:refs/heads/$branch"
      [[ $(git ls-remote origin "refs/heads/$branch" | cut -f1) == "$candidate" ]]
      printf 'Published %s in %ss.\n' "$candidate" "$SECONDS"
      exit 0
    fi
    # Stop promptly when a required child has already failed, rather than
    # spending the remaining publication budget waiting for sibling jobs.
    if gh api "repos/$repository/actions/runs/$run/jobs?per_page=100" --jq \
      'any(.jobs[]; .status == "completed" and (.conclusion == "failure" or .conclusion == "cancelled" or .conclusion == "timed_out"))' | grep -qx true; then
      printf 'Qualification failed: %s\n' "$url" >&2
      exit 1
    fi
  fi
  sleep 3
done
echo 'Qualification did not finish within the push budget; maintained branch was not advanced.' >&2
exit 1
