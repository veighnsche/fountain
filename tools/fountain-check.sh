#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/fountain-toolchain.sh"
source "$(dirname "${BASH_SOURCE[0]}")/fountain-policy.sh"
cd "$(dirname "$0")/.."
if [[ $# != 1 || ${FOUNTAIN_CHECK_ISOLATED:-} != 1 ]]; then
  echo 'Usage: FOUNTAIN_CHECK_ISOLATED=1 bash tools/fountain-check.sh {source|unit|database|browser|packaging|release}' >&2
  echo 'Use a disposable committed checkout; these checks run source generators.' >&2
  exit 2
fi
case $1 in source|unit|database|browser|packaging|release|push-source|push-unit|push-native|push-race|push-database|push-browser|push-packaging) ;; *) exit 2 ;; esac

clean_source() {
  bash tools/fountain-check-clean.sh
}
clean_source
[[ $(uname -sm) == 'Linux x86_64' ]]
export GOTOOLCHAIN
GOTOOLCHAIN=$(fountain_go_toolchain "$PWD")
export GOWORK=off
[[ $(go version) == "go version $GOTOOLCHAIN linux/amd64" ]]
[[ $(node --version) == "v$(<.node-version)" ]]
[[ "npm@$(npm --version)" == $(node -p "require('./package.json').packageManager") ]]
[[ ${TMPDIR:-} == /home/* && -d $TMPDIR ]]
export CI=true TZ=UTC
case $1 in source|release|push-source) export GOFLAGS='' ;; *) export GOFLAGS=-mod=readonly ;; esac
mkdir -p .artifacts/fountain-ci
if [[ -d node_modules ]]; then touch node_modules; fi

format_independent_sources() {
  local -a files=()
  local session_path
  mapfile -d '' files < <(git ls-files -z 'sdk/*.go' 'sdk/**/*.go')
  while IFS= read -r session_path; do
    [[ -f $session_path ]]
    if [[ $session_path == *.go ]]; then files+=("$session_path"); fi
  done < tools/fountain-session-delta.txt
  if [[ ${#files[@]} != 0 ]]; then
    go run mvdan.cc/gofumpt@v0.9.2 -w "${files[@]}"
  fi
}

lint_workflows() {
  go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 "${fountain_workflows[@]}"
}

module_checks() {
  go mod verify
  for module in sdk third_party/go-chi-session; do
    (
      cd "$module"
      go mod verify
      go mod tidy
      if [[ $module == sdk ]]; then go vet ./...; else go vet .; fi
    )
  done
  # The upstream formatter omits independent modules. Format only our SDK and
  # the patched session files, preserving the unchanged vendored providers.
  format_independent_sources
}

case $1 in
  push-source)
    make fmt-check
    (cd sdk && go mod verify && go vet ./...)
    (cd third_party/go-chi-session && go mod verify && go vet .)
    format_independent_sources
    ;;
  push-unit)
    go test -count=1 -tags 'sqlite sqlite_unlock_notify' \
      ./build/fountain-ci ./modules/extensions ./modules/setting ./services/extensions \
      ./modules/session ./modules/queue ./routers/common ./cmd ./contrib/extensions/example
    (cd sdk && go test -count=1 ./...)
    ;;
  push-native)
    go test -count=1 -tags 'sqlite sqlite_unlock_notify' \
      ./services/nativeoperation ./services/pull ./services/automerge
    ;;
  push-race)
    go test -race -count=1 -tags 'sqlite sqlite_unlock_notify' \
      ./models/nativeoperation ./services/nativeoperation ./services/pull ./modules/queue code.forgejo.org/go-chi/session
    ;;
  push-database)
    : "${FORGEJO_TEST_PG_DSN:?Set a DSN for a disposable PostgreSQL database}"
    make generate-ini-sqlite
    env -u FORGEJO_TEST_PG_DSN go test -count=1 -tags 'sqlite sqlite_unlock_notify' ./models/nativeoperation
    for package in models/nativeoperation services/nativeoperation models/forgejo_migrations; do
      result=".artifacts/fountain-ci/pg-${package//\//-}.jsonl"
      if [[ $package == models/forgejo_migrations ]]; then
        # This self-contained SQL contract uses the real migration sources, but
        # not the historical migration suite's server/Git fixture TestMain.
        # File-list mode keeps the production package intact without requiring
        # an unrelated host executable or bypassing any PG assertion.
        mapfile -t migration_sources < <(git ls-files 'models/forgejo_migrations/*.go' | sed '/_test\.go$/d')
        go test -json -count=1 -tags 'sqlite sqlite_unlock_notify' -run 'OnPostgres$' \
          "${migration_sources[@]}" models/forgejo_migrations/v17b_add-native-operation_pg_test.go | tee "$result"
      else
        GITEA_ROOT="$PWD" GITEA_CONF=tests/sqlite.ini go test -json -count=1 \
          -tags 'sqlite sqlite_unlock_notify' -run 'OnPostgres$' "./$package" | tee "$result"
      fi
      node tools/fountain-test-results.mjs "$result"
    done
    ;;
  push-browser)
    make lint-frontend test-frontend
    npx --no-install tsc -p sdk/browser/tsconfig.json
    make frontend
    ;;
  push-packaging)
    for script in tools/fountain-*.sh; do bash -n "$script"; done
    hadolint --failure-threshold warning Dockerfile
    lint_workflows
    ;;
  source)
    make --what-if=routers/api/v1/api.go checks lint-backend-analysis lint-swagger
    module_checks
    ;;
  unit)
    make test-backend GOTESTFLAGS=-count=1
    (cd sdk && go test -count=1 ./...)
    go test -race -count=1 -tags 'sqlite sqlite_unlock_notify' \
      ./models/nativeoperation ./services/nativeoperation ./services/extensions ./modules/session ./modules/queue ./routers/common \
      ./services/pull ./services/automerge ./services/actions ./services/repository \
      code.forgejo.org/go-chi/session
    ;;
  database)
    : "${FORGEJO_TEST_PG_DSN:?Set a DSN for a disposable PostgreSQL database}"
    make backend TAGS='sqlite sqlite_unlock_notify'
    make test-sqlite-migration test-sqlite GOTESTFLAGS=-count=1 GOTESTCOMPILEDRUNSUFFIX=-test.timeout=120m
    # Each package mutates schema/fixtures. Never run these against one database
    # concurrently; pipefail preserves go test failures before JSON admission.
    for package in models/nativeoperation services/nativeoperation models/forgejo_migrations; do
      result=".artifacts/fountain-ci/pg-${package//\//-}.jsonl"
      GITEA_ROOT="$PWD" GITEA_CONF=tests/sqlite.ini go test -json -count=1 \
        -tags 'sqlite sqlite_unlock_notify' -run 'OnPostgres$' "./$package" | tee "$result"
      node tools/fountain-test-results.mjs "$result"
    done
    ;;
  browser)
    make lint-frontend test-frontend
    npx --no-install tsc -p sdk/browser/tsconfig.json
    make frontend backend TAGS='sqlite sqlite_unlock_notify'
    go build -trimpath -o .artifacts/fountain-ci/example-backend ./contrib/extensions/example
    export RUN_ALL=1 PLAYWRIGHT_PROJECT=chromium PLAYWRIGHT_FLAGS=chromium
    make 'test-e2e-sqlite#fountain' GOTESTFLAGS=-count=1
    ;;
  packaging)
    for script in tools/fountain-*.sh; do bash -n "$script"; done
    hadolint --failure-threshold warning Dockerfile
    lint_workflows
    ;;
  release)
    # The local release profile shares existing source commands. PostgreSQL and
    # real-browser qualification are the hosted workflow's separate lanes.
    make --what-if=routers/api/v1/api.go checks lint-frontend test-frontend
    npx --no-install tsc -p sdk/browser/tsconfig.json
    go test -count=1 -tags 'sqlite sqlite_unlock_notify' \
      ./modules/extensions ./modules/setting ./services/extensions ./cmd ./contrib/extensions/example
    (cd sdk && go test -count=1 ./...)
    ;;
esac
clean_source
