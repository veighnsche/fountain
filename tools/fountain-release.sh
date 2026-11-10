#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Local build only; publication is owned by the GitHub workflow.
set -euo pipefail
tool_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source "$tool_dir/fountain-release-names.sh"
source "$tool_dir/fountain-toolchain.sh"
if [[ $# != 3 ]]; then
  echo "Usage: $0 VERSION FULL_COMMIT NEW_OUTPUT_DIRECTORY" >&2
  exit 2
fi
version=$1
commit=$2
out=$3
fountain_release_names "$version" || { echo "release: VERSION '$version' must look like 15.0.10-fountain.2" >&2; exit 1; }
[[ $commit =~ ^[0-9a-f]{40}$ ]] || { echo "release: COMMIT '$commit' must be a 40-character hex commit" >&2; exit 1; }
[[ $out == /home/* && $(uname -sm) == 'Linux x86_64' ]] || { echo "release: output '$out' must live under /home on native Linux x86_64 ($(uname -sm))" >&2; exit 1; }
head_commit=$(git rev-parse HEAD 2>/dev/null) || { echo "release: cannot read HEAD from this checkout" >&2; exit 1; }
[[ $head_commit == "$commit" ]] || { echo "release: HEAD $head_commit is not the release commit $commit" >&2; exit 1; }
dirty=$(git status --porcelain --untracked-files=normal) || { echo "release: cannot inspect checkout cleanliness" >&2; exit 1; }
[[ -z $dirty ]] || { echo "release: checkout has uncommitted changes:" >&2; echo "$dirty" | head -n 10 >&2; exit 1; }
for tool in go npm gcc make tar gzip jq sha256sum "${CONTAINER_ENGINE:-docker}"; do command -v "$tool" >/dev/null || { echo "release: required tool missing: $tool" >&2; exit 1; }; done
mkdir "$out" 2>/dev/null || { echo "release: cannot create fresh output '$out' (refusing to overwrite an earlier attempt)" >&2; exit 1; }
out=$(realpath "$out")
mkdir "$out/source"
# Extension fixtures use Unix sockets; keep temporary paths short even when
# the chosen output directory is deeply nested.
export TMPDIR="${TMPDIR:-$(mktemp -d "$HOME/.fountain-tmp.XXXXXX")}"
export npm_config_cache="${npm_config_cache:-$HOME/.npm}"
source_root=$(git rev-parse --show-toplevel)
GOTOOLCHAIN=$(fountain_go_toolchain "$source_root")
export GOTOOLCHAIN
export GOCACHE="${GOCACHE:-$HOME/.cache/go-build}"
export GOPATH="${GOPATH:-$HOME/go}"
for path in "$TMPDIR" "$npm_config_cache" "$GOCACHE" "$GOPATH"; do [[ $path == /home/* ]] || { echo "release: scratch path '$path' must live under /home" >&2; exit 1; }; done
git archive --format=tar --prefix="$fountain_source_root/" "$commit" | gzip -1n > "$out/$fountain_source_archive"
[[ $(gzip -dc "$out/$fountain_source_archive" | { git get-tar-commit-id; cat >/dev/null; }) == "$commit" ]]
tar -xzf "$out/$fountain_source_archive" --strip-components=1 -C "$out/source"
cd "$out/source"
# Upstream format/tidy gates compare generated changes with Git. Give them an
# isolated baseline instead of accidentally inspecting the caller's checkout.
git init -q
git add --force .
git -c user.name=release-check -c user.email=release-check@example.invalid commit -qm 'Exact source baseline'
npm ci --no-audit --no-fund
# Reuse the upstream source gates, then the SDK/host fixtures relevant to this track.
FOUNTAIN_CHECK_ISOLATED=1 make fountain-check-release
mkdir "$out/example"
cp -R contrib/extensions/example/extension.json contrib/extensions/example/assets "$out/example/"
CGO_ENABLED=0 go build -trimpath -o "$out/example/backend" ./contrib/extensions/example
chmod 755 "$out/example/backend"
# Upstream Makefile archive identity; no .git or workstation state enters the build.
printf '%s\n' "$fountain_full_version" > VERSION
rm -rf "$out/source/.git" # Only the task-owned source-check baseline.
engine=${CONTAINER_ENGINE:-docker}
build=("$engine" build)
if [[ $engine == docker && ${GITHUB_ACTIONS:-} == true ]]; then
  build=(docker buildx build --load --cache-from type=gha,scope=fountain-runtime --cache-to type=gha,mode=max,scope=fountain-runtime)
fi
"${build[@]}" --platform "$fountain_platform" \
  --build-arg "RELEASE_VERSION=$version" --build-arg "SOURCE_REVISION=$commit" \
  --build-arg "SOURCE_URL=https://github.com/${GITHUB_REPOSITORY:-veighnsche/fountain}" \
  --iidfile "$out/image-id" -t "fountain-release:$version" .
image=$(cat "$out/image-id")
bash tools/fountain-image-test.sh "$image" "$version" "$commit" "$out"
# Copy the actual shipped executable, not a second compilation.
container=$("$engine" create "$image")
trap '"$engine" rm -fv "$container" >/dev/null' EXIT
"$engine" cp "$container:/app/gitea/gitea" "$out/$fountain_binary"
"$engine" cp "$container:/usr/share/licenses/fountain/LICENSE" "$out/LICENSE"
"$engine" rm -v "$container" >/dev/null
trap - EXIT
# Recover the shipped frontend, then reuse upstream's source distribution
# recipe (generated bindata and vendored Go dependencies, without recompiling).
"$out/$fountain_binary" --work-path "$out/source" \
  --custom-path "$out/source/custom" --config "$out/source/custom/conf/app.ini" \
  embedded extract --overwrite --include-vendored --destination "$out/source" 'public/**'
GZIP=-1 make SHASUM=sha256sum VERSION="$fountain_full_version" TAGS=bindata FORGEJO_GENERATE_SKIP_HASH=true sources-tarbal
cp "dist/release/$fountain_prepared_archive" "$out/$fountain_prepared_archive"
gzip -1n "$out/$fountain_binary"
sha256sum "$out/$fountain_source_archive" "$out/$fountain_prepared_archive" "$out/$fountain_binary_archive" "$out/LICENSE" | sed "s|$out/||" > "$out/SHA256SUMS"
(cd "$out" && sha256sum -c SHA256SUMS)
printf 'Validated image: %s\nArtifacts: %s\n' "$image" "$out"
