#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
set -euo pipefail
tool_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source "$tool_dir/fountain-release-names.sh"
source "$tool_dir/fountain-toolchain.sh"
[[ $# == 5 ]]
out=$1 version=$2 commit=$3 image=$4 digest=$5
fountain_release_names "$version"
[[ $commit =~ ^[0-9a-f]{40}$ ]]
[[ $image =~ ^ghcr.io/[a-z0-9._/-]+$ && $digest =~ ^sha256:[0-9a-f]{64}$ ]]
(cd "$out" && sha256sum -c SHA256SUMS)
[[ $(gzip -dc "$out/$fountain_source_archive" | { git get-tar-commit-id; cat >/dev/null; }) == "$commit" ]]
config=$(cat "$out/image-id")
[[ $config =~ ^sha256:[0-9a-f]{64}$ ]]
[[ $(tar -xOf "$out/$fountain_prepared_archive" "$fountain_prepared_root/VERSION") == "$fountain_full_version" ]]
tar -tzf "$out/$fountain_prepared_archive" | grep '/vendor/modules.txt$' > /dev/null
toolchain=$(fountain_go_toolchain "$out/source")
metadata=$(cd "$out/source/sdk" && GOTOOLCHAIN="$toolchain" GOWORK=off GOFLAGS=-mod=readonly go run ./cmd/fountain-metadata)
protocol=$(jq -er '.extension_protocol | numbers | select(. == floor and . >= 0)' <<< "$metadata")
browser=$(jq -er '.browser_contract | numbers | select(. == floor and . >= 0)' <<< "$metadata")
jq -n --arg version "$version" --arg commit "$commit" --arg image "$image@$digest" \
  --argjson protocol "$protocol" --argjson browser "$browser" \
  --arg config "$config" --arg source "$fountain_source_archive" \
  --arg prepared "$fountain_prepared_archive" --arg platform "$fountain_platform" \
  --arg prepared_sha256 "$(sha256sum "$out/$fountain_prepared_archive" | cut -d' ' -f1)" \
  --arg source_sha256 "$(sha256sum "$out/$fountain_source_archive" | cut -d' ' -f1)" \
  '{schema:1, version:$version, source_commit:$commit, source_archive:$source,
    source_sha256:$source_sha256, prepared_source:$prepared, prepared_source_sha256:$prepared_sha256,
    image:$image, image_config_digest:$config,
    platform:$platform, extension_protocol:$protocol, browser_contract:$browser}' > "$out/release.json"
(cd "$out" && sha256sum release.json >> SHA256SUMS && sha256sum -c SHA256SUMS)
