# Native desktop enrollment

Read enrollment and privileged controller authority are separate capabilities.
Read enrollment admits no execution or publication and never exposes the
private extension callback protocol.

Use a registered **public** OAuth client with S256 PKCE and a current bearer
authorization. No client secret is needed. The server operator supplies the
public client ID and allowed loopback redirect. Set `[oauth2]`
`FACTORY_DESKTOP_CLIENT_ID` to that existing public application and explicitly
enable `ENABLE_ADDITIONAL_GRANT_SCOPES`. Discovery stays unavailable otherwise;
it creates no OAuth application or grant. A `read:repository` token is
sufficient; the current account must also have code-read access to the repository.
The normal OAuth expiry, revocation and repository permission checks still apply.

The desktop discovers the selected client at `GET /api/v1/factory/desktop/oauth`.
After S256/loopback exchange, `GET /api/v1/factory/desktop/identity` verifies the
current immutable actor under that same scope and configured public application.
This endpoint grants no code read or execution authority.

Base path: `/api/v1/repos/{owner}/{repository}/factory/desktop/grants`.

| Method/path | Result |
| --- | --- |
| `POST /` | Enroll `command_id` and `runtime_id`, each 32 lowercase hexadecimal characters; new enrollment returns 201, identical replay 200. |
| `GET /{id}` | Read the current account/application/repository/runtime binding and active state. |
| `DELETE /{id}` | Withdraw this enrollment and retain its inactive record. |
| `GET /{id}/repository` | Read the bounded repository projection through an active enrollment. |

The SDK types are in `sdk/desktop_grants.go`. Account and OAuth authorization
identity come from the validated authentication result, never request fields.
An enrollment ID is metadata, not a bearer credential. Every use requires the
same current OAuth grant, repository and account. An unchanged command replay
cannot reactivate a withdrawn enrollment. The runtime ID identifies the selected
desktop; it is not a cross-machine lease or verified placement generation.

OAuth authorization may be shared by multiple devices. Native clients enroll
with `proof_sha256` (64 lowercase hexadecimal characters), the SHA256 of a
profile-held 64-character hexadecimal proof, and send that proof only in the
`X-Factory-Desktop-Proof` HTTPS header on enrollment and subsequent requests.
The server retains only its hash and returns `runtime_bound: true`. Replaying
metadata or a shared account token cannot replace the proof. This binding is
immutable, including after withdrawal. Legacy enrollments with an empty hash
remain read-only and return `runtime_bound: false`; they cannot qualify future
control-changing operations. The proof is not browser, Go-sidecar or SDK metadata.

Factory's Rust client derives this proof from its existing private profile root
under the separate `factory-fountain-desktop-enrollment/v1` HMAC domain, scoped
to origin/account/profile/application/repository/command. Native custody checks
the same credential revision for each bounded HTTPS call. No root or OAuth
credential is exported to Go or the UI.

`TestNativeDesktopEnrollment` exercises the actual S256 exchange, read-scoped
enrollment/replay, repository read, withdrawal, subsequent read refusal and
inactive readback using fixture accounts and PostgreSQL. Optional
`FACTORY_FOUNTAIN_NATIVE_CHECK`/`FACTORY_FOUNTAIN_NATIVE_ROOT` join the existing
native credential check through a private CA/server-leaf TLS fixture and isolated
OS keyring. That check covers enrollment/read, profile-key reopen, exact replay,
withdrawal and refusal; the host check also refuses missing-proof requests.
`FACTORY_FOUNTAIN_NATIVE_OAUTH=1` joins the actual configured-host native login
and encrypted credential owner: old-format migration, refresh, access expiry,
withdrawal during refresh I/O and a committed enrollment with a lost reply.
`FACTORY_FOUNTAIN_NATIVE_UI=1` uses an explicit installed-app runner instead,
with real keyboard/browser controls, original-command readback, refresh,
Quit/reopen and irreversible withdrawal. The runner is supplied through the same
optional check variables; it is not installed by the host or an execution grant.

## Explicit controller authority

Set `FACTORY_DESKTOP_CONTROL_CLIENT_ID` to a **different** registered public
application with the same loopback/S256 requirements. Fountain retains one scope
per account/application grant; control consent must not replace the existing
read authorization. `GET /api/v1/factory/desktop/oauth/control` advertises
`write:repository` only when this client and `FACTORY_APPLIANCE_PACKAGE_ID` are
configured. Factory keeps each application's credential and connection separate.
Configuring the appliance package opts its private callbacks into central
admission: unscoped old writes and the legacy signed issue-intake callback refuse.
Other extensions and ordinary human writes retain their existing permissions.
Do not enable unattended work until the controller-aware appliance/worker
consumer is integrated and qualified; that integration remains unfinished.

Paths below are relative to `/{enrollment}` at the enrollment base path.

| Method/path | Result |
| --- | --- |
| `GET /placement` | Central repository controller, generation, revision and active/retiring/inactive state. This observation is not a dispatch lease. |
| `POST /placement` | Finite admin command (`command_id`, `expected_revision`, `action`, `not_after`); activate, withdraw or finish retirement. Exact applied/refused receipts are retained in the existing native operation journal. |
| `POST /operations` | Separate immutable finite operation grant; binds current control OAuth grant, native profile proof, account, repository, selected runtime/generation and named operations. |
| `GET, DELETE /operations/{grant}` | Observe or withdraw the grant. Exact replay cannot reactivate it or change its deadline. |
| `GET /native-revision` | Existing global native mutation revision, idle/inhibited state; it is separate from the repository controller. |
| `POST /operations/{grant}/submit` | Existing conditional native effect, with exact `controller_grant_id` and authenticated installation/actor/repository. |
| `GET /operations/{grant}/journal/{operation}` | Original effect receipt; absence never permits another send. |
| `POST /operations/{grant}/claims` | Reserve one exact external native execution manifest. Only first admission reports `acquired: true`; replay cannot authorize another launch. |
| `GET /operations/{grant}/claims/{claim}` | Read the exact original claim, including after expiry/withdrawal, with `acquired: false`. Missing history is 404 and never authorizes another reserve or launch. |
| `POST /operations/{grant}/claims/{claim}/retire` | Owning profile's exact retirement/no-start receipt digest. Neither expiry nor lookup absence settles an unknown run. |

Selection does not authorize operations, launch a worker or fund inference.
Withdrawal stops new admission. Retirement waits for claims and native effects
to settle; no competing runtime takes over an uncertain outcome. Repository
placement uses its own row gate, never a controller-lifetime global reservation.
Actual SQL primary commits and Git prepared admission recheck current controller
authority within their existing transactions. Read-only enrollment, old command
deadlines and today's login cannot confer historical authority. Reauthorization
under the same application/account and original native profile proof permits
history lookup and retirement; new effects still require the **original** OAuth
grant to remain valid.

`TestNativeDesktopEnrollment/FiniteControllerAuthority` exercises real OAuth,
PostgreSQL selection/replay/refusal, separate finite grants, a journaled comment,
claim gating, withdrawal/expiry and provider revocation. Optional
`FACTORY_FOUNTAIN_CONTROLLER_CHECK`/`FACTORY_FOUNTAIN_CONTROLLER_ROOT` use the
existing native credential check against TLS, preserving separate actual read
and control credentials, a lost initial selection reply, native reopen and exact
replay. The fixture supplies host-exchanged tokens over a private pipe; native
control consent discovery/URL is checked, but native control code exchange and
installed UI are not qualified by that mode. Claim probes do not launch workers.
Actual worker admission, appliance/desktop competition, canonical shared-view
consumer integration, candidate transfer and installed delivery remain open.
