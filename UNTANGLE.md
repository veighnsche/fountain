# Untangle log

## Scope

- Phase: untangle-refactor (phase 3), explicitly requested by the user. Apply the reviewed fixes on `codex/fountain-untangle`; comments/test cleanup remain excluded.
- Requested boundary: all code Fountain added or modified; inherited, unchanged Forgejo implementation is context only and is not an independent refactoring target.
- Primary checkout: `/home/vince/Projects/forgejo-ext`, branch `main`, pinned commit `ad61c8ff398322a55b02b4e165da620c49a7d774`; compare against its merged upstream base, Forgejo `v15.0.10` (`17e1001d5e00e8ee3cbc1b6dd31be94ac38185f9`).
- Supplement: inspect Fountain-only differences in `/home/vince/Projects/fountain-lts-15.0.11`, branch `codex/fountain-lts-15.0.11`, pinned commit `adbcb8ad4de2f0ae70ca20e51e0cec3695d1207a`, based on Forgejo `v15.0.11` (`753f7edc775a97e66cd13d410404d5edc99b6b04`). Upstream merges and the four patch-equivalent upstream dependency backports are not Fountain design findings.
- Modified upstream files are mixed provenance: findings must concern Fountain's changes or the boundaries they introduce. Imports and comparisons may read upstream context without expanding the target scope.
- The copied `third_party/go-chi-session` dependency is audited only for local changes and integration, not unchanged third-party implementations.
- Optional additions: **neither**, explicitly selected by the user. No comment cleanup or test removal/repair is authorized. Tests may be read and run as evidence; structural test coupling may be recorded without planning cleanup.
- Initial state: both source checkouts were clean; no pre-existing `UNTANGLE.md` was found in either checkout.

## Baseline

Pinned source: `ad61c8ff398322a55b02b4e165da620c49a7d774` on `main`; the LTS supplement remains `adbcb8ad4de2f0ae70ca20e51e0cec3695d1207a`. Both checkouts started clean. All counts below include named subtests when the runner reports them separately.

| Check | Exact result |
| --- | --- |
| SDK, pinned Go 1.26.10, `go test -json -count=1 ./...` from `sdk/` | **125 pass, 0 fail, 0 skip**, package succeeds. |
| Selected host/package baseline, pinned Go 1.26.10 and SQLite tags, listed below | **1,213 pass, 3 fail, 12 skip**; 12 selected packages succeed, `services/nativeoperation` fails tests, and `cmd` cannot build. |
| Patched session dependency, `go test -json -count=1 code.forgejo.org/go-chi/session` | **47 pass, 0 fail, 0 skip**. |
| Frontend, Node v24.14.1/npm 11.11.0, `node node_modules/vitest/vitest.mjs run --reporter=json --outputFile=.artifacts/untangle-audit/baseline/frontend.json` | **483 pass, 0 fail, 0 pending**, exit 0. Exact locked dependencies installed with `npm ci --ignore-scripts --no-audit --no-fund`. |
| Browser TypeScript contract, `node node_modules/typescript/lib/tsc.js -p sdk/browser/tsconfig.json` | Exit 0, no diagnostics. |
| `bash -n` for every `tools/fountain-*.sh` | All **10** scripts pass syntax checking. |
| Host build, `go build -tags 'sqlite sqlite_unlock_notify' -o /tmp/fountain-audit-gitea .` | Exit 1: `routers/api/v1/repo/desktop_source.go:182:1: syntax error: unexpected EOF, expected }`. The malformed file is already in the pinned commit, not an audit modification. |

The three native-operation test failures are baseline facts, not changes made or repaired by this phase:

- `TestSubmitReportsUnimplementedKind`: expected `operations_unavailable`, received `invalid operation intent`.
- `TestMapExactReviewRefusal`: expected `native_refused`, received `pr_mismatch` for the approve/reject refusal case.
- `TestSnapshotReadChecksConveyActionsPostedRow`: expected an empty system-creator identity, received `-2`.

Twelve skipped test/subtest events comprise two optional Redis-server cases and ten PostgreSQL-specific cases with no disposable PostgreSQL fixture configured. Exact names and per-package outcomes are retained in `.artifacts/untangle-audit/baseline/summary.json`. This is not evidence that those unexecuted contracts pass.

The exact host selection was:

```sh
go test -json -count=1 -timeout=5m -tags 'sqlite sqlite_unlock_notify' \
  ./build/fountain-ci ./modules/extensions ./modules/setting ./services/extensions \
  ./modules/session ./modules/queue ./routers/common ./cmd ./contrib/extensions/example \
  ./models/nativeoperation ./services/nativeoperation ./services/pull ./services/automerge \
  ./routers/web/extensions
```

For Go commands the actual executable was `/home/vince/.cache/go-runner/mod/golang.org/toolchain@v0.0.1-go1.26.10.linux-amd64/bin/go`, with `GOTOOLCHAIN=local`, `GOMODCACHE=/home/vince/.cache/go-runner/mod`, `GOCACHE=$REPO/.artifacts/untangle-audit/runtime/cache`, `GOTMPDIR=/tmp/fa`, `TMPDIR=/tmp/fa`, `GOPROXY=off` and `GOFLAGS=-mod=readonly`. The system `go` wrapper overrides mutable cache locations, so directly selecting the already cached pinned executable kept audit writes in allowed scratch locations. SDK and host socket tests ran with local Unix-socket access available.

The initial sandbox SDK attempt recorded 101 passes and 18 failures because Unix `setsockopt` was denied; the socket-capable rerun above supersedes it as the functional baseline. An initial offline frontend dependency installation stopped at uncached `ws-8.20.0`; installing the same lockfile with network access succeeded. Both initial environmental attempts and final outputs are retained separately.

Documented complete qualification commands are `FOUNTAIN_CHECK_ISOLATED=1 make fountain-check-{source,unit,database,browser,packaging}` in a disposable committed checkout with the pinned toolchain and a disposable PostgreSQL DSN. Generator/formatter/source gates were not run against this retained source checkout because they write or stage source. Full PostgreSQL, race, complete inherited Go regression, real-browser E2E, generated-asset production build, image/release and publication checks were not run. Existing compile failure prevents a current-commit CLI/API/screenshot behavior baseline; this audit makes no claim that the application builds or that every documented CI lane passes.

Raw evidence lives in `.artifacts/untangle-audit/baseline/`: SDK/host/session JSONL, their stderr, frontend JSON, TypeScript output, build diagnostics and npm installation output. The baseline is preserved; later phases should write their comparison results to a different directory.

## Map

The audited delta contains **1026 paths** (1025 present, one removed), after filtering the copied session module against the cached pristine v1.1.0 ZIP. The filter retains its **eight** local-delta paths, including the modified `file.go`, `file_test.go`, `memory.go`, local lifecycle tests, module manifests and provenance note; unchanged imported providers are context only. The unfiltered fork delta still has 555 non-vendor additions, 27 imported/session paths, 462 modified upstream paths and one deletion. Comparison uses file content and Git ancestry, not copyright text.

| Area | Responsibility and important links |
| --- | --- |
| SDK and browser contract | Manifest/catalog validation, protocol DTOs, private HTTP/Unix transports, typed native/human/background clients and browser owner lifecycle; links to callback dispatch, startup capability selection and mount datasets. |
| Extension package/runtime | Filesystem store, receipts, staging/recovery, source snapshots, desired intents, controller admission and processes; links to administrator/CLI adapters and SDK lifecycle semantics. |
| Native operation domain | Durable global reservation, operation journal, SQL/Git primary effects, exactly attributed completion and stopped-domain recovery; links to every participating ordinary writer and private Git hooks. |
| Ordinary Forgejo integration | Added ownership/proof propagation in model, service, router and command paths; repeated narrow persisted scopes and resource-label grammars couple writers to admission/recovery. Unchanged native business logic is outside the target. |
| Desktop and human callbacks | Separate OAuth/grant consent, current actor/repository admission, preparation facts, immutable human intents and receipts; links to SDK callbacks, native-operation effects and issue-only scopes. |
| Browser host/native features | Retained page/panel owners, session revalidation, history and DOM transplantation; per-feature init/retire envelopes, widgets, Vue requests and template JSON/data attributes must agree on lifetime/identity. |
| Session adapter/local dependency patch | File generation/revision locking, memory lifetime detachment, virtual-store release and WebSocket response/hijack integration; only verified local dependency deltas are targets. |
| Qualification and release | Isolated source gates, exact-commit tag/branch admission, per-lane tests, native image fixture and release provenance; conventions repeat across shell and workflow YAML. |

Independent area inventory/review covered 113 native production Go files plus tests/migrations, 140 extension-area production files, 269 existing writer production files, and all 185 initially inventoried browser/template/E2E paths. These area counts overlap at boundaries and are not additive. The parent checked the remaining integration/configuration hunks, local session delta and all Fountain shell/workflow tooling. Coverage is complete at inventory/declaration/diff-pattern level; repeated writer guards and callback/test families were assessed structurally, with direct body reads concentrated on lifecycle, authorization, persistence and cross-area protocols. The huge callback integration test was searched by contract/callsite rather than read line by line. No claim is made to formal verification or an exhaustive proof of absence of defects.

The static Go graph spans 3,702 tracked Go files as context and detects **no production import cycles**; this does not negate the syntax-error baseline. Nested SDK/session module imports are resolved to their local directories. Including test importers, `services/nativeoperation` has 37 importing packages, `models/nativeoperation` 26 and `modules/nativeoperation` 23. Layering pressure appears in JSON scope projections and English-error matching used to avoid import cycles, even though the static graph itself is acyclic.

Churn uses the **281 non-merge fork commits** in the last year. Production leaders: `web_src/js/features/extensions.js` 31 commits; `routers/web/extensions/extensions.go` 22; `services/nativeoperation/recovery.go` 16; `models/nativeoperation/operation.go` 15; `services/extensions/manager.go` and native `service.go` 14; native `guard.go` and SDK `background.go` 13. Tests and documentation are retained in the full churn table, without becoming optional cleanup targets.

Co-change excludes five bulk commits touching more than 80 scoped paths so mechanical sweeps do not dominate pair evidence. Leading cross-area pairs: Go contribution routing/browser host (13 commits together), model operation vocabulary/native service (10), model operation vocabulary/submission dispatch (9), SDK snapshots/native snapshot reader (8), callback dispatch/SDK transport (7). These are search leads; every listed problem also has a concrete source mechanism.

Highest leverage is at the shared operation/capability vocabulary, persisted ownership witness, credential/snapshot policy, retained-owner state, and asynchronous native-feature retirement. Findings were discovered by area and consolidated across boundaries; the redesign below assigns each preserved finding to exactly one structural fix or an explicit leave-as-is decision.

Retained regeneration entry point: `python3 .artifacts/untangle-audit/rebuild.py`. It asserts the pinned HEAD and requires the recorded cached pinned Go toolchain, installed locked TypeScript, and pristine session ZIP at `/home/vince/go/pkg/mod/cache/download/code.forgejo.org/go-chi/session/@v/v1.1.0.zip`; it never changes application files. Supporting helpers are `go-map.go` (Go AST stripping/import extraction) and `js-map.mjs` (TypeScript AST body stripping and static/literal-dynamic imports).

Generated evidence: `scope.csv`, `spec.json`, `go-skeleton.txt`, `js-skeleton.txt`, `go-imports.json`, `js-imports.json`, `dependencies.csv`, `fanin.csv`, `churn.csv`, `cochange.csv`, `special-case-candidates.csv`, `session-local-delta.csv`, `summary.json`, `go-parse-errors.json`, and `finding-location-check.json` under `.artifacts/untangle-audit/`. Go parse diagnostics contain the unrelated upstream empty test fixture and the real desktop source syntax error. Dynamic DOM/global/JSON conventions cannot all appear as import edges and were checked directly. Per-area notes and preserved blind/fresh review outputs are retained beside the maps.

## Problems

All **121** audit entries are preserved verbatim below, each with a stable P001–P121 identifier. Their full text now lives exactly once under its fix or under Leave as is. Original counts: hidden **71**, needless **9**, surplus **13**, special **28**. The redesign assigns **96** findings to **10** fixes and leaves **25** as is, including intentional requirements, compatibility APIs, low-value cleanup and one corrected upstream-provenance finding (P087).

These are structural dispositions, not permission to delete every exceptional branch. Optional comment cleanup and test removal/repair remain excluded. Existing exported APIs, operation IDs, intent hashes, receipts, consent meanings and fail-closed recovery remain constraints unless an individual plan explicitly identifies an audited behavior correction.

## Fixes

**Review status:** The user reviewed the scope question and reported **no additional constraints**. This handoff preserves the compatibility assumptions below. The user subsequently requested untangle-refactor; implementation is now authorized on the dedicated review branch.

The first three fixes establish stable contracts, ownership and mount facts. The independent frozen-schema fix is separated because historical migrations must not evolve with runtime consent. Tooling and feature lifetimes follow; retained-owner coordination builds on those lifetimes. Shared native facts precede package/controller admission and effect execution. Dependencies below are prerequisites, not a requirement to merge unrelated changes together. Existing adapters allow a fix to land without removing public APIs or completing every other area first.

| Fix | One-line result | Findings | Depends on |
| --- | --- | ---: | --- |
| [F01](#f01) | Own the stable contract vocabulary and transport boundaries. | 10 | None |
| [F02](#f02) | Make durable ownership and ordinary leases explicit. | 12 | F01 |
| [F03](#f03) | Carry one admitted route and mount identity across the host. | 10 | F01 |
| [F04](#f04) | Freeze historical desktop-grant migration schemas. | 1 | None |
| [F05](#f05) | Use factual qualification and release inputs. | 6 | F01 |
| [F06](#f06) | Give native features and widgets an explicit lifetime. | 19 | F03 |
| [F07](#f07) | Coordinate retained owners, session checks and navigation intent. | 8 | F03, F06 |
| [F08](#f08) | Derive native admission and observations from authoritative facts. | 15 | F01, F02 |
| [F09](#f09) | Unify package provenance, lifecycle admission and runtime liveness. | 8 | F01, F08 |
| [F10](#f10) | Compose native operation execution and evidence-based recovery. | 7 | F01, F02, F08 |

### Requirements revealed by special cases

All **28** special entries are requirements to preserve or make explicit. This table references their IDs; their original audit text appears only at the assigned destination below.

| Finding | Requirement | Disposition |
| --- | --- | --- |
| P009 | Scope refinement must distinguish irrelevant ownership from storage/proof failure and stop bound effects on error. | F02 |
| P015 | Every valid later cursor must make bounded progress, with truthful counts and incomplete indicators. | F08 |
| P035 | Failed activation needs exact, expiring, single-use cleanup while desired/pending activation persists and normal admission stays closed. | F09 |
| P036 | Configured-appliance legacy intake must not bypass its controller; unrelated legacy intake retains its current policy. | F08 |
| P037 | Existing comment intent hash bytes include historical float normalization and cannot be silently recoded. | Leave as is; F01 preserves |
| P038 | Only proven controller-owned rows may enter its snapshot; unknown legacy rows are preserved and fenced, not broadcast. | F09 |
| P039 | Private listener and control readiness represent one runtime liveness; unexpected listener failure must be observable. | F09 |
| P054 | A delayed webhook redirect belongs to its originating current page; an issued mutation is never retried. | F06 |
| P059 | Shelter retains four oldest-evictable pages with the existing persist exemption. | Leave as is |
| P075 | Participating host Git writes need explicit receive/internal origin and proof, with the current admitted execution set preserved. | F02 |
| P076 | Rename proof covers exactly its bounded delete/create ref batch, including wiki targets. | F02 |
| P077 | Contention retry is pre-effect admission only: one second with 25–250 ms backoff; queues handle longer delays. | Leave as is |
| P078 | Legacy anchorless identity must use full available context or remain ambiguous/incomplete, never infer identity from a page prefix. | F08 |
| P079 | Ambiguous or over-10,000-row legacy location-only replies remain refused. | F08 |
| P080 | Conditional exact review cannot consume a draft; a current human review may consume its own native draft. | Leave as is; F08/F10 preserve |
| P081 | Retained blank-proof enrollment is repository-read-only and never issue, write or controller consent. | Leave as is; F08 preserves |
| P082 | Absent header stays legacy, exact protocol 2 maps its existing scopes, and unknown versions refuse. | Leave as is; F08 preserves |
| P091 | Outbound mirror push releases the global lease to avoid loopback deadlock, then reacquires for local metadata. | Leave as is |
| P092 | Unmerged PR snapshots emit zero merge time despite inherited ORM auto-update behavior. | Leave as is |
| P096 | Production direct routing follows admitted slot/owner facts while the exported legacy workspace helper stays compatible. | F03 |
| P097 | Repository route composition retains the current supported feed slot and its code-or-issue/contribution permission guards. | F03 |
| P098 | A linked owner may share only the exact canonical same-extension page-owner API path. | F03 |
| P108 | A GET form may refresh the same query and exempt itself from dirty checks without changing other navigation sources. | F07 |
| P109 | A current transplant times out after 15 seconds and coherently falls back to a full document. | Leave as is; F07 preserves |
| P110 | History failures need explicit coherent fallback/restoration rather than mismatched URL, content or traversal bookkeeping. | F07 |
| P111 | Terminal cleanup attempts every owner/socket; suspension keeps streams alive and is not disposal. | Leave as is; F07 preserves |
| P113 | Visible admin observation stops on authorization/not-found responses, tolerates transient failure and otherwise observes every two seconds. | Leave as is |
| P121 | The pinned session WebSocket bridge preserves case-insensitive Upgrade, concrete hijacking and session release. | Leave as is |

### Boundary and verification rules

- Scope stays at the pinned Fountain delta on main plus the verified Fountain-only LTS supplement. Modified upstream files are mixed provenance; do not extract unchanged Forgejo business workflows. Phase 3 changes the reviewed Fountain implementation and adds targeted regressions. Independent comment/test cleanup and changes to release policy, remotes or published artifacts remain outside scope.
- Prefer a small shared primitive or finite data contract to a framework. In particular, callback transport, journal execution, enrollment, consent policy and lifecycle admission are distinct compositions. Do not create one universal operation/plugin registry, reflection schema system or workflow generator.
- Keep the SDK a standalone nested module. Neutral modules may own codecs/interfaces; models own persistent authority; services compose effects; routers and browser adapters present them. Retain exported compatibility wrappers where downstream absence is unproven.
- Preserve original wire/hash/witness bytes. New decoders/refiners must retain unknown scope fields and old encodings; no hidden intent/receipt/consent version upgrade. Preparation is observation; final admission must remain current under the required transaction/lease. Unknown state remains fenced, with no timeout unlock or effect replay.
- The recorded host syntax error and three native-operation test failures predate this plan. A phase-3 host comparison must first account for the existing build blocker explicitly; this document does not silently repair it or redefine failing expectations. The recorded audit baseline remains intact, including skipped/unrun PostgreSQL, race, E2E and release checks.
- Later implementation should run the relevant existing checks and add only meaningful regressions for changed behavior or concrete gaps. Neither optional test cleanup/repair nor a comment sweep is authorized. Do not run source-writing qualification gates in the retained audit checkout; use the documented disposable qualification checkout when that phase is requested.

<a id="f01"></a>

### F01 — Own the stable contract vocabulary and transport boundaries (10 findings)

Give wire syntax one SDK-owned, host-independent home: Git ref/OID syntax, operation-ID grammar and human namespaces, enrollment kind names, and strict JSON object mechanics. Preserve caller-specific normalization and acceptance through named variants, including OID casing and SHA-1/SHA-256 support; sharing mechanics must not broaden every caller to one acceptance profile. Use small stdlib-only SDK leaf packages where host models need these definitions; the nested SDK must never import root models/services. Keep exported SDK types, helpers and error sentinels through aliases or thin adapters. Enrollment-only kinds such as `execution.launch` remain distinct from journal-supported execution kinds.

Describe callback wire variants with finite data (selector, result, capability and transport need); derive envelope/exclusivity checks and callback-socket provisioning from that description. The root host composes actual handlers. This is not a registry for journal execution, consent policy or recovery. Keep the public request/response union shapes, and make exactly-one-result validation inspect the actual typed envelope. Include every accepted callback capability and reserve all human ID prefixes, including manual merge and pull creation.

Separate Unix transport status from operation-family error interpretation. The transport owns peer identity, HTTP limits, strict decoding and typed transport outcomes; each family maps those outcomes to its existing public errors with `errors.Is` compatibility. A shared human-result envelope validator accepts the family's canonical digest rather than choosing a codec; add the missing comment intent comparison using its historical digest. Strict JSON mechanics reject duplicate decoded keys, trailing objects and invalid required/null fields, while domain validators retain their own semantic rules. Do not pass hashed intents through a new serializer.

Carry the bootstrap service's expected host UID, including UID 0, into every credential-bearing dial/reconnect. Check the peer before sending the credential. Extension-local transport remains a separate explicit mode.

**Structural checks:** Syntax and envelope edits now change their owning contract definitions and adapters together. Family refusal vocabularies, canonical intent encoders and host handler composition remain independent. Host models and services depend on stable SDK leaf contracts; those leaves depend only on the standard library, so no SDK-to-host or model-to-service edge is introduced.

**Done when:** One definition supplies each shared grammar/namespace; callback-only packages receive their required socket; family-specific error identities and existing wire/hash bytes remain stable; malformed lifecycle nulls are refused; service-client credentials never cross an unverified peer connection.

**Depends on:** None.

**Implementation validation:** Exercise existing SDK/callback/catalog tests and focused cases for every accepted callback capability, human-prefix collisions, duplicate/unknown/null/trailing JSON, preserved comment hashes, family sentinel mappings and UID checks on reconnect. Check dependency edges and exported signatures.

**Audit entries addressed:**

- **P005** [hidden] `services/nativeoperation/service.go:163`/`:174`, `services/nativeoperation/publish_exact.go:84`/`:97` and `services/nativeoperation/family_actions.go:227`, `sdk/publish.go:54`, `sdk/prcreate.go:34`, `sdk/review.go:104` and `sdk/merge.go:18`, `models/issues/snapshot.go:424`, `models/git/commit_status_snapshot.go:43` and `services/pull/merge_exact.go:186` independently implement the same branch-ref/OID shapes; accepted Git syntax and object-format support require matching edits to the general, publish and status validators.
- **P011** [hidden] `services/nativeoperation/service.go:193` manually excludes ten SDK human operation-ID prefixes, while `sdk/native_human_manual_merge.go:73` and `sdk/native_human_pull_create.go:81` define two additional human prefixes absent from that exclusion; introducing a human callback namespace requires an unrelated edit to background validation or a background operation can occupy the same installation/ID.
- **P021** [hidden] `sdk/native.go:50`, `sdk/native.go:125`, `sdk/native.go:748` and `routers/web/extensions/callback.go:245` independently enumerate the same callback variants and exclusive payload fields; adding a callback family requires matching changes to the request/response structs, host forbidden-selector checks, host dispatch/capability switches and SDK exactly-one-result counter.
- **P022** [needless] `sdk/native.go:352` makes the shared Unix HTTP transport select every operation family's individual query/admission/conflict/unavailable errors through hundreds of operation-specific status branches, so one collaboration API's error vocabulary changes this transport used by all other APIs.
- **P023** [hidden] `sdk/native_human_comment.go:172`, `sdk/native_human_lock.go:172`, `sdk/native_human_edit.go:172`, `sdk/native_human_state.go:166`, `sdk/native_human_review.go:227`, `sdk/native_human_inline.go:193`, `sdk/native_human_thread.go:185` and the other human-result validators separately copy installation, effect/cancellation/completion-state and tombstone rules; changing the durable operation envelope requires coordinated edits across every family, and comment validation already differs by omitting the canonical submitted-intent digest comparison present in the other families.
- **P024** [hidden] `sdk/native_human_comment.go:97`, `sdk/native_human_lock.go:101`, `sdk/native_human_edit.go:99` and the other human-operation ID helpers copy actor/repository prefix binding, SHA-256 command suffix and hex validation under separate operation-specific functions, so the common identity format and validation rules can drift independently across operation families; the common 128-character operation-ID grammar is also independently copied in `services/nativeoperation/service.go:150`, `routers/web/extensions/background.go:496` and `sdk/background.go:378`.
- **P025** [hidden] `models/extensionauth/binding.go:25` and `sdk/background.go:23` separately define the background operation-kind strings and supported-kind sets; adding or renaming a kind requires matching edits in model enrollment/validation and the SDK although both describe the same wire vocabulary.
- **P026** [hidden] `services/extensions/manager.go:693` manually lists capabilities that need a native callback socket but omits `CapabilityHumanStates`, `CapabilityHumanReviews`, `CapabilityHumanMerges`, `CapabilityHumanLocks`, `CapabilityHumanIssueCreate` and `CapabilityHumanPullCreate`, all accepted at `sdk/catalog.go:116`; a package declaring only one of those accepted write capabilities starts without the callback transport that its SDK methods require.
- **P032** [hidden] `sdk/catalog.go:300` and `modules/extensions/intent.go:184` carry separate recursive duplicate-key JSON walkers for the same strict-decoding rule; later envelope decoders also maintain their own required-field/null handling, already allowing lifecycle `admission_ready: null`/`desired_enabled: null` through `sdk/lifecycle.go:380` while intent explicitly rejects `desired_enabled: null` at `modules/extensions/intent.go:171`.
- **P040** [hidden] `sdk/background.go:265` pins the service host UID during bootstrap but discards that UID when constructing `backgroundClient` at `sdk/background.go:327`; subsequent credential-bearing requests dial the socket without peer validation at `sdk/background.go:341`, so continued server-identity enforcement depends on a bootstrap-time check that the client transport no longer represents.

<a id="f02"></a>

### F02 — Make durable ownership and ordinary leases explicit (12 findings)

Put persisted scope, scoped-ref target, owner-family and legacy resource-label codecs in the neutral `modules/nativeoperation` layer, retaining service aliases. New writers pass explicit authority IDs and code/wiki targets rather than reparsing display labels. Preserve existing slash, `wiki/` and `wiki:` encodings in the compatibility codec. Ambiguous old records stay fenced.

A lossless scope document supports bounded typed refinement while retaining original bytes and unknown fields. The SQL model remains responsible for owner/generation checks and exact-byte compare-and-swap; do not normalize stored witnesses. Pull ref updates no longer inspect `cond:` or patch arbitrary JSON keys themselves. A checked refinement result distinguishes NotApplicable from Updated and from error; participating production paths stop before effects on storage/proof failures. If a public void helper must remain, keep it as a reporting compatibility adapter rather than discard errors in new paths.

Share the ordinary-lease sequence through a small neutral runner with a claim/release interface implemented by the model. It owns cancellation-before-claim, capability-file lifetime, nested reuse, execution context, detached release and error propagation. Keep the service adapter and database rules in their layers; do not move process/file management into the database model. Define a typed contention sentinel below services and preserve service aliases, so HTTP classification uses `errors.Is` rather than English text.

Use an explicit Git execution-origin/proof builder for currently participating receive/internal paths instead of incidental `SSH_ORIGINAL_COMMAND` knowledge. Preserve the absent-marker legacy receive adapter and current internal exclusions; unknown explicit origins refuse. Move the duplicated archive ownership envelope to one repository-service primitive, leaving transport-specific responses in the routers.

**Implementation-driven rename correction:** Scratch Git transcripts showed that native `branch -m` does not produce one atomic two-ref prepared callback: it reports source deletion and symbolic-HEAD updates, while destination creation is internal. Replacing native rename with a two-ref update would discard reflog/config/worktree semantics. Retain native `branch -m`, install a typed exact rename stage on the held scope before launch, admit only its bounded proven native transcript, then verify the complete ref and affected-HEAD result after the existing model transaction completes. Read that proven stage before repository-table lookup in the hook, since the native child runs inside the parent’s existing SQL transaction. Wiki rename may stage only its same-repository bounded parent settings operation or direct rename owner. An entered failure or unverified result keeps the stage and ownership fence for stopped-domain reconciliation; it never releases based on ref tips alone.

The neutral witness adds optional explicit code/wiki target and rename-stage fields for new participating operations. Historical absent fields are read through the legacy codecs, and refinement preserves original root/per-ref unknown bytes. New authority writers supply explicit operation/entity IDs. The common lease runner retires its capability but does not release a typed indeterminate effect or an active rename stage; model release also checks the exact witness so a swallowed nested error cannot discard the fence.

**Structural checks:** The witness format and all refiners move together; Git execution origin and archive transport presentation remain separate adapters. Database ownership checks stay independent of filesystem lifetime management. Modules depend on neutral codecs/interfaces, models implement persistence, and services compose policy without a module importing services.

**Done when:** All listed writers use typed scope/target operations and one ordinary-lease sequence; refinements preserve unknown witness fields and fail visibly before effects; receive and exact two-ref rename proof survives to admission; archive transports agree; contention classification survives message changes.

**Depends on:** F01

**Implementation validation:** Use existing ownership/admission/recovery tests plus focused round trips with unknown legacy fields, exact-byte CAS conflicts, cancellation/release failure, nested ownership, both rename refs/wiki targets and injected refinement storage failures. Assert no broader Git admission or lease duration changes.

**Audit entries addressed:**

- **P009** [special] `services/nativeoperation/callback_repository.go:56` and `:83` silently return on reservation/scope failures and discard `UpdateScopeRefs` errors before hook classification; an infrastructure failure is indistinguishable from an intentionally irrelevant owner, coupling later scope-mismatch refusals to an unreported earlier persistence failure.
- **P069** [hidden] `modules/repository/authority_ownership.go:20` and `services/nativeoperation/guard.go:43` independently define the authority reservation JSON shape; collaborator admission and recovery depend on matching field names despite the module having no dependency on the owning Scope type.
- **P070** [hidden] `modules/repository/authority_ownership.go:43` and `services/nativeownership/ordinary.go:22` independently implement capability creation, ordinary claim, execution context, cancellation-independent release and file cleanup; a capability-lifetime or release-semantics change has to update both protocols.
- **P071** [hidden] `services/issue/own_collab.go:23`, `services/pull/own_collab.go:25`, `services/pull/own_refwrite.go:23` and `services/nativeoperation/guard.go:43` maintain separate JSON projections of the same persisted reservation scope, while `services/pull/own_refwrite.go:34` repeats the service's `ref-write` family value; scope/family contract changes require synchronized updates across these ownership helpers.
- **P073** [hidden] `services/wiki/wiki.go:112`, `services/wiki/wiki.go:199`, `services/nativeoperation/admission.go:198` and `services/nativeoperation/recovery_repository.go:140` encode and rediscover wiki identity through a `wiki/` prefix inside a resource label rather than the reservation fields; renaming that label changes both admission and offline attribution.
- **P074** [hidden] `services/user/email.go:32`, `services/user/user.go:79` and the authority wrappers throughout routes/commands format slash-separated resource strings whose numeric segments become persisted operation/IDs in `services/nativeoperation/authority.go:54`; changing a writer label can silently change the recovery protocol.
- **P075** [special] `modules/repository/hooks.go:84` skips the reference-transaction delegate whenever `SSH_ORIGINAL_COMMAND` is absent, so local ref-writing paths participate only if their environment builder follows this receive-origin convention.
- **P076** [special] `services/repository/branch.go:325` and `services/wiki/wiki.go:102` leave branch-rename children unbound because the single-ref gate does not express their two-ref operation; a rename changes the ownership protocol used by otherwise similar ref writers.
- **P084** [hidden] `services/mirror/mirror_pull.go:566`, `services/nativeoperation/admission.go:125` and `services/nativeoperation/recovery_repository.go:432` separately encode/decode the `wiki:` namespace inside ScopedRef.Ref; changing the mirror target representation requires coordinated changes in fetch recording, hook admission and recovery.
- **P088** [hidden] `routers/api/v1/repo/repo.go:1187` and `routers/web/repo/setting/setting.go:1187` duplicate the archive-state ownership family, resource label, scope and model call; changing archive ownership semantics requires synchronized transport-local helpers.
- **P089** [hidden] `services/context/policy_error.go:27` identifies service-level reservation contention by searching the model sentinel's English message because the context package cannot import the service; changing error wording alters HTTP status classification.
- **P114** [needless] `services/pull/own_refwrite.go:87` and `:99`–`:115` detect conditional ownership by the private `cond:` string and update the raw persisted JSON key `new_oid`; owner construction and typed reservation fields live in `services/nativeoperation/guard.go:43,117`, so changes to reservation representation require the pull service's scope-preserving updater to track internals independently of its ref-write operation. This is distinct from merely repeating narrow scope structs: the helper must preserve arbitrary enclosing fields while mutating an untyped record and distinguish an owner class by encoding.

<a id="f03"></a>

### F03 — Carry one admitted route and mount identity across the host (10 findings)

Build one admitted mount descriptor on the Go host, render it through one shared template partial, and parse it once through browser helpers. Its identity includes extension, contribution, owner, entry/API/asset URLs, app prefix, contract version and admission generation. Repository display fields do not substitute for authority identity. Compare the same complete identity for retained pages and panels; shared ownership always uses the tuple (extension ID, owner ID).

Bind each owner's declared direct patterns to that owner instead of broadcasting a global pattern union. Derive production direct-path classification from admitted slot/route facts, preserving the exported `workspaceDirectPath` helper's old output for compatibility. Compose routes over the existing manifest route functions with an explicit allowlist of supported slots. Repository contributions still support the current `feed` slot and its code-or-issue visibility and contribution authorization; this plan does not open arbitrary repository routes.

Replace the linked-panel substring exemption with exact structural validation against the canonical same-extension page-owner API path. Share a small plain-data policy artifact for auth-route segments, credential keys/suffix rules and opaque-byte limits across embedded Go and browser imports. Keep independent validation at both trust boundaries and agree on UTF-8 byte measurement, including app prefixes. No schema generator or general route engine is needed. Accept valid version-1 mount data without requiring newly invented SDK attributes. Remove only the confirmed private uncalled Go matcher; preserve public route adapters.

**Structural checks:** Host rendering, browser identity and owner-pattern association change with one mount contract. Product route selection remains separate from generic URL validation. Go/browser consumers depend on stable finite policy data and SDK route facts; the SDK does not import a host router, browser module or template.

**Done when:** A changed entry/asset/prefix/owner fact remounts both page and panel correctly; two extensions with the same owner label cannot suppress each other; one owner's routes do not affect another; Go and browser agree on byte limits/confinement; linked owners use exact canonical identity without broad URL exceptions.

**Depends on:** F01

**Implementation validation:** Run routing/template/browser contract tests for app subpaths, non-ASCII 255-byte boundaries, cross-extension owner collisions, descriptor changes, linked panels and disjoint owner routes. Keep repository feed permission cases and exported route-helper expectations intact.

**Audit entries addressed:**

- **P043** [surplus] `routers/web/extensions/extensions.go:225` defines `matchDirectPattern` without any callers, while actual subordinate-route dispatch uses `Page.MatchSubroute`; this unused second route matcher has no runtime responsibility.
- **P047** [hidden] `web_src/js/features/extensions.js:122` omits entry URL, asset base, app subpath and owner ID from retained-page identity, whereas `web_src/js/features/extensions.js:134` includes them for panels; changing a page contribution declaration can therefore silently preserve the previous implementation at `web_src/js/features/extensions.js:359` while the parallel panel change correctly retires and remounts it.
- **P049** [hidden] `web_src/js/features/extensions.js:5`, `web_src/js/features/extensions.js:11` and `routers/web/extensions/extensions.go:475` duplicate the auth-route/credential-key navigation exclusions; `web_src/js/features/extensions.js:40` and `sdk/manifest.go:303` separately implement declared-route matching, with the nominal 255-character limit measured as UTF-16 units in the browser and UTF-8 bytes in Go.
- **P050** [hidden] `templates/extensions/content.tmpl:5` suppresses a page mount when any panel has the same owner ID, while `web_src/js/features/extensions.js:127` scopes shared-owner identity by extension plus owner; two extensions reusing an owner ID therefore require unrelated declarations to coordinate to avoid hiding a page.
- **P064** [hidden] `routers/web/extensions/extensions.go:434` publishes a single union of every page's direct patterns, and `web_src/js/features/extensions.js:402` uses that union for every shared owner; adding one owner's route changes the page/panel presentation sent to unrelated owners because route declarations carry no association to their owner in the browser.
- **P093** [hidden] `web_src/js/features/extensions.js:84` and `web_src/js/features/extension-mount.js:19` duplicate `validAppSubURL` exactly; deployment-prefix validation changes must be made in both host navigation and contribution admission or the same document can be accepted by one and rejected by the other.
- **P094** [hidden] `templates/extensions/content.tmpl:8`, `templates/extensions/panels.tmpl:6`, `web_src/js/features/extension-mount.js:58`, and `web_src/js/features/extensions.js:122` separately enumerate the mount dataset and identity fields; adding an admission-relevant field requires coordinated template serialization, parsing, page identity, loose identity, and panel identity changes or retained owners can be adopted under stale facts.
- **P096** [special] `web_src/js/features/extensions.js:61` treats every pathname ending in `/workspace` as a direct contribution path and `web_src/js/features/extensions.js:67` recognizes the repository form only when segment 3 is literally `workspace`; the otherwise manifest-driven direct-route mechanism retains one product route as host-owned vocabulary, so changing the preferred page's route requires browser code changes.
- **P097** [special] `routers/web/extensions/extensions.go:80`, `routers/web/extensions/extensions.go:269`, and `routers/web/extensions/extensions.go:308` register and resolve repository contribution roots/subroutes only under literal `feed`, with repeated code/issue-read checks; repository pages follow a separate one-route path rather than the generic manifest route loop, so another repository route requires host routing changes.
- **P098** [special] `web_src/js/features/extension-mount.js:69` exempts linked panel owners from the ordinary API-scope validator using an `includes('/-/native/pages/...')`/`endsWith('/api/')` test; changing canonical shared-owner API identity requires this panel-only branch to track `composePanelsHome`'s page-base rewriting.

<a id="f04"></a>

### F04 — Freeze historical desktop-grant migration schemas (1 finding)

Make each numbered migration describe its own historical table shape instead of syncing the mutable current `desktopgrant.Grant`. Recover the intended table names, columns, defaults, indexes and migration order from Git history for v17d, v17e and v17g, then freeze minimal migration-local records with explicit original table mapping. Keep current runtime models out of these historical definitions. New runtime fields require a new forward migration; this does not rewrite existing database rows or reinterpret existing consent.

**Structural checks:** A historical schema changes only when correcting that historical migration, while current grants evolve through forward migrations. These have different reasons to change and share no mutable DTO. The migration depends on its fixed local schema and existing migration/ORM support, not the live authorization model.

**Done when:** Changing the runtime Grant cannot change the three historical migration steps; fresh and upgrade sequences produce the intended columns/defaults/indexes in the same order, with existing data and grant meaning preserved.

**Depends on:** None.

**Implementation validation:** Compare migration SQL/schema on fresh and representative pre-v17d/v17e/v17g fixtures using existing migration infrastructure. Keep this validation separate from runtime consent tests; do not edit old test expectations to accept a changed schema.

**Audit entries addressed:**

- **P018** [needless] `models/forgejo_migrations/v17d_add-native-desktop-grant.go:12`, `models/forgejo_migrations/v17e_bind-native-desktop-proof.go:12` and `models/forgejo_migrations/v17g_add-desktop-issue-grant-kind.go:12` all sync the current `desktopgrant.Grant` struct; future runtime schema edits change what already-numbered historical migrations do when replayed on a fresh database.

<a id="f05"></a>

### F05 — Use factual qualification and release inputs (6 findings)

Keep Make, Bash and GitHub Actions as the orchestrators. Replace whitespace-sensitive Go-source scraping with a small SDK-only metadata command that reads compiled protocol/browser constants for the exact prepared source. It must work without building the host application. Use root `go.mod`'s toolchain directive, `.node-version` and `package.json`'s packageManager as canonical tool versions, with SDK toolchain parity checked. Do not replace the copied session module's historical minimum toolchain merely to match application tooling.

Give prepared-source/artifact naming one small shell helper or manifest consumed by build, image checks, release metadata and publication. Preserve `+gitea-1.22.0`, linux/amd64, existing filenames/release JSON fields, immutable tag rules and the distinction between image config and registry digests. Keep dry-run behavior and existing script entrypoints/fixtures.

Use one finite qualification-policy input and one workflow-lint inventory. GitHub trigger/branch literals that must exist before scripts execute stay explicit YAML declarations, checked for consistency rather than generated wholesale. Preserve the maintained `codex/fountain-extensions` branch, `ci/fountain` exact/full-SHA rules, workflow identity, `Fountain CI` gate, run/source/app/event/success-or-allowed-skip checks, five-minute reuse budget and fast-forward push. The audit's `main` checkout is not a release-policy branch change.

Drive session formatting/provenance coverage from the verified eight-path local-delta inventory, including memory lifetime changes. Correct the factual patch inventory; this is not a comment-cleanup pass. Never format unchanged imported providers. Resolve helpers relative to the invoked script so fixture invocations keep working.

**Structural checks:** Version, artifact naming and qualification facts change with their actual owners. Release admission remains independent from application runtime and historical dependency toolchain minima. Scripts/workflows consume stable metadata and policy data, without a build framework or a dependency on a successful root application build.

**Done when:** Declaration formatting cannot change release protocol metadata; one toolchain change and one naming change reach all relevant consumers; all Fountain workflows are linted consistently; the session delta inventory includes exactly local changes; admission accepts/refuses the same commits and artifacts as before.

**Depends on:** F01

**Implementation validation:** Use the existing shell/tooling fixtures, all ten syntax checks, workflow validation and dry-run metadata/image-name comparisons. Validate exact-commit refusals and SDK/root toolchain parity; do not publish images, tags or releases as part of verification.

**Audit entries addressed:**

- **P066** [hidden] `tools/fountain-release-metadata.sh:16` extracts protocol values with whitespace-sensitive `awk` patterns from `sdk/manifest.go` and `sdk/contract.go`; a declaration-format or type-annotation change with unchanged Go semantics can independently break release metadata extraction.
- **P067** [hidden] `tools/fountain-release.sh:49`, `tools/fountain-release-metadata.sh:12`, `tools/fountain-image-test.sh:12` and `.github/workflows/fountain-release.yml:142` each reconstruct the `+gitea-1.22.0` prepared-source/version naming convention; a change to the compatibility identity must be repeated in build, validation, metadata and publication paths.
- **P068** [hidden] `go.mod:5`, `sdk/go.mod:5`, `.github/actions/fountain-setup/action.yml:26`, `tools/fountain-check.sh:17` and `tools/fountain-release.sh:27` independently pin Go 1.26.10, while the check script also embeds its full version-output string; a toolchain update requires synchronized module, CI, local-check and release edits, including the shipped-version assertion at `tools/fountain-image-test.sh:14`; npm 11.11.0 is separately pinned by `package.json:4` and `.github/actions/fountain-setup/action.yml:38`.
- **P115** [hidden] `third_party/go-chi-session/FOUNTAIN.md:7`–`:9` declares only `file.go`/file lifecycle tests as local source changes, and `tools/fountain-check.sh:39,50` encodes that same file-only patch inventory, while `third_party/go-chi-session/memory.go:88,114,158,189,196` and `memory_lifecycle_test.go` contain local changes; patch rebasing and formatting coverage must change with memory lifetime semantics, but their maintained inventories omit that implementation.
- **P116** [hidden] `tools/fountain-check.sh:96,134` separately enumerate workflow-lint targets: push packaging checks push and full CI, deep packaging checks only push CI, and neither checks `.github/workflows/fountain-release.yml`; adding/changing a Fountain workflow requires coordinating separate verification inventories that already differ.
- **P117** [hidden] `tools/fountain-push.sh:24,26,38,51`, `tools/fountain-ci-reuse.sh:10,11,14,18`, `.github/workflows/fountain-ci.yml:5,6,154`, `.github/workflows/fountain-full-ci.yml:5,6`, `.github/actions/fountain-setup/action.yml:21` and `tools/fountain-release-run.sh:27` independently encode the maintained branch, exact-commit CI tag grammar, workflow identity and required gate name; a branch/workflow/tag/gate change requires manually aligned edits across qualification, admission, trigger, and release code.

<a id="f06"></a>

### F06 — Give native features and widgets an explicit lifetime (19 findings)

Add a small page-feature run/lifetime token before the first asynchronous step: abort signal, current-run check, resource ownership and disposal. The feature registry observes initializer promises, reports failures and retires acquired resources in reverse order. Do not block contribution reconciliation on large lazy widgets; pending runs can settle only into their own still-current page. Capture each request's page-data object and check the run after every await, including body decoding, errors and finally. An outgoing completion never clears an incoming page's flags or redirects its document.

Factor the repeated native Vue mount mechanics, leaving component URLs, payload transforms and business rules with each component. Pass initialization data through a supported component prop/adapter rather than calling its private `data()`. Share only the three graph components' latest-request cancellation and existing 202/one-second polling protocol; do not fold the independent administration observer into a polling framework.

Let ComboMarkdownEditor accept a lifetime signal before its first await while retaining its Promise/getter exports. Its public destroy owns EasyMDE, listeners, tribute/autosize/modal resources and pending initialization; wiki uses that boundary. CodeMirror view destruction unregisters the view itself. Dropzone and Sortable late completions adopt only their exact originating run, otherwise destroy themselves. Moved native dialogs retain their originating page owner and close/restore/remove on retirement; document-global and contribution-owned dialogs keep their distinct ownership.

Delegate native controls only outside contribution roots. Registry ownership retires independently registered image-diff/conversation features exactly once rather than through diff-view teardown. Replace the actions-template private polling global dependency with a feature-owned before-request gate and explicit marker, retaining any exported compatibility surface needed. Keep page-data replacement generic: mention normalization is a pure mention adapter preserving synchronous last-name-wins/empty replacement behavior, and auto-merge visibility is derived once in the Fountain JSON template. Keep the five-second webhook redirect only while the originating page run is current; never replay an already-issued mutation.

**Structural checks:** Resource acquisition and retirement move into the same run/widget owner; component business data, mention semantics and unrelated polling remain independent. Features depend on a small lifecycle primitive and supported widget APIs; the generic registry does not depend on feature-specific state, Vue internals or contribution implementation classes.

**Done when:** Retiring during imports, fetch/JSON, pending editor/widget creation, dialog display or delayed redirects leaves no stale DOM/global writes or orphan resource. Async failures are observed; existing page/widget exports and mutation semantics remain compatible; native delegation cannot act inside contributions.

**Depends on:** F03

**Implementation validation:** Run frontend/browser checks with deferred import/fetch/body/widget completions, navigation during success/error/finally, reopened dialogs, repeated editor destruction, contribution controls and exactly-once retirement. Check graph 202 polling and webhook delay without replaying effects; add only regression cases for real gaps.

**Audit entries addressed:**

- **P051** [needless] `web_src/js/features/repo-wiki.js:82` and `web_src/js/features/repo-wiki.js:123` tear down the editor through its `easyMDE` member and private container expando, bypassing `web_src/js/features/comp/ComboMarkdownEditor.js:53`'s destroy lifecycle; changes to editor transport, tribute, autosize or modal cleanup must also be reflected in wiki-specific internals knowledge.
- **P052** [hidden] `web_src/js/features/common-global.js:43` records a Dropzone before its asynchronous lazy creation at `web_src/js/features/common-global.js:374`, but `web_src/js/features/common-global.js:308` can retire and forget the element before that creation settles, so mount completion and page retirement have no shared cancellation/identity contract.
- **P053** [hidden] `web_src/js/modules/modal.ts:14` moves page dialogs into the persistent body, while `web_src/js/features/repo-branch.ts:10` and `web_src/js/features/comp/LabelEdit.js:119` retire only their control listeners; opened dialogs outlive the swapped page and subsequent visits can resolve duplicate modal IDs to stale forms.
- **P054** [special] `web_src/js/features/comp/WebHookEditor.js:17` preserves a five-second post-delivery redirect, but the new retirement at `web_src/js/features/comp/WebHookEditor.js:22` only detaches click/change handlers, allowing work started on the outgoing page to redirect a later native page.
- **P055** [hidden] `web_src/js/features/repo-diff.js:173` drops retired-page responses using a generation guard, but its `finally` at `web_src/js/features/repo-diff.js:188` reads the current page-data object and clears that page's loading flag, coupling an outgoing request's completion to an unrelated incoming diff's pagination.
- **P056** [hidden] `web_src/js/features/repo-issue-list.js:220` admits a lazy Sortable by a single reused `pinSortableWanted` boolean, while `web_src/js/features/repo-projects.js:75` ties the same resource to a specific state object; navigating between sortable issue lists can make an old pending creation pass the new page's boolean and replace its tracked instance.
- **P057** [hidden] `web_src/js/native-lifecycle.js:47` invokes feature initializers without observing returned promises, although `web_src/js/features/dashboard-repo-list.ts:14` and other registered features initialize asynchronously and rethrow failures; the transplant's synchronous catch at `web_src/js/features/extensions.js:796` consequently cannot govern whether those features actually initialized.
- **P060** [hidden] `web_src/js/features/common-global.js:538` and `web_src/js/features/common-global.js:560` now delegate native delete/modal controls for the whole document without the contribution-root exclusion used by `web_src/js/features/comp/ReactionSelector.js:11`; contribution implementations must privately avoid native CSS classes despite the root-boundary contract in `web_src/js/native-lifecycle.js:10`.
- **P062** [hidden] `web_src/js/features/repo-issue-content.js:152` checks page generation before awaiting JSON, then `web_src/js/features/repo-issue-content.js:155` updates shared translations and resolves live comment IDs after that await; page retirement during body decoding is therefore invisible to a history request's DOM/global-state writes.
- **P063** [hidden] `web_src/js/features/repo-issue.js:278` and `web_src/js/features/repo-issue.js:654` bind page-owned PR update/WIP saves whose completion redirects or reloads the live document, while their retire handlers at `web_src/js/features/repo-issue.js:321` and `web_src/js/features/repo-issue.js:677` only unbind controls; outgoing saves and incoming navigation remain implicitly coupled after the new transplant seam.
- **P065** [hidden] `templates/repo/issue/view_content/pull.tmpl:242` repeats the same auto-merge visibility expression in five merge-style records after the inert-JSON migration, replacing the previous single derived value; any visibility-rule change now requires five synchronized template edits.
- **P100** [needless] `web_src/js/native-lifecycle.js:75` makes generic page-config replacement know the mention widget's `mentionValues` collection, `name` identity and last-value deduplication rule; changes to mention data semantics require changes inside the lifecycle seam used by every page feature.
- **P101** [hidden] `web_src/js/features/code-frequency.js:6`, `web_src/js/features/contributors.js:6`, `web_src/js/features/recent-commits.js:6`, `web_src/js/features/dashboard-repo-list.ts:8`, `web_src/js/features/repo-activity-top-authors.ts:8`, and `web_src/js/features/heatmap.js:6` repeat native Vue ownership: mutable mount slot, retirement/unmount, duplicate-element guard, generation capture and late-import rejection; changing mount invalidation or retry semantics requires synchronized edits across otherwise independent chart/dashboard features, and their error paths already differ.
- **P102** [hidden] `web_src/js/components/RepoCodeFrequency.vue:55`, `web_src/js/components/RepoContributors.vue:120`, and `web_src/js/components/RepoRecentCommits.vue:51` separately add the same graph request owner, abort-on-replacement/unmount and aborted-result/error/finally guards around the same 202 polling protocol; a request-lifetime rule change must be repeated across all three chart components.
- **P103** [needless] `web_src/js/features/repo-branch-tag-selector.js:39` directly calls `RepoBranchTagSelector.data()` while overriding the component's data function and spreading its private state into the entry wrapper; changing how the component initializes lifetime state also changes its external mount caller.
- **P104** [hidden] `web_src/js/features/codeeditor.ts:115` and `web_src/js/features/repo-settings.js:23`/`:139` manually remove views from `window.codeEditors` before destroying them, while `web_src/js/features/codemirror.ts:10` owns creation/registration; changing editor registration or teardown semantics requires updates in independent callers because registry cleanup is not conveyed by the view's ordinary `destroy` operation.
- **P105** [hidden] `web_src/js/features/repo-milestone.js:18`, `web_src/js/features/repo-release.js:93`, and `web_src/js/features/comp/ComboMarkdownEditor.js:699` rely on `initComboMarkdownEditor()` synchronously publishing its partially initialized instance on the container before the promise settles; callers start init, immediately retrieve that instance with a separate getter for retirement, then compare it after await, so moving initialization across an async boundary silently breaks their pending-editor cleanup.
- **P106** [needless] `web_src/js/index.js:190`/`:194` registers conversation navigation and image diff as independently retired page features while `web_src/js/features/repo-diff.js:243`/`:250` also retires both from diff-view retirement; these independently registered feature lifetimes are tied to diff-view cleanup and rely on their duplicate teardown being idempotent.
- **P107** [hidden] `web_src/js/features/repo-actions-list.js:15` exports the polling predicate through `window.pollingOk`, while `templates/repo/actions/list.tmpl:13` invokes that global inside HTMX trigger expressions; renaming/removing the feature or changing global-init order must track the distant template expression, with no import from the template.

<a id="f07"></a>

### F07 — Coordinate retained owners, session checks and navigation intent (8 findings)

Have one document controller own session listeners, generation and pending revalidation, retaining the current initialization entrypoints as idempotent adapters. Track roots explicitly as pending, mounted, sheltered or terminal before async mount begins. Fragment synchronization of a still-connected identical root does not cancel its pending mount or strand a mounted marker. Removal, identity change or authority loss cancels the exact run, and late owners dispose themselves.

Represent suspension as independent holds (shelter, document visibility/BFCache, session check). Invoke public `suspend()` and `resume({sessionGeneration: admittedGeneration})` only at active/inactive edges after current live validation. Preserve the validation-bearing resume contract and fail-closed retirement on missing/mismatched generation. Successful session revalidation clears its own hold, never the shelter hold; late concealed mounts inherit all current holds. Isolate each owner's update/suspend/resume/retire callback, revoke internal authority/abort resources before external callbacks where required, finish the batch, then report failures. A bad contribution cannot prevent another owner or the session check from progressing. Remove each closed socket from its owner's set; suspension continues to preserve open streams.

Normalize all callers into one navigation intent carrying target, push/replace/traverse behavior, source and history bookkeeping. The GET-form adapter alone supplies same-query refresh and submitted-form dirty exemption. Preserve replace semantics through top destinations, admission rejection, timeout and full-document fallback; use the matching assign/replace behavior. Superseded or authority-aborted requests cannot initiate fallback; a still-current timeout follows the original intent's full-load fallback. Traversal fallback respects the already-landed history entry and never creates an extra one.

Make history write/stamp/restore failure an explicit transition result with coherent native fallback or restoration, including forward-history/index compensation. Do not silently publish mismatched address/content. Keep existing full-load confinement, 15-second timeout, four-entry oldest-evictable shelter with existing persist exemption, and best-effort terminal cleanup policies.

**Structural checks:** Session authority, owner states and their transitions are coordinated by the document controller. Product retention limits, contribution callbacks and native widget lifetimes remain separate. The controller depends on admitted mount facts and feature lifecycle APIs; lower-level mount/resource helpers do not import host navigation or session policy.

**Done when:** Each document runs one session observer; fragment navigation preserves valid pending mounts; revalidation never activates a sheltered owner; one throwing owner cannot stop all others; closed sockets are forgotten; every navigation/fallback preserves its requested history semantics and either restores or coherently leaves on history failure.

**Depends on:** F03, F06

**Implementation validation:** Use browser regressions for deferred fragment mounts, multiple suspension holds, throwing owners, closed streams, same-query GET, dirty forms, replace fallbacks, traversal failures and timeout. Preserve the existing four-page shelter, BFCache and full-load behavior cases.

**Audit entries addressed:**

- **P046** [hidden] `web_src/js/features/extensions.js:450` and `web_src/js/features/extensions.js:537` independently subscribe to focus/BFCache/visibility events and revalidate the same session; every eligible document runs both controllers, so one session-policy change must update two event lists, pending/version guards, conceal/reveal paths and invalidation paths.
- **P058** [needless] `web_src/js/features/extension-mount.js:160` retains every created socket in the owner-wide set at `web_src/js/features/extension-mount.js:215` until owner disposal, without removing sockets on close; independent completed stream lifetimes accumulate in the long-lived contribution owner.
- **P061** [hidden] `web_src/js/features/extensions.js:314` and `web_src/js/features/extensions.js:323` remove every owner from the tracking array before calling contribution-provided cleanup without per-owner isolation; one throwing owner prevents all later owners from closing resources during unload or session revocation, while `web_src/js/features/extensions.js:287` isolates the same callback during individual panel withdrawal; suspend/update/resume loops at `web_src/js/features/extensions.js:237`, `web_src/js/features/extensions.js:366`, `web_src/js/features/extensions.js:404`, `web_src/js/features/extensions.js:457` and `web_src/js/features/extensions.js:464` have the same cross-owner failure coupling, including a suspend exception preventing session revalidation from starting.
- **P108** [special] `web_src/js/features/extensions.js:665` adds `excludeForm` and `viaSubmit` parameters understood only by GET-form navigation; form submissions bypass the same-URL no-op and exclude the submitted form from dirty checks, so link/history/programmatic transitions carry flags for one caller's semantics.
- **P110** [special] `web_src/js/features/extensions.js:584`/`:594`/`:871` silently catches history-stamping, URL-tracking and traversal-restoration exceptions, allowing navigation bookkeeping to continue with incomplete state; browser-history failure policy is represented as several independent exception branches.
- **P118** [hidden] `web_src/js/features/extensions.js:341` aborts every pending page mount during contribution synchronization, including the fragment-only navigation event emitted at `:688`; the same live root keeps `data-extension-mounted` from `:208`, and `mountRoot` refuses marked roots at `:200`, so fragment routing and pending async initialization must coordinate an undocumented cancellation/retry convention or a still-visible page remains permanently cancelled.
- **P119** [hidden] `web_src/js/features/extensions.js:641` forwards programmatic replace intent only to transplant's inverse `push` argument, while the top-destination branch at `:644` and full-document fallbacks at `:722`, `:728`, `:742`, `:752` and `:797` always call location.assign; changing a route from in-shell admission to full load silently changes its history behavior despite `sdk/browser/extension-contract.d.ts:139` guaranteeing replace semantics for context.navigate.
- **P120** [hidden] `web_src/js/features/extensions.js:237` suspends sheltered page owners, but successful session revalidation at `:464` resumes every mounted owner, including roots still inside the hidden shelter; `web_src/js/features/extension-mount.js:304-306` changes the same single suspended state to active without knowing its suspension reason, so session continuity cancels the unrelated hidden-page suspension and restarts visible work without readoption.

<a id="f08"></a>

### F08 — Derive native admission and observations from authoritative facts (15 findings)

Share model-owned native action facts for capability reporting, preparation and execution. Factor issue visibility/issue-only filtering and comment projections so list and explicit-ID reads agree. Derive the actual user-column update plan once, then classify that plan through the model's authority-changing rule; preserve known exemptions and fail closed for unknown columns. Native merge evaluation returns structured blocker decisions directly instead of snapshots replaying its private predicate order. Explanatory snapshots never replace final live checks under the execution lease/transaction.

Move after-cursor filtering into bounded model queries so later valid comment/review/status cursors can advance beyond the first 501 rows. Preserve ordering, page/sentinel semantics, latest-check-per-context behavior and honest completeness/counts. Give thread reading an explicit bounded/completeness contract rather than hydrating every comment behind a limited page API. Resolve legacy anchorless identity against the full available context, or mark it ambiguous/incomplete; never infer immutable thread identity from an arbitrary page prefix. Preserve conservative legacy reply refusal for ambiguous or over-10,000-row location-only forms.

Share the idle-revision/read-twice/recheck bracket across typed preparation readers; leave their payloads and disappearance mappings explicit. PR files and commits share admitted comparison identity, Git lifetime, cursor binding and post-I/O recheck, with separate output projections. The desktop and callback surfaces consume these same facts instead of restating eligibility.

Centralize PAT identity/fingerprint/scope/resource primitives in the auth model, with explicit read, mutation and controller profiles rather than one permissive verifier. Keep actor/repository/current credential checks inside their existing transactional placement; no cached preparation becomes write authority. Bind grant kind through typed route/admission data instead of a magic context key. Move the configured-appliance legacy intake restriction to its explicit controller admission policy, preserving the exact configuration-specific fence and unrelated legacy intake behavior. Retain proofless legacy repository-read grants, protocol-2 discovery semantics and the distinct human/conditional draft contracts.

**Structural checks:** The fact, its permission rule and all projections change together, while wire profiles, payloads, migration history and final write admission remain independent. Services/routers consume model-owned facts; models never import SDK snapshot DTOs or router/service policy. Typed preparation brackets depend on stable revisions, and F02 supplies neutral ownership rather than a new downward service import.

**Done when:** Capability/preparation/execution agree on native eligibility without weakening final admission; explicit-ID reads honor issue-only grants; later cursors progress with bounded reads and honest completeness; thread identity remains conservative; PR readers share stable comparison identity; user updates and typed consent profiles cannot drift across entrypoints.

**Depends on:** F01, F02

**Implementation validation:** Exercise snapshot/native/auth tests for issue-only IDs, later cursors, status context selection, bounded/incomplete legacy threads, draft modes, structured protection blockers, revision races, PR identity changes, unknown authority columns, all grant profiles and configured-appliance legacy intake. Historical consent bytes and permission boundaries must remain unchanged.

**Audit entries addressed:**

- **P012** [hidden] `services/nativeoperation/snapshot_read.go:266` enforces `decision.IssuesOnly` through `loadIssue`, while `:481` independently checks the parent for explicit comment IDs without that restriction; maintaining the issue-only grant boundary requires synchronized visibility edits across the index and ID lookup paths.
- **P013** [hidden] `services/nativeoperation/snapshot_threads.go:382` repeats the protected-branch predicate sequence from `services/pull/merge.go:558` to infer the first blocker after the evaluator returns an opaque refusal; adding or reordering a native protection rule requires a separate change to snapshot explanation logic to keep the claimed blocker accurate.
- **P014** [hidden] `services/nativeoperation/snapshot_read.go:427` and `:490` independently map the same native comment snapshot into SDK fields for paged and explicit-ID reads; a new field or redaction rule must be applied to both copies for these views of one comment to stay equivalent.
- **P015** [special] `services/nativeoperation/snapshot_read.go:399`, `:811` and `:863` fetch the first 501 comment/review/status rows before applying an arbitrary after-ID cursor, so later pages cannot reach records beyond that fixed prefix and remain incomplete even though the public request accepts later cursors.
- **P020** [hidden] `services/nativeoperation/snapshot_threads.go:116` reasons about a bounded comment fetch using `snapshotFetchCap`, while `models/issues/thread_group.go:36` now fetches and hydrates every code comment with no limit; the snapshot page limit therefore does not bound its underlying read, and the truncation/count logic depends on a fetch contract the shared helper no longer supplies.
- **P029** [hidden] `models/extensionauth/verify.go:99`, `models/extensionauth/verify.go:272` and `models/extensionauth/controller.go:90` separately implement PAT identity/fingerprint, scope/resource and account/repository checks, so native credential-policy changes require synchronizing distinct submission, snapshot and controller paths in addition to their intentionally different kind permissions.
- **P030** [hidden] `routers/web/extensions/callback_comment_snapshot.go:69`, `routers/web/extensions/callback_capabilities.go:108` and `routers/web/extensions/callback_pull_capabilities.go:116` repeat the comment lock/writer/site-admin and repository-archive policy for snapshot preparation and generic capability reporting; a native comment eligibility change must be repeated in all projections to keep them consistent.
- **P031** [hidden] `routers/web/extensions/callback_comment_snapshot.go:17`, `routers/web/extensions/callback_edit_snapshot.go:17`, `routers/web/extensions/callback_state_snapshot.go:20`, `routers/web/extensions/callback_lock_snapshot.go:19` and the other mutation snapshot resolvers separately copy the idle-revision/read-twice/re-read-revision and disappearance-to-conflict protocol; consistency-policy changes must track all these otherwise independent payload readers.
- **P036** [special] `routers/web/extensions/background.go:227` refuses the legacy issue-intake writer only when its package ID equals `setting.OAuth2.FactoryAppliancePackageID`; a generic background dispatcher therefore contains a deployment-specific controller-migration exception whose behavior changes with OAuth configuration.
- **P041** [hidden] `routers/web/extensions/callback_pull_files.go:194` and `routers/web/extensions/callback_pull_commits.go:19` duplicate PR parent/repository lookup, Git base/head object and ancestry validation, dotted cursor identity and post-I/O PR/ref identity recheck; changing what defines one stable PR comparison requires matching changes to both readers.
- **P072** [hidden] `services/user/update.go:52` classifies authority-changing UpdateOptions separately from `models/user/user.go:1023`'s column exemption map and `services/user/update.go:75`'s option-to-column mapping; changing a user's permission-bearing field requires keeping all three classifications aligned or the service and model disagree about ownership.
- **P078** [special] `models/issues/thread_group.go:117` assigns retained comments with no immutable anchor to the sole valid recorded context, but creates separate unknown conversations when several contexts exist; retained-data interpretation depends on how many later anchored comments share the native location.
- **P079** [special] `services/pull/review.go:260` rejects older location-only reply forms when over 10,000 rows or several immutable threads share that location; supporting new thread shapes requires coordinating reply form identity with grouping rules.
- **P085** [hidden] `routers/api/v1/repo/desktop_grants.go:39` writes the untyped `factory_desktop_issue_grant` context marker that `desktopGrantKind` reads, and `routers/api/v1/api.go:819` must install that marker for every issue-grant route; moving or adding a handler can change the persisted grant kind without a signature change.
- **P086** [hidden] `routers/api/v1/repo/desktop_issue_collaboration.go:132`–`:154` independently computes comment/edit/state/lock action capabilities from permissions, blocking and dependencies, while `services/nativeoperation/human_comment.go:110`, `services/nativeoperation/human_edit.go:120` and `services/nativeoperation/human_state.go:112` enforce the corresponding policies at submit; policy changes require updating the proposal and execution implementations together; the desktop preparation also repeats action-fact and revision/double-read rules in `routers/web/extensions/callback_comment_snapshot.go:17`, `routers/web/extensions/callback_edit_snapshot.go:17`, `routers/web/extensions/callback_state_snapshot.go:20` and `routers/web/extensions/callback_lock_snapshot.go:19`.

<a id="f09"></a>

### F09 — Unify package provenance, lifecycle admission and runtime liveness (8 findings)

Make installer and recovery share package-origin validation, distinguishing supported prebuilt packages from catalog-verified receipt-bearing packages. Under the existing store lock, persist a minimal replacement witness before the first rename, tying exact old/new package IDs, origins and fixed paths. This host-created witness is not package-supplied authority. Continue accepting valid existing catalog schema-1 records; never downgrade a missing/invalid required receipt to prebuilt. Ambiguous legacy receiptless backups stay intact and fenced pending explicit repair.

Use one source-snapshot verifier for online caching and offline release preparation, taking the trusted source catalog SHA/required identity, controller scope and live intent/pending facts. Source and target catalog are distinct inputs. The current exported preparation helper cannot prove source identity from target+snapshot alone: add an explicit source-trust entrypoint for production/CLI use, preserve the old export as a limited adapter, and never silently treat target as source. Stage all target bytes before the first replacement; preparation does not mutate desired intents.

Bind controller ownership durably while trusted catalog attribution is known, separately from the immutable schema-1 intent envelope (for example a bounded host sidecar). Another known controller's rows are excluded; catalog-unknown legacy rows remain preserved and fenced, not copied into every controller snapshot or guessed from the receiving controller. Do not silently add/rewrite intent wire fields; any actual format addition needs explicit versioning and the old decoder.

Name the lifecycle commands, transitions and admission purposes instead of carrying a public cleanup boolean/magic phase. Failed-enable cleanup is a narrow, single-use, deadline-bounded permission for that exact activation; desired/pending state survives and normal admission stays closed. Keep replay/retry explicit. Return typed stale/invalid/exhausted intent outcomes from the owning module and map them at the boundary; remove the now-useless substring helper and discarded detail computation.

Treat private HTTP listener and control RPC as one process liveness state. Observe unexpected Serve failure, drop readiness and propagate the failure through the existing manager health/Ready path so an alive-but-unavailable process is reaped. Expected server closure is normal. Preserve uncertain external-effect fencing; do not create a second control plane or enable go-plugin Test mode, which bypasses its handshake.

**Structural checks:** Package origin and crash recovery share one durable witness; lifecycle admission and process readiness have named ownership. Catalog source proof, target staging and intent/controller attribution remain distinct facts. The store owns filesystem invariants, SDK runtime owns listener failure, and manager composes them without importing the application from the SDK or turning package recovery into native-operation recovery.

**Done when:** Interrupted valid prebuilt replacements recover without inventing receipts; unprovable packages/snapshots/unknown controller rows stay fenced; online/offline verification applies the same source contract; cleanup never grants ordinary authority or clears pending work; typed intent outcomes classify correctly; private-listener failure cannot remain ready.

**Depends on:** F01, F08

**Implementation validation:** Use existing store/bootstrap/lifecycle/socket tests and crash points before/between renames, source-vs-target catalog mismatches, pending/required/live coverage, two-controller/unknown rows, exact cleanup expiry/reuse, invalid vs stale generations and listener-only failure. Preserve exported lifecycle facade tests and expected-close behavior.

**Audit entries addressed:**

- **P027** [hidden] `modules/extensions/store.go:96` permits prebuilt installs/replacements without a receipt, while `modules/extensions/store.go:339` requires a generated receipt when recovering their `.previous-<id>` backup; the installer and crash recovery disagree about valid prebuilt package state, so an interrupted prebuilt replacement cannot recover its valid old bytes through the common recovery path.
- **P028** [hidden] `modules/extensions/bootstrap.go:257` and `services/extensions/manager_control.go:598` independently verify the same frozen source snapshot with different conditions: offline preparation checks optional pending-operation equality and complete live intent coverage but skips required rows/catalog SHA, while online caching checks catalog SHA/required identity but omits pending-operation equality and live intent coverage; one snapshot-contract change must be carried through both implementations.
- **P034** [surplus] `services/extensions/lifecycle_dispatch.go:1068` constructs a cause-dependent `detail` string and discards it with `_ = detail`, then always returns the fixed backend-failed message; the branch and `cause == nil` distinction do not affect the result.
- **P035** [special] `services/extensions/lifecycle_dispatch.go:330` and `services/extensions/lifecycle_dispatch.go:1042` authorize failed-enable cleanup through a `CleanupException` admission plus the magic mutation phase `"cleanup"`, allowing deactivate while persisted desired state/pending still say activate; this recovery-only authority rule spans admission issuance, mutation state, verification and cleanup completion.
- **P038** [special] `services/extensions/manager_control.go:104` appends every valid intent not selected by `managed(id)` to each controller's snapshot, using a comment about catalog-unknown ownership even though the intent carries no controller identity and the loop also includes catalog entries managed by another controller; catalog removal/ownership cannot be represented without this fallback.
- **P039** [special] `sdk/runtime.go:255` discards the private HTTP server's `Serve` failure in its goroutine while the separate control RPC remains ready, so listener failure has no lifecycle/error propagation path back to the process-handshake state.
- **P042** [hidden] `services/extensions/lifecycle_dispatch.go:820` recognizes stale intent errors by searching their English text for `"changed; reload"` or `"generation"`, coupling HTTP refusal classification to wording from `modules/extensions/intent.go:68`, `modules/extensions/intent.go:296` and `modules/extensions/intent.go:303` and classifying invalid/exhausted generations as stale conflicts as well.
- **P045** [surplus] `services/extensions/lifecycle_dispatch.go:828` reimplements substring search in `containsSubstring` solely for the text-based stale-error classifier, with no independent domain rule or variation beyond string containment.

<a id="f10"></a>

### F10 — Compose native operation execution and evidence-based recovery (7 findings)

Extract one sealed conditional execution lane for capability setup, claim, busy/stale/replay handling, cancellation, primary/final transitions and retirement. Keep live human execution as a distinct lane with its final current-admission recheck. Per-kind code supplies typed preparation, primary effect and evidence operations; it cannot replace lease/transaction ordering through arbitrary hooks. Retain the existing per-kind exported model wrappers and public service adapters.

Build one typed host service composition for journal parsing, submission and recovery readiness, consumed by dispatch and advertised handshake membership. Stable journal kind declarations remain in a lower-level contract usable by model validation; models never import the service handler composition. The service verifies complete implementation/recovery coverage against that contract before advertising readiness. This is separate from F01's callback wire description and enrollment vocabulary. Provide explicit stopped-domain recovery for thread resolve and the three lifecycle operations from their actual immutable primary evidence. Missing or ambiguous evidence continues to fence; never substitute generic unlock, timeout recovery, side-effect replay or silent removal of supported operation semantics.

Put machine refusal codes at the model that owns the refusal, separate from diagnostic prose, preserving public reason/error adapters and receipt/lock decisions. Share the SQL-primary/capture/completion one-way transition primitive while retaining per-kind target locks. Give native pull creation-history encoding one producer-owned service helper so recovery compares exactly the bytes produced at creation. Encapsulate Git lock retirement in a Git/file-backend adapter with stopped-domain, exact operation/OID witness checks; unsupported backend/format stays fenced rather than guessing file paths or deleting arbitrary locks.

Preserve the explicit implementation revision/build generation compatibility authority. A source or binary hash is not a replacement for a semantic revision. Preserve distinct live-human vs conditional draft behavior and never retry an entered effect or uncertain notification.

**Structural checks:** Execution invariants, supported-kind composition and their recovery evidence change together. Live user admission, per-kind business effects and Git backend details remain separate owners. Services compose stable model/neutral-module APIs; models do not import execution services, SDK transport or Git storage internals, and recovery depends on producer-owned evidence contracts.

**Done when:** All participating conditional paths share the same sealed ordering; all human paths perform the same required live recheck; every advertised journal kind has explicit safe recovery; explanatory message edits cannot change refusal/fencing; SQL completion and pull-history evidence agree; Git lock cleanup is exact or refuses.

**Depends on:** F01, F02, F08

**Implementation validation:** Run existing operation/model/recovery suites with crash boundaries, replay/cancel/expiry, supported-kind parity, each lifecycle/thread recovery, semantic refusal mappings, SQL capture/completion and Git backend witness mismatches. Compare existing baseline failures explicitly; do not repair/delete tests or auto-update expectations as part of this design.

**Audit entries addressed:**

- **P002** [hidden] `services/nativeoperation/merge.go:28`, `services/nativeoperation/issuegraph.go:42`, `services/nativeoperation/prcreate.go:119` and `services/nativeoperation/lifecycle_issue.go:79`/`services/nativeoperation/lifecycle_review.go:89`/`services/nativeoperation/lifecycle_note.go:84` duplicate the capability-file/conditional-claim/busy-stale-replay/retirement sequence already implemented by `services/nativeoperation/review.go:225`; a reservation or capability-lifetime change must be repeated across these independent per-kind entry paths.
- **P003** [hidden] `services/nativeoperation/human_comment.go:59`, `services/nativeoperation/human_edit.go:57`, `services/nativeoperation/human_lock.go:56`, `services/nativeoperation/human_state.go:50`, `services/nativeoperation/human_review.go:61` and the inline/thread/reviewer submitters each repeat installation/admission/replay/deadline ordering; the later thread/reviewer versions add a final live-admission recheck absent from the older copies, so one request-lifetime policy is maintained in multiple evolving forms.
- **P004** [hidden] `services/nativeoperation/human_review.go:206`, `services/nativeoperation/human_inline.go:160`, `services/nativeoperation/human_thread.go:127`, `services/nativeoperation/human_review_request.go:135` and `services/nativeoperation/review.go:164` classify model failures by literal explanatory strings; changing an `ErrExactReviewRefused.Reason` at its producer independently changes receipt classification or whether ownership stays fenced at these separate switch tables.
- **P006** [hidden] `models/nativeoperation/operation.go:126`, `services/nativeoperation/service.go:219`, `services/nativeoperation/operations.go:48` and `services/nativeoperation/recovery.go:137` maintain independent supported-kind/parse/submit/recovery dispatch lists; the advertised thread-resolve and three lifecycle kinds have submitters but no conditional recovery case, so interrupted instances fall through to unknown-family fencing.
- **P016** [hidden] `models/nativeoperation/human_manual_merge_commit.go:16` and `models/nativeoperation/human_pull_create_commit.go:16`, their completion files `:13`, and recovery files `:15` duplicate the same SQL-primary/capture/completion/release state machine around different kind-specific locks; changes to pending/committed/revoked interpretation have to be repeated across both model families despite the shared `lockHumanSQLPrimaryOperation`.
- **P017** [hidden] `services/nativeoperation/human_pull_create_recovery.go:144` reproduces the reversed commit-list/JSON push-history encoding produced by `services/pull/pull.go:192`; changing native creation-history formatting independently makes completed creations fail recovery evidence checks.
- **P019** [needless] `services/nativeoperation/human_merge_recovery.go:151` retires a Git lock by constructing `refs/... .lock`, reading its raw `<OID>\n` contents and walking ref directories; recovery depends directly on the files ref backend storage/layout rather than the Git ref operations used by the merge producer, so backend/lock-format changes require this separate service to track them.

### Leave as is

These **25** entries retain their current behavior or compatibility surface. Related fixes must respect the requirement; a cross-reference here does not assign the finding a second time. P087 explicitly corrects the audit’s scope interpretation after upstream comparison.

**P001 disposition:** The exported per-kind model names already delegate their invariant to one primitive. Keep compatibility names; deleting their test/API surface buys no structural improvement.

- **P001** [surplus] `models/nativeoperation/human_comment_commit.go:11`, `models/nativeoperation/human_edit_commit.go:10`, `models/nativeoperation/human_issue_create_commit.go:11`, `models/nativeoperation/human_lock_commit.go:10`, `models/nativeoperation/human_state_commit.go:10`, `models/nativeoperation/human_review_commit.go:10`, `models/nativeoperation/lifecycle_issue_commit.go:18`, `models/nativeoperation/lifecycle_review_commit.go:16`, `models/nativeoperation/lifecycle_note_commit.go:15` and `models/nativeoperation/review_commit.go:20` expose per-workflow functions with identical signatures that pass every argument straight to `commitConditionalPrimary`; changes to the shared primitive require matching these names/signatures without any workflow-specific model behavior.

**P007 disposition:** This is an exported, tested verification API. Repository-only caller evidence does not prove downstream absence, and its removal would require excluded test/API cleanup. It must not be represented as proof that the production hook is qualified.

- **P007** [surplus] `services/nativeoperation/publish_exact.go:394` retains `VerifyPublishCommands` and its separate `ReceiveLine` shape with no production caller; the actual pre-receive and prepared checks in `services/nativeoperation/admission.go:459` and `:344` implement their own exact tuple rules, so this tested API can change without changing the runtime gate.

**P008 disposition:** The exported full-target checker evaluates different time/evidence from launch plus hook admission. Retain it until a separately authorized API deprecation establishes external use; do not weaken runtime admission to force reuse.

- **P008** [surplus] `services/nativeoperation/publish_exact.go:255` exports `VerifyExactPublishTarget` but all callers are tests (`tests/integration/publish_exact_test.go:55`); production uses `VerifyPublishLaunchTarget` plus hook ancestry checks, leaving a parallel full-target verification path maintained outside execution.

**P010 disposition:** An explicit semantic implementation revision and build generation is a compatibility decision. Automating it with source/binary hashes would change its meaning; retain the documented manual bump discipline.

- **P010** [hidden] `services/nativeoperation/operations_handshake.go:18` requires a manually bumped integer whenever validation, submit/get/cancel, refusal mapping or reservation semantics change; those edits span many files without a code dependency on the revision, so an uncoordinated change can leave the advertised implementation generation unchanged.

**P077 disposition:** The one-second, 25–250 ms wait is deliberately bounded admission-only contention handling before effects. The durable queue owns longer retries. Retrying an already-entered effect is not allowed.

- **P077** [special] `services/pull/own_collab.go:56` retries only reservation admission for one second with 25–250ms backoff to let a deferred worker outlive its parent's hold; tuning worker contention requires understanding the parent's completion timing and durable-queue retry behavior.

**P080 disposition:** A conditional exact review must not consume a human draft, while a current human action may consume its own draft. Distinct admission modes are required, not duplicated policy to collapse.

- **P080** [special] `models/issues/review.go:671` refuses conditional exact reviews when the actor already has a pending draft, while `models/issues/review_human_primary.go:85` deliberately submits through the native path that consumes that draft; review composition changes require preserving two distinct pending-draft contracts.

**P083 disposition:** The small named private submit-primary seam has negligible maintenance cost. Removing this two-line wrapper does not solve a lifecycle or authority problem and is not worth unrelated cleanup.

- **P083** [surplus] `services/pull/review.go:454`'s `submitReviewPrimary` forwards every argument and result directly to `issues_model.SubmitReview`; the extra service function has no independent policy or transaction boundary and changes only when the model API changes.

**P087 disposition:** Provenance correction after checking the upstream base: the duplicated mirror row/key/remote workflow is already present in Forgejo v15.0.10. It is inherited implementation outside this request. F02 may normalize only Fountain ownership integration through existing APIs; do not extract the inherited workflow.

- **P087** [hidden] `routers/api/v1/repo/mirror.go:429` and `routers/web/repo/setting/setting.go:781` each own the push-mirror row insertion, optional private-key persistence, Git remote creation and failure cleanup sequence; changing that workflow requires coordinated edits to both transports.

**P090 disposition:** Keep the exported exact-review adapter and its typed call signature for source compatibility. Repository production caller absence alone does not justify removing it.

- **P090** [surplus] `models/issues/review.go:616`'s `SubmitExactReview` only calls `SubmitExactReviewInline` with nil comments and drops that result; production callers use the inline function directly, so this wrapper's API evolves only with its wrapped function.

**P091 disposition:** Releasing ownership across outbound network push avoids self-server receive deadlock. Reacquisition protects local metadata afterward. One lease around both legs would violate this requirement.

- **P091** [special] `services/mirror/mirror_push.go:236` releases ownership during the outbound network push, then reacquires it for the local result row so a mirror target hosted on the same server can acquire its own receive ownership; local and remote legs have a different coordination contract from pull mirroring.

**P092 disposition:** Zero is the honest merge timestamp for an unmerged PR when the inherited ORM updates MergedUnix on unrelated writes. Keep this projection rule; rewriting the upstream ORM is outside scope.

- **P092** [special] `models/issues/snapshot.go:506` treats MergedUnix as unusable evidence for unmerged PRs and emits zero because its native ORM tag updates it on every row update; snapshot merge evidence depends on a separate interpretation of that persisted field.

**P033 disposition:** Retain the exported legacy activate/deactivate/install facade and the tests of its existing surface. Removal or forced routing through RequestEnabled changes compatibility and requires evidence/authorization beyond repository-only caller absence.

- **P033** [surplus] `services/extensions/lifecycle.go:249`, `services/extensions/lifecycle.go:312`, `services/extensions/lifecycle.go:433` and their `LifecycleRequest` validation in `modules/extensions/lifecycle.go:55` retain an older activate/deactivate/install facade whose callers are tests only; production administration uses `RequestEnabled`, while this facade retains its own prechecks, early already-running result and failure-recording path.

**P037 disposition:** The old comment float-normalizing canonical codec is part of existing operation/hash identity. Keep it; F01 accepts its digest without re-encoding and adds the missing comparison against that identity.

- **P037** [special] `sdk/native_human_comment.go:34` round-trips the typed comment payload through `any` before hashing to preserve old floating-point normalization, whereas newer human intents such as `sdk/native_human_lock.go:71` hash typed integer payloads directly; comment intent identity retains a separate historical codec rule that recovery and consumers must preserve.

**P044 disposition:** Keep the private tested proxy adapter. Removing it would force excluded test cleanup for little architectural benefit; the production admission path stays authoritative.

- **P044** [surplus] `routers/web/extensions/proxy.go:134` retains a proxy wrapper whose only callers are tests; production requests enter `proxyWithAdmission` through `api`, so the wrapper's independent timeout/admission path has no production caller.

**P081 disposition:** Proofless retained enrollment remains repository-read-only. It must not confer issue, mutation or controller authority. This is an intentional legacy consent boundary.

- **P081** [special] `routers/api/v1/repo/desktop_grants.go:95` admits an empty proof only for retained legacy enrollments, while operation grants and issue enrollments require a bound proof; the legacy read-only path has separate identity semantics from current enrollment.

**P082 disposition:** Preserve absent-header legacy behavior, exact protocol-2 discovery/scope expansion and refusal of unknown versions. A consent/version negotiation redesign is not needed for this untangling.

- **P082** [special] `routers/api/v1/repo/desktop_grants.go:55` recognizes only an exact `X-Factory-Desktop-Protocol: 2` header and expands the two repository scope strings to include issue scopes; every later consent version must coordinate this header switch with client behavior.

**P121 disposition:** The WebSocket adapter belongs at the pinned session dependency boundary: normalize case-insensitive Upgrade into the dependency’s literal expectation and preserve concrete hijack/release behavior. Revisit only on an actual dependency rebase, with existing WebSocket tests.

- **P121** [special] `routers/common/middleware.go:136` normalizes the Upgrade header to the exact lowercase spelling expected by the pinned session middleware and conditionally unwraps its concrete `DeferredResponseWriter` for hijacking; WebSocket release/connection behavior depends on this dependency-specific adapter in addition to the session options.

**P048 disposition:** The dormant presentation flags have no active production behavior. Deleting inert UI variants is low-value cleanup and is not necessary to establish the mount/lifetime boundaries.

- **P048** [surplus] `templates/base/head_navbar.tmpl:6` and `templates/base/footer_content.tmpl:1` branch on `PageIsExtensionWorkspace`, but no production source assigns that field, leaving two dormant UI variants coupled to an absent renderer flag.

**P059 disposition:** Keep the tested four-page oldest-evictable shelter and existing persist exemption as host retention policy. No new manifest or SDK retention setting is needed.

- **P059** [special] `web_src/js/features/extensions.js:240` imposes a hard-coded four-page shelter limit and excludes roots with `data-extension-persist`; retention policy depends on a private DOM attribute with no manifest/SDK declaration or production setter, while its intended shared Workspace owner uses a persistent panel instead.

**P095 disposition:** Retain the legacy serialized PageData projection for compatibility despite no current browser reader. F03 establishes the authoritative live mount descriptor without making unproven external-reader removal part of the change.

- **P095** [surplus] `routers/web/extensions/extensions.go:371` and `routers/web/extensions/extensions.go:471` publish extension identity, API/asset/entry URLs and panel links into `ctx.PageData` although the browser host reads these facts only from mount datasets; no production browser reader consumes `pageData.extension`, `apiBase`, `assetBase`, `entry`, or `extensionPanels`, so this second serialized envelope only changes alongside the live dataset representation.

**P099 disposition:** The Go, JavaScript and public TypeScript version pins are independently enforced boundary declarations. Keep version 1 and parity validation; a generator solely to remove three literals adds more coupling than it removes.

- **P099** [hidden] `web_src/js/features/extension-mount.js:54`, `sdk/contract.go:45`, and `sdk/browser/extension-contract.d.ts:120` separately hardcode browser contract version `1`; a version change requires manual synchronization of the Go-rendered mount data, JavaScript admission check and public TypeScript literal before compatible contributions can mount.

**P109 disposition:** The host’s 15-second transplant timeout is a local navigation policy. Keep the threshold; F07 makes its full-load fallback obey the original navigation/history intent.

- **P109** [special] `web_src/js/features/extensions.js:710` waits 15 seconds for a transplant fetch then abandons the retained-document transition for `window.location.assign`; the timeout threshold is a host-only navigation policy outside the fetch/admission contract.

**P111 disposition:** Terminal cleanup must attempt every owner/socket despite individual failures. Keep best-effort close/retire behavior; F07 fixes cross-owner isolation and reporting without making suspension terminal.

- **P111** [special] `web_src/js/features/extension-mount.js:169` suppresses all socket-close exceptions so teardown proceeds, and `web_src/js/features/extensions.js:287` suppresses contribution-retire exceptions while withdrawing panels; terminal cleanup has distinct exception policies in the resource owner and presentation host.

**P112 disposition:** Keep exported dirty-form/Dropzone compatibility wrappers. Repository caller evidence does not justify removing public entrypoints or rewriting tests under the user’s selected scope.

- **P112** [surplus] `web_src/js/features/common-global.js:50` and `web_src/js/features/common-global.js:326` retain exported `initGlobalFormDirtyLeaveConfirm`/`initGlobalDropzone` wrappers after `index.js` replaces their calls with `enhanceNativeNodes`; production search finds only the definitions and stale comment references, while the actual helpers run directly through enhancement, so the two wrappers no longer own behavior or have runtime callers.

**P113 disposition:** The visible administration status observer is already a cohesive local policy: stop on 401/403/404, tolerate transient errors and otherwise observe every two seconds. Keep it local rather than couple it to chart polling.

- **P113** [special] `web_src/js/features/admin-extensions.ts:69` silently ignores every refresh failure and `:49` permanently stops observation for 401/403/404, while `:78`/`:85` retry observation every two seconds otherwise; status-observation failure and retry policy must change together across refresh, visibility restart and interval setup.

## Progress

- **Audit complete (phase-1 history).** The original audit was completed before design began. User selected neither optional cleanup addition; that selection still applies.
- First pass: independent native, extension and browser areas plus parent cross-area/tooling reads; ordinary-writer integration was then independently covered.
- Fresh pass: independent native and browser reviewers discovered and confirmed additional patterns. New patterns were followed across the corresponding source families and consolidated into this log.
- Closure pass: fresh native, browser and integration reviewers wrote blind candidate files before reading the log, then compared findings. Their new browser cancellation/history/suspension and integration scope/patch-inventory/verification-list findings were checked and added. Broader unsupported Scope and editor reinit concerns were rejected; duplicate mechanisms only expanded existing entries.
- Final fresh pass: the reviewer wrote `.artifacts/untangle-audit/final-blind.md` before comparison; seven candidates matched existing entries, one was the already recorded surplus route matcher, and one broad Scope claim lacked concrete change-together evidence. **Zero worthwhile new findings**; see `final-check.md`.
- Parent validation checked representative findings against source/callers and their Fountain hunks, compared the local session sources directly with pristine v1.1.0 archive members, reconciled co-change with source mechanisms, and validated every problem's located files/line bounds and at least one Fountain-touched anchor. `finding-location-check.json` has no unresolved locations or out-of-scope-only entries.
- Deliberate requirements retained: exact authority/owner/generation proofs, fail-closed uncertain-write recovery, immutable receipts, bounded pre-effect contention, per-kind evidence, URL/session confinement, terminal best-effort socket cleanup and disclosed deterministic crash-test instrumentation are not blanket removal targets. Required behavior remains the constraint for the next phase; specials inventory the exceptions to understand, not permission to discard them.
- Audit final source check: tracked working-tree/index diffs are empty in the primary checkout; the only untracked review document is `UNTANGLE.md`. The LTS checkout remains clean. Ignored audit maps/logs/caches and installed `node_modules` are supporting artifacts. No application code, tests, existing comments, commits, remotes, PRs or issues were changed.
- **Redesign complete (phase 2).** Refreshed the retained skeleton/import maps, read the entire map and all 121 findings, and independently reviewed native, runtime and browser boundaries before writing the plan. Converted every special into an explicit requirement and assigned every finding exactly once: 96 under ten fixes, 25 left as is. Historical schemas remain separate from runtime policy. No implementation began.
- Plan review: the user reported **no additional constraints**. Independent contract/runtime and browser review checked the plan against source. Review clarified validation-bearing resume, current-timeout versus superseded-request fallback, and stable model kind declarations versus volatile service handler composition. Public entrypoints, wire/hash bytes, historical consent, exact ownership proofs and existing safety policies remain constraints.
- Redesign validation: stable-ID coverage, original audit text preservation, all 28 special requirements, dependency ordering/acyclicity, counts and unchanged tracked source are checked mechanically. The audit baseline is unchanged; no code tests were rerun for this document-only phase.
- **Refactor started (phase 3).** Review branch: `codex/fountain-untangle`, based on the pinned main commit. The existing untracked audit/design record is retained in this branch. No other initial changes existed.
- Baseline replay before source edits exactly matches the audit: SDK 125 pass; selected host 1,213 pass / 3 fail / 12 skip with the same failure/skip/package identities; session 47 pass; frontend 483 pass; browser TypeScript succeeds; all ten shell scripts pass syntax; host build reproduces the same desktop source EOF. New evidence: `.artifacts/untangle-refactor/baseline/`; audit evidence remains unchanged.
- [x] F01 — stable contracts and transport boundaries. Implementation and targeted verification complete; commit recorded below.
- [x] F02 — durable ownership and ordinary leases. Implementation and targeted verification complete; commit recorded below.
- [x] F03 — admitted route and mount identity. Implementation and targeted verification complete; commit recorded below.
- [x] F04 — frozen migration schemas. Implementation and SQLite/PostgreSQL verification complete; commit recorded below.
- [ ] F05 — qualification/release inputs.
- [ ] F06 — native feature/widget lifetimes.
- [ ] F07 — retained owner/session/navigation coordination.
- [ ] F08 — authoritative native admission/observation facts.
- [ ] F09 — package/controller lifecycle and liveness.
- [ ] F10 — native operation execution/recovery.
- Comments step: skipped by user selection. Tests cleanup step: skipped by user selection. Existing tests stay as the regression baseline; meaningful new regression tests may cover changed behavior.
- Fresh independent review and final baseline/full qualification comparison remain pending.

- Baseline prerequisite repaired: the issue-only family loop in `routers/api/v1/repo/desktop_source.go` lacked its closing brace. Added that brace and formatted the Fountain-owned file; the pinned Go/SQLite host build now succeeds. No test expectations or business rules changed. Evidence: `.artifacts/untangle-refactor/build-repair.{stdout,stderr}`.

- **F01 completed:** shared stdlib-only SDK syntax/namespace/strict-JSON leaves; host/model aliases; typed callback selector/result/capability composition; family-owned status interpretation; common human ID/result envelope with unchanged family canonical codecs and receipt validation; continued service host UID checks on every dial. Kept public request/response shapes and sentinel/error profiles. Semantic implementation revision advanced from 1 to 2 for changed validation behavior.
- **F01 bugs fixed:** background IDs could occupy the manual-merge/pull-create human namespaces; seven accepted capability-only packages (including issue intake) lacked callback transport; comment results did not compare the submitted canonical digest; lifecycle null booleans could manufacture false; service clients discarded the verified UID after bootstrap. Strict callback/background JSON now rejects duplicate decoded keys, non-object envelopes and trailing values as required. Existing nullable effect-generation behavior is retained.
- **F01 verification:** all 1,213 previously passing host tests remain passing; expanded affected-host selection has 2,116 passes / the same three baseline failures / 12 skips, including newly buildable command tests. Full SDK succeeds with 154 pass events on the retained JSON run and also passes after final family mapping changes. New focused lifecycle/UID/namespace and real seven-capability socket regressions pass. Exhaustive temporary status compatibility comparison covered 69 operations × 1,001 statuses with identical sentinel/text outcomes. A fresh reviewer initially found the family-ownership gap, then confirmed it resolved with no worthwhile F01 issue remaining. Evidence: `.artifacts/untangle-refactor/f01/`, `f01-contract-safety/` and `/tmp/f01-blind-review.md`.

- F01 commit: `ad0b2ce07c`. Baseline compiler prerequisite: `aa50ccdb0e`; plan/baseline record: `a3841316a5`.
- **F02 in progress:** neutral scope/owner codecs, lossless document edits, generation+byte-exact model CAS, shared ordinary runner and typed contention are implemented. Checked refinements and explicit authority targets have focused passing evidence. Git transcript/SQL-lock experiments required the documented bounded native-rename stage design; implementation preserves native rename metadata and fences uncertain results. No F02 completion claim yet.

- F02 focused checks so far: 16 ownership/authority/refinement checks and 23 PR-create parse/intent/completion checks pass; full neutral-module tests and focused exact-witness model tests also pass. A fresh core/authority review found no actionable defect. Explicit per-ref target conflict discovered during test review was corrected and covered. Rename/origin integration and final F02 comparison remain pending.

- **F02 completed:** persisted Scope/ScopedRef, owner/authority/target codecs and ordinary lifetime runner now live below models/services. Scope refinements preserve original root/per-ref bytes, use owner+generation+binary-exact witness CAS, and distinguish not-applicable work from proof/storage errors. 114 production authority callsites now supply explicit identities (100 attributable, 14 intentionally unidentifiable name/batch targets remain fenced). Repository, pull and issue writers share the neutral type/lifetime; archive transports share the repository service primitive. Legacy exports and original writer byte order remain compatible. Semantic implementation revision advanced to 3.
- **F02 rename/origin result:** explicit origins flow through HTTP/SSH/internal builders and hook transport, retaining the absent-marker legacy adapter. Exact typed code/wiki and active native-rename stages carry authority before the existing SQL transaction, admit only the bounded native hook transcript, and retire only after complete refs/HEAD verification. Native `branch -m` preserves config/reflog/worktree semantics. Indeterminate effects and active stages remain fenced even if a callback swallows an error; recovery cannot release on tips alone. Minimum supported Git uses its compatible porcelain snapshot format.
- **F02 bugs fixed:** scope/proof/storage refinement errors are no longer silently discarded; concurrent/stale refinements cannot replace another witness; target disagreement refuses; unknown witness bytes survive updates; diagnostic label edits no longer change authority identity; typed contention cannot be spoofed by English text. Wiki rename no longer changes the code repository HEAD. Late Git config failure, metadata rollback, or success-without-launch cannot silently retire an unproven rename. Expanded testing caught and restored the historical ordinary writer field order without changing existing test expectations.
- **F02 verification:** final 25-package Go/SQLite selection reports **2,574 pass / the same three baseline failures / 12 skip**. All 1,213 original and all 2,116 F01 pass identities remain passing. Host build succeeds. Four real Git/SQL/hook regressions prove metadata preservation, no repository-table lock deadlock, code/wiki isolation, and rollback/late-error fencing; neutral/core/model, checked refinements, PR-create completion and minimum-Git profile tests pass. Fresh independent review found the swallowed-launch-error hole, then confirmed the fix and no further worthwhile issue. No existing tests were removed or rewritten. PostgreSQL/MySQL-specific execution remains unqualified at this stage. Evidence: `.artifacts/untangle-refactor/f02/`, `f02-lane-a/`, `f02-real-rename/`; independent review retained under `f02/independent-review.md`.

- F02 commit: `28ee7787ea`.
- **F03 completed:** one private admitted mount descriptor and shared template partial now serialize page/panel roots; one parsed complete identity drives both retained forms, with extension-scoped owner tuples and a version-only comparison. Repository display facts and owner patterns refresh without changing identity. Each owner receives only its own declared global root/subroutes; production classification no longer uses the legacy workspace helper, whose export and output remain compatible. All scopes compose over SDK-supported routes; repository feed keeps its existing visibility and contribution guards. Exact linked-owner API structure replaces substring acceptance. Go and browser import one finite auth/credential/opaque-byte policy while retaining separate URL validation.
- **F03 bugs fixed:** changed page entry/API/asset/prefix/owner definitions retire stale code instead of adopting it; a foreign extension's matching owner label cannot suppress a page; unrelated owners no longer inherit each other's direct routes. Browser opaque captures now measure decoded UTF-8 bytes consistently with Go; malformed deployment prefixes and encoded auth/private paths cannot bypass their confinement checks. Fresh review caught repository display data arriving after descriptor construction, including ordinary native renders; the render-time descriptor projection preserves that behavior without coupling context rendering back to the router.
- **F03 verification:** full SDK **171 pass**, affected extension-host package **168 pass**, full frontend **517 pass**, browser TypeScript, focused ESLint and host build succeed. All original SDK/frontend pass identities and all 152 previously passing extension-host identities remain passing. Two existing browser tests required narrow structural adaptation: the source assertion follows the shared template partial, and the initial shared-owner fixture declares its route on that owner. No tests were removed; optional test cleanup remains skipped. Fresh independent review confirmed no worthwhile unresolved F03 issue. Evidence: `.artifacts/untangle-refactor/f03/`, including `comparison.json` and `independent-review.md`.

- F03 packaging follow-through: the Docker frontend stage now receives the shared navigation policy JSON imported by its browser entrypoint. Read-only F05 preparation caught the missing stage input; no application behavior changed. Actual production asset/image qualification remains part of the final gates.

- F03 commit: `c05d45fefa` (includes the Docker frontend policy input).
- **F04 completed:** v17d, v17e and v17g each own an independent, complete migration-local desktop-grant record with its original table mapping. Their field types/tags match Grant at `778e08aa98`, `1a29a5e86e` and `ad61c8ff39` respectively; historical replay no longer imports the mutable authorization model. The records preserve all original indexes, defaults and legacy blank proof/kind semantics. The historical ORM column spelling `proof_sh_a256` is retained.
- **F04 verification:** new fresh/sequential/pre-v17e/pre-v17g upgrade and replay regressions pass on SQLite and real PostgreSQL **17.11** (four pass events on each engine). They check exact columns, widths where enforced, nullability, defaults, primary key, all three indexes, active/revoked rows, existing proof and timestamps. The full migration package has **34 pass / no failures / one existing PG-only skip** in its SQLite run. Independent history/schema/consent review found no meaningful defect. New test queries name the actual column list to avoid pgx's cached SELECT-star result shape across ALTER; production migration behavior was unchanged. MySQL execution remains unqualified. Evidence: `.artifacts/untangle-refactor/f04/`.
- Additional database qualification: all existing model/service `OnPostgres` native ownership/admission/authority checks now pass against the task-owned disposable PostgreSQL 17.11 fixture (**39 pass events, no failures/skips**). This closes the earlier missing-server limitation for those recorded checks; it does not claim every database profile has run. No existing service or database was used.
