# Independent official catalog distribution (#398)

Status: implementation and issue-scoped local verification complete; not
deployed. Production publication still requires operator-provisioned public
roots and a protected signing environment. This document does not authorize
production infrastructure or signing-key creation.

## Trust boundary

Use the maintained `github.com/theupdateframework/go-tuf/v2` client rather than
inventing a key-rotation protocol. A reviewed root metadata file must ship with
the program independently of the download origin. Never promote the existing
Center-generated catalog key into this root. The concrete origin, trusted root
and protected signing environment require operator confirmation.

The distribution origin is `https://downloads.petauron.com/vastora/catalog/`,
reusing the existing download R2 bucket identified by the operator. This is not
evidence of an uploaded catalog. Existing download objects must not change.
Use immutable versioned metadata and hash-prefixed targets below this prefix;
`timestamp.json` is the final mutable publication pointer. A directory prefix
is not itself a credential permission boundary: verify the hosting provider's
actual authorization scope before assigning publisher credentials. Do not
claim prefix isolation unless the credential or publishing service enforces it.

Official identity is the reserved source ID, anchored to that root, not an app
name, icon, URL or a key ID supplied by downloaded content. Third-party sources
continue to use their existing explicit trust configuration.

References:
- https://github.com/theupdateframework/go-tuf
- https://theupdateframework.github.io/specification/latest/

## Signed content and publication

TUF root, timestamp, snapshot and targets metadata provide authenticated role
rotation, expiry, rollback protection and consistent target hashes. Publish
immutable versioned metadata and hashed targets before atomically publishing
timestamp metadata last. Do not publish a detached signature and mutable JSON
as separately replaceable latest objects.

The catalog target additionally binds the reserved source identity, channel,
monotonic catalog revision, generation time and expiry. All are inside the
hashed target. Channels have distinct target paths and pinned expectations.
TUF verification is necessary but does not replace these application checks.
Images remain digest-pinned; native artifacts retain platform and SHA256 checks.

## Persistent acceptance

Store accepted metadata, the exact target hash, highest catalog revision and
last trusted observation time separately from deletable source records. Commit
the new catalog and its acceptance state in one transaction after all checks.
Authenticated TUF role updates are separately checkpointed when a later step
fails: a valid root revocation must not be forgotten because an app's download
or contract validation failed. These checkpoints never replace the displayed
catalog, advance its revision, extend its expiry, or authorize a first install.
Revision zero records an authenticated root before any target is accepted.
Reject older revisions and same-revision different content. Existing per-app
version-content immutability remains mandatory.

Do not advance expiry from a 304, successful transport, restart or source
recreation. On failure preserve the last verified cache, reporting the error.
Expired cached content is display-only: new install and upgrade authorization
must fail closed. Stop, uninstall and restoration of recorded installations
must remain available without downloading a fresh catalog.

An upgrade rejected at its first dispatch must not strand the installed
application in a pending state. Capture its confirmed running, failed or stopped
state in the deployment creation transaction, retain the existing pending lock,
and restore the snapshot only when rejecting a never-issued operation and the
application is still pending. Record the failed deployment, restoration and
terminal task event atomically. Already-issued work and explicit reconciliation
continue through their original recovery paths. The schema 72 migration gives
records without a captured state a conservative failed default, never a presumed
running state.

The apps screen loads catalog sources itself, alongside the app list. It does
not depend on a prior visit to Settings. Failed source-status requests clear old
source state and show retry guidance without preventing management of installed
apps; a successful refresh clears the error. Installation authorization remains
the responsibility of the server, not a source-status badge.

A local database rollback can also roll back remembered high-water marks. A
backup restore must enter a refresh-required state before authorizing new
deployments; do not claim complete rollback detection from the restored DB
alone. Clock regression against persisted observation time must fail closed.
First install requires a valid shipped trust root and unexpired verified
metadata; an attacker can freeze metadata within its validity window. Expiry
limits this window, but cannot solve a compromised clock or all-key compromise.

## Executor contracts

Replace exact application-version equality only after explicit contracts exist.
An application version is not an executor capability. Known app ID + supported
contract + typed permission/config/service/artifact validation authorizes an
executor; unknown contracts require a program upgrade. Downloaded declarations
cannot expand permissions, host paths, commands or installation semantics.

Installed manifests retain their recorded provenance for recovery and removal.
Refreshing the store never queues upgrades or changes running applications.

## Implementation coverage

Checked items describe the implemented requirements. The verification scope
and deployment boundary are recorded separately below.

- [x] Integrate go-tuf v2 with bounded HTTPS fetching and independent root.
- [x] Add signed target identity/channel/revision/time validation.
- [x] Add forward-only migration for non-deletable acceptance state.
- [x] Replace production official seed/self-signing path and exact-version gates.
- [x] Enforce cache validity at task creation and initial task dispatch.
- [x] Surface trust, revision, refresh time and expiry in API/UI.
- [x] Add protected, independent catalog publication and verification workflow.
- [x] Add tampering, replay, rotation, expiry, 304, clock and recovery tests.
- [x] Demonstrate an application-only version update without rebuilding agents.
- [x] Update release documentation, including #6's bundled-catalog wording.

## Verification audit

Local targeted verification was explicitly authorized. On 2026-09-12 the
operator also authorized the default Go cache access; the prior environment
blocker was resolved without changing cache locations or directory permissions.

Passed on the final implementation:

- `go mod tidy`; Go builds for `cmd/vastora`, `cmd/catalog-check`,
  `cmd/catalog-publish` and `cmd/catalog-verify`.
- Complete `internal/catalog` and all three catalog command test packages,
  including the real signer/publisher/independent verifier upgrade flow. This
  exposed and fixed the final-root expiry boundary: equality is expired.
- The catalog CI selector for Agent and Center, plus 61 Center lifecycle,
  migration, backup and task tests (29 subcases), and 28 Agent executor/native
  installation tests with their subcases. Three clock fixtures now start at
  the store's current time rather than simulating a historical clock rollback;
  two older fixtures were brought into line with existing CPA/controller
  contracts, without weakening production validation.
- The two affected Center command startup tests.
- Publication/signing/upload Node tests: 24/24; CI workflow policy and
  actionlint for the catalog and program release workflows.
- Six affected frontend test files: 176/176, including ten
  loader-to-AppStore cases and localized rejection guidance; TypeScript checks
  and the production frontend build. Portable catalog contract validation
  also passed (3 valid, 20 invalid, 43 boundary cases and signature/tampering).
- `go run ./cmd/catalog-check --catalog catalog/catalog.json --artifacts`:
  actual anonymous downloads verified native SHA256/ELF architectures and
  pinned OCI manifests/configs for linux/amd64 and linux/arm64. No image layers
  were run, no native payload was executed, and no service was installed.

This is not an all-repository or production signoff. An additional full
`cmd/vastora` test attempt on macOS hit existing Agent self-update tests that
pass the host's `runtime.GOOS` into a Linux-only platform validator; those
unchanged tests failed before reaching their HTTP mocks. The affected Center
startup tests passed separately. A localhost-only browser check could not
launch Chromium due to sandbox permissions; no browser QA success is claimed
and its temporary dev server was stopped.

| Requirement | Authoritative implementation / test evidence | Result |
| --- | --- | --- |
| Independent HTTPS update and expiry | `official_fetch.go`, `official_target.go`; real TLS fetch, replay, tampering, 304 and clock tests | Passed |
| Independently shipped root and authorized rotation | Image/Compose public root path; `official_root.go`; Go TUF rotation and revocation tests | Root/rotation tests and packaging policy passed; production root provisioning is an operator step |
| Restart/source lifecycle/restore replay floor | Schema 72; `store_official_catalog.go`, `backup.go`; migration/restore/checkpoint tests | Passed, including real production `Open` backup-first migration |
| Contract boundaries and immutable app versions | `official_contract.go`, `strict_json.go`, manifest history; permission/semantics/ambiguous JSON tests | Passed |
| Execution-time artifact identity/platform | OCI digest flow; Komari/Pulse SHA256 and ELF validation before mutation | Matching/mismatched install and upgrade tests and public artifact check passed |
| No automatic install/upgrade; explicit authorized update | `official_catalog_flow_test.go`: real signed TLS publication, authenticated API, real typed executor and filesystem install; only systemd commands simulated | Passed; this is not a live Komari daemon test |
| Failure cache retention and expired authorization | Store/dispatch gates; integration test offers a newer version before expiring it; configure/uninstall retain recorded state | Backend flow, task and frontend regressions passed |
| Rejected unissued upgrade preserves installed state | Deployment pre-dispatch snapshot and atomic terminal event; `official_catalog_claim_test.go` | Passed, including rollback and repeated claims |
| Unambiguous official identity and status | API source identity and trust fields; real apps loader-to-AppStore tests, Settings/installation sheet tests | 176 affected frontend tests and typecheck passed; no browser QA claimed |
| Approved atomic publication, retry, history and independent verification | Catalog workflows; signing job; immutable/CAS uploader; durable GitHub ledger; real signer/verifier CLI tests | Node/Go tests, actionlint and actual public artifact checker passed; no production R2 writes |
| Separate catalog/program/application releases | This document, release guide, packaging policy test | Documentation/code review; packaging policy passed |

A production GitHub/R2 publication has not occurred. Before deploying, the
operator must provision the reviewed public root chain and protected signing
environment, then publish the first catalog; this issue does not authorize
creation of production keys or resources. Local test keys are disposable
fixtures and must never be promoted to production trust.

## Offline publication command

`cmd/catalog-publish` prepares a repository without accessing object storage.
Provide independently approved root metadata and PKCS8 Ed25519 signer files
with owner-only permissions. The root role private keys are not needed by this
command; root rotations are a separate reviewed ceremony.

Example for a subsequent publication (paths are operator-provided):

```sh
catalog-publish --catalog catalog/catalog.json \
  --root /protected/root.json --previous /protected/publication-state.json \
  --history /protected/manifest-history.json \
  --revision 42 --channel stable --valid-for 168h \
  --targets-keys /protected/targets.pem \
  --snapshot-keys /protected/snapshot.pem \
  --timestamp-keys /protected/timestamp.pem --output /staging/catalog-42
```

Use `--bootstrap` instead of `--previous` and `--history` only for the explicitly approved first
publication. Multiple comma-separated key paths support role thresholds. The
output directory must be new; partial output from a failed command must never
be uploaded. The command checks the full executor contract and signer role
authorization before writing any output.

Persist `publication-state.json` in the protected publication ledger, not as an
unauthenticated source of truth obtained from the CDN. The hosting publisher
must serialize releases, reject overwriting existing immutable objects with
different bytes, verify the staged repository, and write `timestamp.json` last.
Keep previous root versions available so offline clients can verify rotations.
The protected workflow orchestrates this command; the command alone does not
constitute an end-to-end publication. See the release guide for its ledger,
retry, approval and production-provisioning requirements.
