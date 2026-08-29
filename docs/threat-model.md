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
  available to the local Vastora Agent and the restricted deployment helper;
  the Center process cannot access either Docker or the Admin socket.
- Application Web ports bind to the confirmed private service address rather
  than every host interface. A public address is accepted only when Agent finds
  it on a local interface and an administrator explicitly enables direct ingress.
- When suggesting a VLESS node region, Center sends only the selected Gateway's
  confirmed public IP address to `api.country.is`. No credentials, node secrets,
  private addresses, or application data are included. The lookup is optional;
  failure leaves the searchable manual region selector available.
- LAN and Headscale Web entries use selected Caddy Gateway nodes. They may use
  HTTP inside the private network, or a browser-trusted certificate obtained
  through Cloudflare DNS-01 without exposing the service publicly. Public Web
  entries require HTTPS. Caddy Admin remains reachable only over its Unix socket.
- ACME account keys and private HTTPS certificates are encrypted in Center.
  Certificate keys are absent from desired-state JSON and task-event records,
  delivered only with a claimed Gateway task, and encrypted at rest by Agent.
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
- Bundled Headscale is public through Caddy HTTPS because new nodes need a
  reachable coordination server. Center has no public DNS record or public
  Caddy route. The Headscale hostname forwards only the exact Agent installer
  path to Center; enrollment tokens remain ten-minute, single-use credentials.
- Bundled Headscale advertises its authenticated embedded DERP relay plus a
  static `STUNOnly` entry for `stun.cloudflare.com:3478/udp`. It never downloads
  Tailscale's public DERP map and never uses Cloudflare TURN or DERP. Cloudflare
  may observe a STUN probe's source IP, source port, and timing, but no
  application traffic crosses Cloudflare through this integration. Headscale
  performs no update check and keeps Logtail and client auto-update instructions
  disabled. Vastora-managed Linux clients run `tailscaled` with
  `TS_NO_LOGS_NO_SUPPORT=true`, so private-network operational logs are not
  uploaded to Tailscale. Before starting or restarting the daemon, Agent pins
  the verified bundled Headscale address (including active domain aliases) in
  a Vastora-owned resolver section and removes malformed or Tailscale-hosted
  DERP cache data without touching `tailscaled.state` or node keys. Public
  application traffic and explicitly configured public integrations remain
  outside this private-network isolation boundary.
- A fixed `public-ipv4:41641` Tailscale endpoint is never inferred from HTTP,
  request headers, an external egress-IP service, or the Center's public 80/443
  probe. The administrator must enable it explicitly and confirm a reserved
  IPv4 plus UDP mapping. Stale local addresses stop desired-state publication.
  Only the single active, co-located Agent that reports Vastora-managed
  Tailscale can receive it; user-managed clients are excluded. Agent writes only
  dedicated Vastora-owned files, verifies the pinned daemon version and health,
  and restores the previous files if the restart fails.
- An external Headscale deployment is operator-controlled and must enforce an
  equivalent DERP and logging policy before it can satisfy the bundled
  deployment's private-network security boundary.
- Tailscale's macOS GUI clients remain outside the strict zero-external-telemetry
  boundary because they do not support the daemon logging opt-out. Such clients
  require the open-source CLI daemon or an operator-enforced outbound allowlist.
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
Vastora manages Cloudflare Access only for the optional Center browser fallback.
It creates the Access application before the proxied DNS record, limits entry to
an exact email or email domain through one-time PIN login, and keeps Center's
own administrator authentication as a second layer. This policy does not cover
Agent APIs or application publications. Vastora does not manage host firewall
rules. Publishing an application management page publicly therefore still
relies on the application's own authentication and an explicit administrator
confirmation.
