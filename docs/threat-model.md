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
- Catalog source URLs cannot embed userinfo credentials; private catalog access
  uses only the separately encrypted Bearer token field.
- Browser sessions are server-side and revocable. Session cookies are HttpOnly,
  both authentication cookies are SameSite=Strict, HTTPS requests receive
  Secure cookies whether TLS terminates at Center or its reverse proxy, and
  every authenticated browser mutation requires the matching CSRF token.
- Catalog documents are bounded in size, decoded only after signature
  verification, and cached only after validation succeeds. Every redirect must
  remain credential-free HTTPS, cross-origin redirects lose Authorization, and
  the number of redirects is bounded. Center permanently binds each
  source/app/version identity to its first canonical manifest digest; source
  deletion or key rotation cannot reset that history. Refresh failures preserve
  the exact last verified cache.
- The Center never mounts Docker's socket. The Agent deployment executor exposes
  only typed, catalog-declared application operations and no arbitrary Docker API,
  Compose input, or shell hook.
- Caddy never mounts Docker's socket. Its Admin API is a permissioned Unix socket
  available to the local Vastora Agent and the restricted deployment helper;
  the Center process cannot access either Docker or the Admin socket.
- Application Web ports bind to the confirmed private service address rather
  than every host interface. A direct-public address and its local bind address
  are accepted only from the Agent-confirmed network profile after an
  administrator explicitly enables direct ingress; explicit NAT mappings keep
  the public and local addresses distinct.
- When suggesting a VLESS node region, Center sends only that node's confirmed
  public IP address to the explicitly configured region service. No credentials,
  node secrets, private addresses, or application data are included. The
  official package currently configures `api.country.is`; private packages may
  replace or disable it, and the searchable manual region selector remains
  available.
- LAN and Headscale Web entries use selected Caddy Site Gateway nodes. They may use
  HTTP inside the private network, or a browser-trusted certificate obtained
  through Cloudflare DNS-01 without exposing the service publicly. Public Web
  entries require HTTPS. Caddy Admin remains reachable only over its Unix socket.
- ACME account keys and private HTTPS certificates are encrypted in Center.
  Certificate keys are absent from desired-state JSON and task-event records,
  delivered only with a claimed Gateway task, and encrypted at rest by Agent.
- HAProxy is installed on each VLESS node when its explicit node-direct
  shared-443 Publication is created. It performs TCP ClientHello SNI routing
  without terminating TLS, uses no Docker socket, and binds only that node's
  confirmed local receive address. A REALITY route can target only the same node's
  3x-ui alias; cross-node VLESS relaying is rejected. A VLESS-only node rejects
  unknown SNI and does not install Caddy. Only a node separately selected as a
  Site Gateway sends unmatched SNI to that node's local Caddy.
- One global 3x-ui controller owns browser management, clients, subscriptions,
  and encrypted restore points for every Site. Cross-Site control requests use
  only the confirmed private Agent service addresses; public REALITY traffic
  still terminates on the owning VLESS node. During the Alpha forward migration,
  each legacy controller is backed up and converted separately. A failed backup,
  demotion, or node attachment pauses the sequence for an explicit retry, and
  its obsolete public panel and subscription entries are retired only after the
  host and its running former workers have joined the global controller.
- Managed VLESS+REALITY fallback traffic requires an administrator-approved
  `.com` target. Agent pins one resolved IP, rejects cdncheck CDN/WAF matches,
  verifies TLS 1.3, X25519, H2, SNI, and the certificate, and pins REALITY
  directly to that address on port 443. Node-local HAProxy permits only the
  verified outer SNI and sends Proxy Protocol v2 to the local 3x-ui inbound;
  VLESS-only nodes reject unmatched SNI. Any missing, stale, or failed guard
  blocks Center publication and leaves the inbound disabled. Upgrades remove
  obsolete loopback guard inbounds and reserved Xray rules after read-back.
  Invalid REALITY clients may still reach the one approved
  camouflage IP and consume traffic, which is an intentional REALITY property.
  ASN is only a network-selection hint: mismatches and failed lookups do not
  reject a target or unpublish a ready service. SNI is not authentication;
  passthrough cannot inspect the encrypted HTTP Host or ECH inner ClientHello.
  A fixed IP and an outer SNI allowlist alone do not prove CDN tenant isolation,
  so known shared CDN/WAF targets and unavailable safety checks remain rejected.
  A dataset non-match is not proof that all shared hosting is absent. This
  control does not claim zero unauthenticated traffic or volumetric DDoS protection.
- The administrator-initiated REALITY behavior check is not an arbitrary scanner:
  Center accepts only a managed Publication id, re-derives its current approved
  public IPv4 address and fixed port 443, and uses a fixed five-probe set. Probes
  are concurrent, deadline-bound, and globally serialized. Results contain only
  finite status and reason values; certificates, response bodies, credentials,
  and socket errors are not stored. A successful unauthorized TLS identity is
  `affected`; any timeout, interrupted probe, unavailable expected fallback, or
  revision change cannot become `safe`. A co-located Center check is labelled
  `same_host` because it does not prove behavior from an independent network.
- Cloudflare and Headscale credentials are encrypted; list APIs return only
  configuration metadata. Connector tokens are delivered only to the selected
  Agent through authenticated, leased tasks. Enrollment registers an Agent
  X25519 identity and pins the Center TLS trust anchor; every full task payload
  is additionally encrypted to that Agent with a fresh ephemeral key. Envelope
  authentication binds Agent, task, and attempt. Strict request/response limits
  reject overlong bodies instead of accepting truncated JSON, persisted task
  errors redact credential-shaped material, and an encrypted local result
  outbox makes disconnect and restart acknowledgement replay safe. Immediate
  credential revocation is separate from workload-aware node disable.
- An application Cloudflare Tunnel connector is not a Site Gateway. Center
  accepts only HTTP/HTTPS Services, validates cross-node private reachability,
  and sends cloudflared directly to that origin. Existing Tunnel migrations
  keep the former Caddy route until the replacement connector revision and
  external hostname probe succeed, then remove it through desired state.
- CPA uses separate logical management and client API Services on the same
  private origin. The management Service follows the normal private or
  Cloudflare Access-protected Web publication policy. The client API Service
  can only use a Cloudflare Tunnel publication whose native ingress rule
  matches `^/v1(/.*)?$`; all other paths reach the terminal 404 rule. This API
  hostname intentionally does not use browser-oriented Cloudflare Access and
  instead fails closed unless an unauthenticated `/v1/models` probe receives
  CPA's `401 Unauthorized`. The hostname is not a secret; every API request
  still requires the independently generated CPA client key.
- CPA management and client credentials are generated independently with the
  Center cryptographic token facility. They are removed from the Catalog form,
  encrypted in deployment state, preserved unchanged during ordinary upgrades
  and configuration changes, and delivered only inside the Agent's sealed task
  payload. CPA's routine timezone comes from the selected Agent's Site.
- The application credential endpoint is shared by CPA and the 3x-ui controller
  but remains application-aware. Every reveal reauthenticates the current Center
  administrator, returns only that application's current fields with
  `Cache-Control: no-store`, records a secret-free audit event, and the browser
  clears its temporary values when the sheet closes. Application lists,
  diagnostics, task events, assistant context, tool results, and approval
  payloads never include these values.
- CPA management-key and client-key rotations are independent durable operations
  bound to an administrator and idempotency key. The generated replacement is
  encrypted while pending and deleted from transient rotation storage after the
  successful CPA deployment becomes the current encrypted source of truth. A
  management rotation configures CPA before Keeper with exactly the same value;
  CPA failure is closed, while a failed dependent update remains visibly
  `action_required` and can retry without generating another replacement. The
  browser polls only this non-secret operation state, so it does not retain the
  administrator password while waiting for the Agent.
- The embedded Assistant can preview a CPA credential rotation and create a
  high-risk proposal containing only application identity, target credential,
  impact, and resource revision. It cannot execute the rotation until the exact
  proposal is approved through the trusted approval control, and neither model
  tools, proposal records, events, nor audit payloads receive the old or new
  credential value. Free-form chat input is inspected before a message or run is
  persisted; known credential formats, sensitive assignments, and suspicious
  opaque values are rejected before model delivery. This input gate is defense
  in depth for accidental pastes and does not replace the structural exclusion
  of Center-managed secrets. Free-form chat remains supported: an arbitrary
  user-supplied password can be indistinguishable from ordinary text, so this
  gate does not guarantee detection of every secret. Accepted chat text may be
  saved in conversation history and sent to the configured model provider.
  Users must enter credentials through the trusted credential forms, not chat.
  The provider API key is used only for request authentication, never as model
  message content or tool data. Approval requirements are unchanged.
  Raw deployment, credential-rotation and node-sync diagnostics are excluded
  from assistant tools, conversation history, execution events and audit
  payloads; only finite error categories and execution state are exposed.
  Detailed diagnostics remain on the trusted application/node surfaces.
  Tool arguments are checked before persistence, including decoded string
  values, nested arrays, escaped strings and duplicate JSON keys. Conversation
  titles use the same accidental-paste gate as chat messages.
- Headscale API requests can target only exact HTTPS origins authorized when
  Center starts. Browser administrators select from that operator-controlled
  boundary and cannot send the stored Bearer token to arbitrary network hosts.
- Bundled Headscale is public through Caddy HTTPS because new nodes need a
  reachable coordination server. Center has no public DNS record or public
  Caddy route. The Headscale hostname forwards only the Agent installer, binary
  download, and host-cleanup result paths to Center. Enrollment tokens remain
  ten-minute, single-use credentials. Cleanup result tokens are separately
  generated for one task and attempt, are delivered inside the encrypted Agent
  task, and can only acknowledge that task after its private-network handoff.
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
  DERP cache data without touching `tailscaled.state` or node keys. Before the
  isolation state is marked applied, Agent reads the live map from the local
  Tailscale API: region 998 must remain STUN-only and region 999 must be the sole
  relay at the current bundled Headscale hostname. Any additional relay keeps
  reconciliation pending and prevents silent fallback. Public
  application traffic and explicitly configured public integrations remain
  outside this private-network isolation boundary.
- A fixed `public-ipv4:41641` Tailscale endpoint is never inferred from HTTP,
  request headers, an external egress-IP service, or the Center's public 80/443
  probe. The administrator must enable it explicitly and confirm a reserved
  IPv4 plus UDP mapping. Stale local addresses stop desired-state publication.
  Only the single active, co-located Agent that reports Vastora-managed
  Tailscale can receive it; user-managed clients are excluded. Agent writes only
  dedicated Vastora-owned files, verifies the minimum stable client/daemon
  versions, required configuration capability, loaded privacy settings and
  health, and restores the previous files if applying the configuration or
  verifying the restarted service fails. A compatible version does not grant
  ownership of a user-managed installation.
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
The optional Center browser fallback supports two explicit protection modes.
The recommended native mode sends Cloudflare Tunnel directly to Center's login page,
requires a hostname-bound Turnstile token on every public login, and enforces
persistent account and client failure counters with exponential retry delays and
a fifteen-minute lock after five consecutive failures. Counter keys use a
database-keyed digest instead of storing raw usernames or client addresses.
Turnstile is always
validated by Center; its secret is encrypted at rest, while the browser receives
only the site key. The alternative Access mode limits entry to an exact email or
email domain through one-time PIN login before Center authentication. Neither
mode changes the private Center address configured for Agents. Vastora does not
manage host firewall rules or
claim that login throttling prevents volumetric denial-of-service attacks.
