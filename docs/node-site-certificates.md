# Managed certificates for node-local Daylight sites

Tracking: [Vastora #1128](https://github.com/petauron/vastora/issues/1128).

This opt-in administrator API manages an existing signed Daylight installation.
Center embeds Caddy's CertMagic library for issuance and automatic maintenance;
there is no second node-site ACME implementation, separate Caddy daemon or
Certimate service. Existing gateway certificates remain on their existing path.
This development change does not deploy to production or migrate proxy SNI.

## Configure one node

Install the signed `daylight` or `daylight-https` application normally, with one
exact administrator-controlled hostname and a private HTTP listener. It must be
running. Configure Center's existing Cloudflare OAuth integration with zone read
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
that only this Daylight service may restart. The page reports desired/applied
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
and candidate Daylight configurations. Treat these backups as sensitive; do not
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
It validates the candidate using the installed Daylight launcher. It preserves
page routes and the existing private/public listeners and adds a TLS 1.3/H2
origin on IPv4 loopback. Node-side automatic HTTPS is disabled for this opted-in
application; the explicit pair is loaded through its root-only configuration
and existing systemd credential. DNS-01 does not require public 80/443 on a node,
including IPv6-only or NAT nodes.

The encrypted journal is saved before atomic replacement. Only this Daylight
unit is restarted. Activation requires a real loopback handshake with the exact
hostname, fingerprint and H2. The probe trusts the authenticated issued leaf,
allowing isolated staging checks without claiming public trust.

Failure restores the previous configuration and restarts that same unit. Agent
startup reconciles any journal left `applying`; independent file changes are
preserved and reported for manual recovery. A lost response uses retained
execution evidence; an unknown execution is not silently replayed. Abandon the
reviewed uncertain attempt, inspect/rollback, and request a fresh revision.
Only one prior website configuration is retained per application. Native
Daylight program upgrades preserve this application-owned configuration.

Center schema 127 and Agent schema 23 use forward-only migrations and the
existing consistent recovery backup path. Production deployment, catalog
publication, proxy migration and public acceptance remain separate operations.

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

Runtime fixtures use temporary SQLite databases, an in-process TLS server and a
service-control adapter limited to the synthetic website unit. They do not run
Linux systemd or the packaged Daylight/Caddy binary, validate real Cloudflare DNS
or complete a public CA staging transaction. Those live checks remain necessary
before a production rollout. No production service, SNI, subscription, account
or traffic counter is changed by this worktree.

Local verification on 2026-10-30: all 12 top-level `TestNodeSite*` tests and the
focused migration/key-binding/certificate/OpenAPI regressions passed. The public
fixture privacy check and Linux amd64 build passed. With identical unstripped
build flags, the binary increased from 55,428,808 to 57,568,995 bytes relative to
this branch's pre-CertMagic implementation: +2.04 MiB (+3.86%). This is not the
compressed release-package size. Temporary comparison executables were removed.
Remote CI, release and live acceptance have not run.
