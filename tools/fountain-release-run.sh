#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Drive a Fountain release from one command: preflight, tag, dispatch, watch.
# Usage: bash tools/fountain-release-run.sh VERSION [--dry-run] [--yes] [--move-tag]
# VERSION looks like 15.0.10-fountain.2 (the leading v is added for the tag).
# --dry-run validates only (source gates, build once, test image) and publishes
# nothing; it needs no tag. --yes skips the confirmation prompt.
# Published versions are immutable; --move-tag is refused.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/fountain-policy.sh"

version=${1:-}
dry_run=0
yes=0
for arg in "${@:2}"; do
  case $arg in
    --dry-run) dry_run=1 ;;
    --yes) yes=1 ;;
    --move-tag) echo 'release: version tags are immutable; choose a new version instead of --move-tag' >&2; exit 2 ;;
    *) echo "Usage: $0 VERSION [--dry-run] [--yes] [--move-tag]" >&2; exit 2 ;;
  esac
done
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+-fountain\.[1-9][0-9]*$ ]] || {
  echo "release: VERSION '$version' must look like 15.0.10-fountain.2" >&2; exit 2;
}

cd "$(git rev-parse --show-toplevel)"
branch=$fountain_maintained_branch
[[ $(git branch --show-current) == "$branch" ]] || {
  echo "release: run from the maintained branch '$branch'" >&2; exit 2;
}
bash tools/fountain-check-clean.sh
repository=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
commit=$(git rev-parse HEAD)
tag="v$version"

remote=$(git ls-remote origin "refs/heads/$branch" | cut -f1)
if (( dry_run == 1 )) && [[ $remote != "$commit" ]]; then
  echo "release: dry-run requires $commit already on $branch; qualify and push the candidate separately" >&2
  exit 2
fi

local_tag=0
remote_tag_commit=''
if (( dry_run == 0 )); then
  if git rev-parse --verify "refs/tags/$tag" >/dev/null 2>&1; then
    local_tag=1
    pointed=$(git rev-parse "$tag^{commit}")
    [[ $pointed == "$commit" ]] || {
      echo "release: immutable tag $tag points at $pointed, not $commit; choose a new version" >&2; exit 2;
    }
  fi
  remote_tags=$(git ls-remote origin "refs/tags/$tag" "refs/tags/$tag^{}")
  remote_tag_object=''
  while IFS=$'\t' read -r object ref; do
    case $ref in
      "refs/tags/$tag") remote_tag_object=$object ;;
      "refs/tags/$tag^{}") remote_tag_commit=$object ;;
    esac
  done <<< "$remote_tags"
  remote_tag_commit=${remote_tag_commit:-$remote_tag_object}
  [[ -z $remote_tag_commit || $remote_tag_commit == "$commit" ]] || {
    echo "release: immutable remote tag $tag points at $remote_tag_commit, not $commit; choose a new version" >&2; exit 2;
  }
fi

if (( dry_run == 1 )); then mode=dry-run; else mode=publish; fi
if (( yes == 0 )); then
  [[ -t 0 ]] || { echo 'release: refusing to run without --yes on a non-terminal' >&2; exit 2; }
  printf 'Fountain %s (%s) from %s on %s? [yes/NO] ' "$version" "$mode" "$commit" "$branch"
  read -r answer
  [[ $answer == yes ]] || exit 2
fi

if (( dry_run == 0 )); then
  if [[ $remote != "$commit" ]]; then
    echo "release: pushing $commit to $branch first"
    git push origin "HEAD:refs/heads/$branch"
  fi
  if [[ -z $remote_tag_commit ]]; then
    if (( local_tag == 0 )); then git tag "$tag" "$commit"; fi
    git push origin "refs/tags/$tag"
  fi
fi

if (( dry_run == 1 )); then
  url=$(gh workflow run "$fountain_release_workflow" --ref "$branch" -f tag="$tag" -F dry_run=true --repo "$repository")
else
  url=$(gh workflow run "$fountain_release_workflow" --ref "$branch" -f tag="$tag" --repo "$repository")
fi
# Captured gh workflow output returns the created run URL. Never substitute
# another same-commit run: it may belong to a concurrent dispatch or old attempt.
prefix="https://github.com/$repository/actions/runs/"
run=${url#"$prefix"}
[[ $url == "$prefix"* && $run =~ ^[1-9][0-9]*$ ]] || {
  echo "release: dispatch did not return one exact run URL for $repository; inspect the Actions tab" >&2; exit 1;
}
run_commit=$(gh run view "$run" --repo "$repository" --json headSha --jq .headSha)
[[ $run_commit == "$commit" ]] || { echo "release: dispatched run uses $run_commit instead of $commit: $url" >&2; exit 1; }
printf 'Watching %s\n' "$url"
if timeout --signal=TERM --kill-after=30s 3900s gh run watch "$run" --exit-status --repo "$repository"; then
  if (( dry_run == 1 )); then
    echo "Fountain $version validated from $commit; nothing was published."
  else
    gh release view "$tag" --repo "$repository" --json url --jq '"Published: " + .url'
    printf 'Release metadata: https://github.com/%s/releases/download/%s/release.json\n' "$repository" "$tag"
  fi
  exit 0
else
  result=$?
fi
if (( result == 124 || result == 137 )); then
  printf 'Watch timed out; the run continues at %s\n' "$url" >&2
  exit 2
fi
printf 'Release run failed: %s\n' "$url" >&2
gh run view "$run" --repo "$repository" --log-failed 2>/dev/null | grep -a -A 120 'container log (tail)' | cut -c1-250 | head -n 130 || true
gh run view "$run" --repo "$repository" --log-failed 2>/dev/null | grep -a '##\[error\]' | cut -c1-250 | head -n 10 || true
exit 1
