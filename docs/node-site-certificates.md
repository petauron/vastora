# Managed certificates for node-local Daylight sites

Tracking: [Vastora #1128](https://github.com/petauron/vastora/issues/1128).

This opt-in administrator API manages an existing signed Daylight installation.
Center embeds Caddy's CertMagic library for issuance and automatic maintenance.
On each host Agent is the sole owner of one shared Caddy's complete configuration
and lifecycle. Daylight supplies verified page content, without a second resident
website server. Existing gateway certificates remain on their existing path.
This development change does not deploy to production or migrate proxy SNI.

## Configure one node

Install the signed `daylight` or `daylight-https` application normally, with one
exact administrator-controlled hostname and a private HTTP listener. The signed
recipe must require `shared-caddy`, with a manual web unit; older recipes require
the reviewed handover below. Configure Center's Cloudflare OAuth integration with zone read
and DNS write permissions. Choose an unused high loopback TLS port, independent
of the existing HAProxy/Xray/Caddy listeners.

The administrator API uses the existing session and CSRF protection:

- `PUT /api/v1/applications/{id}/node-site-certificate` with
  `{"hostname":"us1.example.com","port":24482,"caDirectory":"https://acme-staging-v02.api.letsencrypt.org/directory"}`
  starts staging management. Omit `caDirectory` for Let's Encrypt production.
- `GET` at the same URL returns desired/applied revision, state, fingerprint,
  expiry, selected CA and a bounded error message. It returns no key or token.
- `POST /api/v1/applications/{id}/node-site-certificate/actions` accepts
  `{"action":"inspect"}`, `{"action":"rollback"}` or `{"action":"cancel"}`.
  Cancellation stops undelivered work and certificate management. A running or
  unknown execution requires the existing operator review first.

The states are `unconfigured`, `issuing`, `pending`, `ready`, `action_required`
and `rolled_back`. `ready` means the Agent confirmed this revision's actual
loopback TLS/H2 endpoint. It does not establish public website or proxy
reachability. Test those separately before changing SNI through Glivela. The
same-node REALITY target is `127.0.0.1:<port>`, never its shared public 443.

## Web management

Open an installed Daylight application's details and use **Center-managed
certificate**. The exact installed hostname is read-only; choose the loopback
port and production/staging environment, then confirm the recovery point and
website certificate update. The page reports desired/applied
revision, expiry, fingerprint and bounded failures. Inspection, pause and
rollback use the same administrator API; pause and rollback require an explicit
confirmation. Opening the page only reads state and never starts issuance.

## Keys and authorization

As explicitly selected for the MVP, Center generates and retains one independent
private key and exact-name certificate per node site. There is no shared
wildcard key. A compromised node can expose its own certificate key; it receives
neither other nodes' keys, the DNS token nor the ACME account credentials.

CertMagic assets, including account keys, certificate keys and renewal metadata,
are authenticated and encrypted in Center's SQLite database with its existing
root key. Only coordination locks use CertMagic's filesystem locker. The desired
pair is also retained as encrypted delivery material, so a later renewal cannot
change the contents of an in-flight revision. Ordinary queued task records
contain only the action. Claiming a task verifies the exact application, Agent,
hostname and revision, then hydrates the key into the existing sealed execution
and authenticated control channel. Public views, results and task events do not
contain private keys. OAuth token refresh uses Center's existing integration.

Center's consistent database backup and its original root key are required for
recovery. The Agent's existing encrypted journal and root key protect the prior
and candidate website contributions. Treat these backups as sensitive; do not
restore one node's identity onto another. No extra backup service is introduced.

## Issuance, renewal and delivery

`ManageAsync` and CertMagic's cache maintenance own issuance, certificate/key
persistence, retries, CA rate-limit handling, renewal and supported ARI windows.
The 15-second Center poll only reconciles managed certificates into Agent tasks;
it does not implement an ACME retry or renewal scheduler. A restart reuses
CertMagic's stored assets. An identical PUT is idempotent; a loopback-port change
reuses a valid pair. A newly managed certificate creates a new desired revision
only when no deployment or unresolved execution is in its way. Failures retain
the installed website and report an error rather than replacing working data.

The CA directory is administrator selected and allowlisted. The MVP accepts
Let's Encrypt production and staging as separate issuer/account storage
namespaces. Additional providers can be added through CertMagic issuer
configuration and the reviewed directory allowlist, without a new ACME client.
Changing the selected CA is explicit and does not replace the active website
until a complete new pair has passed Agent activation. No unverified future
Cloudflare CA endpoint, exemption from CA limits or live ARI acceptance is
claimed.

## Daylight activation and rollback

The Agent checks the installed signed package receipt, application ownership,
exact hostname, certificate validity and matching key before changing anything.
It reads `site/index.html` and the pinned Caddy binary from the receipt-verified
Daylight archive. Agent composes typed website contributions with the complete
gateway configuration; a page cannot submit arbitrary Caddy JSON or call `/load`.
It preserves existing private/public listeners and adds a TLS 1.3/H2 origin on
IPv4 loopback. The explicit pair is loaded by the shared configuration owner.
DNS-01 does not require public 80/443 on a node,
including IPv6-only or NAT nodes.

The encrypted journal is saved before the combined configuration load. Routine
content and certificate changes do not restart Caddy. Activation requires a real
HTTP response matching the package and a loopback handshake with the exact
hostname, fingerprint and H2. The probe trusts the authenticated issued leaf,
allowing isolated staging checks without claiming public trust.

Failure reloads the previous website contribution, retaining other sites,
certificates and protected system routes. Agent startup recovers only unfinished
journals; an applied site resumes through Caddy's persisted configuration without
replaying application tasks. Independent file changes are preserved and reported
for manual recovery. A lost response uses retained
execution evidence; an unknown execution is not silently replayed. Abandon the
reviewed uncertain attempt, inspect/rollback, and request a fresh revision.
Only one prior website contribution is retained per application. Daylight content
upgrades preserve its hostname, listener and certificate bindings. Uninstall
removes only the website contribution. Disabling the gateway retains Caddy while
protected system routes still require it. If managed websites remain, gateway
disable is refused before changing routes or clearing its recorded base; migrate
or uninstall those website contributions first. This prevents later website
updates from reconstructing a disabled gateway's previous application routes.

Center schema 127 and Agent schema 24 use forward-only migrations. The Agent
23-to-24 change reuses one consistent, key-verified recovery point while its
logical state is unchanged; a changed source requires review. Production deployment, catalog
publication, proxy migration and public acceptance remain separate operations.

## One runtime and first handover

Docker hosts reuse the managed gateway container and its persistent configuration
volume. Native-only hosts use `vastora-caddy.service` with DynamicUser, a private
Unix admin socket, 192 MiB memory and 128-task limits; no Docker is installed.
The binary is copied from the verified bundle into platform-owned storage so
removing the first website cannot remove another site's server. A manual
Daylight unit is retained as package ownership metadata and never starts Caddy.
Standalone Daylight remains independently usable outside this managed mode.

The shared recipe requires an updated Center and Agent, plus a new Daylight
release containing its page as a separate archive member. Do not add that file
to the existing 0.1.0 extraction list: it is absent from those released archives.
The catalog's `scripts/prepare-daylight-shared.mjs` prepares a new immutable
release's content recipe after release identity and download readback checks.
It does not publish the catalog or upgrade an installation.

For an old Daylight upgrade, prepare validates the complete signed content and
owned source before stopping its unit. One stable task journal retains the old
immutable program tree by rename plus the old unit/installation metadata and
certificate binding. An upgrade failure restores those exact owned resources;
unrelated files stop recovery for review. After the new site and registration
are accepted, only the verified retained program tree is removed. Existing
configuration and persistent data remain. A direct HTTPS site without an applied
Center-managed certificate is refused before replacement; complete that
certificate prerequisite before changing its server.

Adding a first Docker listener can require replacing the container because its
published ports are immutable. This is a separately planned transition, not a
zero-interruption promise. Later content/leaf updates reuse its bindings. A
public socket already owned by the node's HAProxy stays with HAProxy; website
traffic is forwarded to the same Caddy over the private runtime network.

The implementation uses Caddy's existing
[`--resume` persistence](https://github.com/caddyserver/website/blob/master/src/docs/markdown/command-line.md#caddy-run)
and the pinned runtime's
[certificate tag selection](https://github.com/caddyserver/caddy/blob/v2.11.4/modules/caddytls/certselection.go)
so an exact website pair is selected even when the gateway holds another
certificate for the same name. These are existing runtime features, not plugins.

Recovery needs the Agent database and original root key, the Center's managed
certificate state/key, and any unfinished handover resources. An application-only
file backup does not include the shared platform's encrypted website state.
Do not restore an old full database over live identity or traffic records merely
to undo a website. Subscription publication, proxy accounts, quotas and manual
maintenance state are outside this feature and are not modified.

## Dependencies and development verification

Pinned dependencies are `certmagic v0.25.6` (Apache-2.0) and
`libdns/cloudflare v0.2.2` (MIT), plus their transitive dependencies. The owner
approved their maintenance, security-update and binary-size costs. These are
runtime libraries, not a new container or service.

The focused `TestNodeSite*` suite exercises the real CertMagic library with a
synthetic issuer: obtain, encrypted storage, restart reuse, independent keys and
renewal. It also covers recipient/revision fences, immutable delivery material,
retained-result reconciliation, visible asynchronous load errors,
idempotent configuration/port changes,
Agent/Center migrations, pair validation, actual loopback TLS/H2 activation,
response replay, rollback and interrupted-journal conflicts.

Runtime fixtures use temporary SQLite databases, an in-process TLS/H2 server and
a bounded shared-admin API adapter. They do not run
Linux systemd or the packaged Daylight/Caddy binary, validate real Cloudflare DNS
or complete a public CA staging transaction. Those live checks remain necessary
before a production rollout. No production service, SNI, subscription, account
or traffic counter is changed by this worktree.

Historical verification on 2026-10-30, before shared-Caddy changes: all 12 top-level `TestNodeSite*` tests and the
focused migration/key-binding/certificate/OpenAPI regressions passed. The public
fixture privacy check and Linux amd64 build passed. With identical unstripped
build flags, the binary increased from 55,428,808 to 57,568,995 bytes relative to
this branch's pre-CertMagic implementation: +2.04 MiB (+3.86%). This is not the
compressed release-package size. Temporary comparison executables were removed.
The shared runtime was initially blocked by isolated-environment I/O. After the
environment recovered on 2026-10-31, the complete Agent package, focused Center
certificate/runtime/API tests, focused race checks, Go vet, Linux amd64 command
build and public-fixture check passed. No new dependency or test environment was
added.

The explicitly gated `TestSharedCaddyRuntimeLab` and
`TestSharedCaddyDockerLab` passed in an existing disposable Linux container using
the real pinned Caddy and existing official Xray. They verified standalone
Daylight handover, recovery of the actual previous website and owned receipt
after an injected failure, independent websites, certificate rotation and H2,
failed-load rollback, process/container reuse during content changes, persistent
configuration after restart, unrelated gateway routes, removal protection, and
an HTTPS request using a synthetic subscription through REALITY. Restored
automatic services must pass their existing endpoint health check before
recovery is reported; a running PID alone is insufficient.

These opt-in tests require root, a disposable-CT marker and explicitly supplied
public recipe/binaries. They refuse existing platform and fixture resources,
bind test ingress to loopback, use synthetic credentials, and remove only their
own services, users, files and containers. The gateway upstream binds only to the
container bridge. They do not contact a public CA or production subscription.
Remote CI, release, catalog publication and production acceptance for the shared
runtime have not run.
