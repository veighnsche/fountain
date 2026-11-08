# Native desktop enrollment

This first gateway slice supplies a revocable repository-read enrollment. It
does not admit execution, select a central controller, publish a candidate, or
expose the private extension callback protocol.

Use a registered **public** OAuth client with S256 PKCE and a current bearer
authorization. No client secret is needed. The server operator supplies the
public client ID and allowed loopback redirect. A `read:repository` token is
sufficient; the current account must also have code-read access to the repository.
The normal OAuth expiry, revocation and repository permission checks still apply.

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
This is component evidence. User-facing desktop login, central placement, operation journals,
candidate transfer, withdrawal races and installed qualification remain open.
