#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
set -euo pipefail
tool_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source "$tool_dir/fountain-release-names.sh"
source "$tool_dir/fountain-toolchain.sh"
[[ $# == 4 ]]
image=$1 version=$2 commit=$3 out=$4
fountain_release_names "$version"
engine=${CONTAINER_ENGINE:-docker}
[[ $image =~ ^sha256:[0-9a-f]{64}$ && $out == /home/* ]]
toolchain=$(fountain_go_toolchain "$out/source")
metadata=$(cd "$out/source/sdk" && GOTOOLCHAIN="$toolchain" GOWORK=off GOFLAGS=-mod=readonly go run ./cmd/fountain-metadata)
browser=$(jq -er '.browser_contract | numbers | select(. == floor and . >= 0)' <<< "$metadata")
[[ $("$engine" image inspect --format '{{.Os}}/{{.Architecture}}' "$image") == "$fountain_platform" ]]
[[ $("$engine" image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$image") == "$version" ]]
[[ $("$engine" image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image") == "$commit" ]]
"$engine" run --rm "$image" forgejo --version | tee "$out/version.txt"
grep -F "$fountain_full_version" "$out/version.txt"
grep -F 'bindata' "$out/version.txt"
toolchain_pattern=${toolchain//./\\.}
grep -E "(^|[[:space:],])$toolchain_pattern([[:space:],:]|$)" "$out/version.txt"
mkdir "$out/data"
# Required packages use the same pinned catalog and verified archive cache as
# production bootstrap. The fixture URL is deliberately unreachable: the native
# image check must prepare from these exact cached bytes and restart offline.
tar --format=ustar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
  -C "$out/example" -cf - extension.json backend assets | gzip -1n > "$out/example.tar.gz"
package_sha=$(sha256sum "$out/example.tar.gz" | cut -d ' ' -f 1)
package_size=$(stat -c %s "$out/example.tar.gz")
jq --arg sha "$package_sha" --argjson size "$package_size" --argjson browser "$browser" \
  '{version:1,packages:[{id:.id,version:.version,url:"https://fixture.invalid/example.tar.gz",size:$size,sha256:$sha,required:true,fountain_protocol:.protocol,browser_contract:$browser,capabilities:.capabilities}]}' \
  "$out/example/extension.json" > "$out/catalog.json"
# The host admits a catalog owned by root or its service UID, but the fixture
# carries the checkout owner's UID instead. Stamp it with the image's own
# default (root) identity through a disposable container: no host privilege is
# needed, the file stays group/other-readable, and local and CI runs agree.
"$engine" run --rm --mount "type=bind,src=$out,dst=/out" "$image" \
  sh -c 'cp /out/catalog.json /out/catalog.json.fixture && mv /out/catalog.json.fixture /out/catalog.json && chmod 644 /out/catalog.json'
# All fixture state is disposable. Use the image's s6 setup and generated app.ini.
container=$("$engine" run -d --mount "type=bind,src=$out/data,dst=/data" \
  --mount "type=bind,src=$out/example,dst=/example,ro=true" \
  --mount "type=bind,src=$out/catalog.json,dst=/fixture-catalog.json,ro=true" \
  -e FORGEJO__security__INSTALL_LOCK=true -e FORGEJO__server__DISABLE_SSH=true \
  -e FORGEJO__extensions__ENABLED=true -e FORGEJO__extensions__REQUIRED_IDS=example \
  -e FORGEJO__extensions__CATALOG_PATH=/fixture-catalog.json \
  "$image")
failed=1
cleanup() {
  "$engine" logs "$container" > "$out/container.log" 2>&1
  if [[ $failed != 0 ]]; then
    echo '--- container log (tail) ---' >&2
    tail -n 120 "$out/container.log" >&2 || true
  fi
  "$engine" rm -fv "$container" >/dev/null
}
trap cleanup EXIT
# The first boot writes configuration but cannot become ready without its required package.
for ((i=0; i<60; i++)); do
  if "$engine" exec "$container" test -s /data/gitea/conf/app.ini; then break; fi
  sleep 1
done
"$engine" stop "$container" >/dev/null
"$engine" run --rm --mount "type=bind,src=$out/data,dst=/data" \
  --mount "type=bind,src=$out/example.tar.gz,dst=/fixture-package.tar.gz,ro=true" \
  "$image" sh -ec 'cache=/data/gitea/extensions/.cache/packages; mkdir -p "$cache"; cp /fixture-package.tar.gz "$cache/$1.tar.gz"; chown -R git:git /data/gitea/extensions' sh "$package_sha"
"$engine" run --rm --mount "type=bind,src=$out/data,dst=/data" \
  --mount "type=bind,src=$out/example,dst=/example,ro=true" \
  "$image" su-exec git forgejo extensions install /example
"$engine" run --rm --mount "type=bind,src=$out/data,dst=/data" \
  "$image" su-exec git forgejo extensions enable example
"$engine" run --rm --mount "type=bind,src=$out/data,dst=/data" \
  "$image" su-exec git forgejo extensions status example | tee "$out/extension-status.txt"
grep -F $'example\tenabled\tmanifest-valid' "$out/extension-status.txt"
"$engine" start "$container" >/dev/null
ready() {
  for ((i=0; i<90; i++)); do
    if [[ $("$engine" inspect --format '{{.State.Running}}' "$container") != true ]]; then
      echo 'Container exited before readiness' >&2
      return 1
    fi
    if "$engine" exec "$container" curl -fsS http://localhost:3000/api/healthz > "$out/health.json"; then return; fi
    sleep 1
  done
  echo 'Container failed readiness' >&2
  return 1
}
verify() {
  ready
  "$engine" exec "$container" curl -fsS http://localhost:3000/api/v1/version | jq -e --arg v "$fountain_full_version" '.version == $v'
  for asset in js/index.js css/index.css licenses.txt; do
    "$engine" exec "$container" curl -fsS "http://localhost:3000/assets/$asset" > "$out/$(basename "$asset")"
    test -s "$out/$(basename "$asset")"
  done
  grep -F 'MIT' "$out/licenses.txt" >/dev/null
  # RequiredIDs makes readiness contingent on a real SDK handshake. The
  # signed-in guard now runs before the native session check, so an
  # unauthenticated client is redirected to login (303). A matched route
  # still proves contribution registration; an unregistered path would 404.
  [[ $("$engine" exec "$container" curl -s -o /dev/null -w '%{http_code}' http://localhost:3000/workspace) == 303 ]]
  "$engine" exec "$container" curl -s -D - -o /dev/null http://localhost:3000/workspace | grep -qi '^location: .*/user/login'
  "$engine" exec "$container" test -s /data/gitea/gitea.db
}
verify
"$engine" exec "$container" sh -c 'echo retained > /data/gitea/release-restart-marker'
"$engine" restart "$container" >/dev/null
verify
"$engine" exec "$container" grep -Fx retained /data/gitea/release-restart-marker
failed=0
echo 'Image startup, assets, extension handshake and persistent-state restart passed.'
