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

`TestNativeDesktopEnrollment` exercises the actual S256 exchange, read-scoped
enrollment/replay, repository read, withdrawal, subsequent read refusal and
inactive readback using fixture accounts and PostgreSQL. This is component
evidence. Real desktop login/custody, central placement, operation journals,
candidate transfer, withdrawal races and installed qualification remain open.
