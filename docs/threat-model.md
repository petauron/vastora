# Threat model

## Trust boundaries

- A Center administrator can configure applications and therefore is trusted
  with their startup secrets.
- An Agent administrator and the local Docker daemon can inspect a running
  container's environment and mounted secret files. Vastora cannot remove this
  host-root trust boundary.
- Catalog transport can be private or public; catalog integrity comes from the
  pinned Ed25519 public key, not transport alone.

## Controls

- Center secrets, catalog Bearer tokens, and registry credentials use
  AES-256-GCM with a root key stored outside SQLite.
- Catalog documents are bounded in size, decoded only after signature
  verification, and cached only after validation succeeds.
- The Center never mounts Docker's socket. The Agent deployment executor exposes
  only typed, catalog-declared application operations and no arbitrary Docker API,
  Compose input, or shell hook.
- Caddy never mounts Docker's socket. Its Admin API is a permissioned Unix socket
  shared only with the local Vastora Agent.
- Application Web ports bind to the confirmed private service address rather
  than every host interface. A public address is accepted only when Agent finds
  it on a local interface and an administrator explicitly enables direct ingress.
- LAN and Headscale Web entries use selected Caddy Gateway nodes. Public Web
  entries require HTTPS. Caddy Admin remains reachable only over its Unix socket.
- HAProxy is installed only for an explicit shared-443 Publication. It performs
  TCP ClientHello SNI routing without terminating TLS, uses no Docker socket,
  and binds only the confirmed public address. Unknown SNI traffic is passed to
  Caddy, which has no matching application route for unconfigured hostnames.
- Cloudflare and Headscale credentials are encrypted; list APIs return only
  configuration metadata. Connector tokens are delivered only to the selected
  Agent through authenticated, leased tasks.
- Headscale API requests can target only exact HTTPS origins authorized when
  Center starts. Browser administrators select from that operator-controlled
  boundary and cannot send the stored Bearer token to arbitrary network hosts.
- The unauthenticated setup endpoint creates an administrator only while the
  administrator table is empty; all later setup requests are rejected. Until
  setup finishes, the released deployment publishes Center only on server
  loopback and administrators reach it through SSH port forwarding. Custom
  deployments must enforce an equivalent initial-access restriction.
- The public root installer accepts only HTTPS release URLs, verifies the
  release archive against its published SHA-256 value, rejects unsafe archive
  entries, and installs a Center image pinned by its complete digest. Running a
  remote installer as root still trusts the official installer origin and
  release account; operators may download and inspect `install.sh` first.
- Center-generated Agent installers use ten-minute, single-use enrollment
  tokens. Node name, site, roles, capabilities, connection address, and optional
  Headscale bootstrap are bound to the token by Center; sensitive bootstrap
  material is encrypted at rest and omitted from the enrollment response. The
  installer and Agent binary both require that token. Binaries carry
  Center-provided version and SHA-256 headers and execute only after both
  integrity and version checks pass.

## Non-goals for v0.1

Vastora does not protect against a malicious root user on an Agent, operate a
quorum or leader election system, provide automatic cross-Site routing or
Gateway HA scheduling, provide multi-user RBAC, or collect telemetry by default.
The first version does not manage Cloudflare Access or host firewall rules.
Publishing an application management page publicly therefore relies on the
application's own authentication and an explicit administrator confirmation.
