# Glivela composite routes (issue 32)

The domain implementation and reviewed runtime source live in
`petauron/glivela` (Go module `github.com/petauron/glivela`). Vastora owns only
storage, permission checks, immutable deployment, source authorization,
business DNS I/O, and applied-health publication. Do not recreate an address
selection policy in Center or deploy an independent mutable panel/daemon.

## Configuration and publication

An existing route grant may reference `ipv4EgressNodeId` in addition to its
primary `egressNodeId`. Both landings must be distinct from each other and the
entry, bound to independent authenticated private identities, and strictly
IPv6-only/IPv4-only respectively. The route has one client credential, one
existing account, and one quota watermark. Native and fixed single-exit grants
remain available; no subscription is converted automatically.

Both landing plans must authorize the exact entry identity in desired AND
applied state. Publication needs the entry's applied revision, the projected
artifact digest, and fresh transport observations reporting the correct
public exit families. Its deadline is the shorter of the two verified leases.
Cached subscriptions also discard an unavailable composite route without
removing unaffected native or single-exit entries.

Schema 111 adds the optional secondary foreign key and replaces the old
single-exit uniqueness constraint with exact-pair uniqueness. Existing rows
retain their IDs, credentials, state, quota watermarks, and snapshots. The
existing migration provider backs up before migrating and rejects downgrade;
the new migration regression must pass before this integration is merged.

## Runtime contract

The Glivela-generated artifact includes one content-addressed routing Lua file.
Its name binds its contents into the config digest. Agent stages it durably
before atomic config replacement, rejects altered/symlinked/writable files,
and verifies them again on observation and restart. Old files still referenced
by an applied config are not removed during staging.

The reviewed Glivela target-selection runtime is required, NOT merely an
official Xray image with the same or a newer version. The Agent requires its
exact `GlivelaTargetSelectionRevision: 1` marker, and the reviewed core's
`run -test` initializes routing Lua without starting inbound listeners. Script
initialization and capability errors fail before the active config is changed.
It exposes the original destination
and directly pins one validated literal before SOCKS dispatch; no loopback,
second dispatch, duplicated traffic accounting, remote re-resolution, or
cross-exit retry is used. Existing fixed/native routes do not call these new
selection methods and retain their hostname recovery and protocol behavior.
An explicit native egress policy still applies only to native credentials. A
composite projection retains its installed, catalog-authorized extension image
instead of replacing it with the stock native-policy image. The Agent's signed
image authorization, artifact validation, and pre-switch capability checks are
all required; selecting a policy cannot bypass them.

The existing Agent serves business DNS on loopback `127.0.0.1:10086` only when
an applied/staged composite artifact needs it. It uses explicit public recursive
resolvers rather than control-plane/Tailscale DNS. AAAA success chooses public
IPv6; only confirmed NODATA permits public A. NXDOMAIN, SERVFAIL, timeout,
unsafe answers, CNAME cycles, malformed replies, and missing trustworthy
targets fail closed. TTLs include CNAME and negative SOA bounds; zero-TTL
answers are not cached. Sequential single-family core queries prevent a cache
generation change from being merged into an incorrect family selection.

Original public IP literals retain their family even when payloads contain a
different TLS SNI or HTTP Host. A client-recovered real hostname is accepted
as a domain request. A raw Fake-IP/reserved target is rejected even with an
arbitrary sniffed hostname; this runtime has no trusted server-side FakeDNS
mapping. HTTPS payload/SNI and certificate validation remain end-to-end.
TCP only: this change does not authorize routed UDP/QUIC or HTTP/3.
Only composite routing explicitly calls the managed DNS server through Xray's
existing per-server API. It is excluded from ordinary DNS fallback; no global
DNS script intercepts unchanged native or fixed-exit resolution. This integration
retains the current host runtime network contract; it does not turn an old bridge
runtime into a composite-capable one without the existing managed upgrade.

Routing metadata identifies the chosen outbound, family, and literal/AAAA/
NODATA selection reason; optional core info logs record only these safe fields.
The existing warning-level default is unchanged. Selection errors are separate from the core's SOCKS
and TCP errors; HTTPS acceptance records TLS separately from HTTP responses.
The production proxy does not decrypt HTTPS or classify HTTP status codes.
Never store credential material, raw config, or full subscription URLs in
diagnostic evidence.

## Dependent review order

1. Review/merge the Glivela domain, UI, and runtime source changes.
2. Pin Vastora's Go module to that actual immutable Glivela commit. Do not
   invent a pseudo-version or commit a local `replace` fallback. During joint
   development use an external `go.work` with the two checkouts.
3. Review this host integration independently; run migration, authority,
   snapshot, DNS, and immutable-file regressions plus the controlled core lab.
4. Separately authorize an immutable attested runtime image and signed catalog
   publication. Do not rewrite historical signed catalog fixtures. Program
   releases, application catalog publication, and installed upgrades remain
   separate operations.

No source change in this issue authorizes release or production deployment.

## Controlled runtime acceptance

`TestCompositeRuntimeLab` requires `GLIVELA_COMPOSITE_LAB=1` and an absolute
`GLIVELA_XRAY_BINARY` built from Glivela's pinned runtime Dockerfile. Without
the opt-in it explicitly skips, which is NOT acceptance success. With opt-in,
missing/unsupported binaries fail. Compile the Agent test binary, then run it
on Linux in a disposable network namespace with only loopback enabled; this
prevents accidental direct traffic to public-shaped fixture addresses.

After the domain module is pinned and an audited Linux core is built, invoke
`sh scripts/run-composite-runtime-lab.sh /absolute/path/to/audited/xray`.
The opt-in runner compiles the Agent test and creates a fresh network namespace
with only loopback enabled. It does not modify the host's routing/firewall or
any production service. Root permission is limited to namespace creation and
test execution; generated identities and files are temporary and never uploaded.

The lab uses the exact generated scripts and counters, actual Xray server and
client processes, Agent DNS, independent SOCKS peers, and certificate-verified
IPv4/IPv6 TLS targets. Only listener addresses, private transport endpoints,
and the test-only VLESS security envelope are rebased. It checks selected-peer
observations, target socket family, literal-plus-SNI behavior, CNAME, failure
without alternate exit, TTL expiry/family changes, HTTP 401/403/404 without
retry, restart, and user-counter equality with transported payload bytes.
It is NOT full production REALITY/HAProxy, kernel landing/firewall, or public
provider acceptance. Existing REALITY, source-identity/kernel lease, deployment
and quota tests remain mandatory too. Do not equate config validation with
target reachability, or a TLS success with a successful HTTP application result.

Only concise sanitized pass/fail summaries may leave the disposable lab. Do
not upload test binaries, generated credentials, source workspaces, configs,
screenshots, or build records as Actions artifacts.
