# Glivela composite routes: official Xray MVP (issue 32)

Glivela generates the domain model, UI, subscription, and official Xray JSON.
Vastora owns storage, permission checks, source authorization, atomic deployment,
and applied-health publication. Neither repository maintains a custom Xray
core, routing Lua, runtime-file manifest, or Agent-side business DNS service.

## Configuration and publication

A grant references `ipv4EgressNodeId` alongside primary `egressNodeId`. Both
landings must be independently authorized in desired AND applied state, ready,
distinct from each other and the entry, and strictly IPv6-only/IPv4-only. It
keeps one client credential, existing account, and quota watermark. Native and
fixed single-exit grants remain available; no subscription converts implicitly.

Publication requires the applied entry revision/digest and fresh observations
of BOTH public exit families. The shorter verified lease bounds availability.
Cached subscriptions discard an unavailable combination without removing
unaffected native or single-exit entries. Schema 111's optional secondary key,
exact-pair uniqueness, identities and quota watermarks remain intact. Released
schemas use the existing backed-up, forward-only migration path, never downgrade.

## Runtime contract

Glivela renders built-in DNS `UseIP` and routing `IPOnDemand`. For each combo,
scoped IPv6 matching precedes IPv4. The corresponding SOCKS outbounds use
`targetStrategy: ForceIPv6` / `ForceIPv4`, so the final SOCKS destination is a
locally resolved address of the selected family. Local DoH uses explicit public
resolvers without entering proxy routing or a control-plane/Tailscale resolver.
There is no additional resolver process, custom capability marker, or patch.

Official Xray 26.7.28 supports these settings. The Agent retains signed image
authorization and an isolated `run -test` using the exact candidate image BEFORE
switching the active JSON configuration. Validation failure leaves the running
config unchanged. Existing landing authorization, kernel gates, family/public
target restrictions, receipts, and shared-account traffic accounting remain.
Explicit native family policy uses the existing official `EgressImage`, even
when combinations exist; custom images receive no policy-validation exception.

The MVP prefers IPv6 in the DNS RESULTS, using IPv4 when no IPv6 result is
available. It does not prove confirmed AAAA NODATA, pin routing and outbound
queries to a single DNS generation, or override official sniffing behavior to
preserve a pre-sniff IP. Existing HTTP/TLS hostname recovery stays enabled;
unrecovered reserved/Fake-IP targets are rejected. Landing restrictions also
protect the final address if a subsequent DNS lookup changes.

Routed UDP, management ports, private/reserved targets and unresolved results
remain blocked. The routed user's final catch-all prevents native/direct escape
and use on another inbound. No cross-exit connection retry, HTTP replay, nested
proxy, NAT64, or UDP/QUIC support is introduced. Configurations with no combo
retain their DNS settings; combinations enable shared built-in Xray DNS, also
used by any explicit native family-resolution policy.

## Dependent review order

1. Review/merge Glivela's official configuration refactor and updated tests/UI.
2. Pin Vastora to its actual immutable commit. Use an external `go.work` for
   joint development, not a committed `replace` or invented pseudo-version.
3. Review this dependent host change and explicitly run configuration, UI,
   migration, authority, quota, snapshot and official-core lab checks.
4. Separately authorize releases, signed catalog publication and installed
   application upgrades. No custom runtime image needs building or publishing.

Source changes alone do not authorize release or production deployment.

## Controlled runtime acceptance

`TestCompositeRuntimeLab` is opt-in via `GLIVELA_COMPOSITE_LAB=1` and an absolute
`GLIVELA_XRAY_BINARY` pointing to an independently verified official Linux core.
A skipped lab is NOT acceptance. Run only in a disposable namespace with
loopback; public-shaped fixture IPs must never dial external destinations.
After explicit verification is requested, the runner is:

```sh
sh scripts/run-composite-runtime-lab.sh /absolute/path/to/official/xray
```

The lab uses actual official Xray server/client processes, generated routing
rules and counters, a test-only DNS fixture, independent SOCKS peers, and
certificate-verified IPv4/IPv6 TLS targets. It rebases listeners, DNS/peer
addresses and the VLESS security envelope, not the generated selection rules.
It covers A-only/AAAA-only/dual-stack/CNAME, public literals, blocked targets,
DNS failures, no alternate-exit retry, HTTP 401/403/404, restart and single user
accounting. Fixture DNS caching is disabled for an explicit family-change case;
this is not proof of atomic DNS evidence or production TTL/cache behavior.

It does not replace production REALITY/HAProxy, landing firewall, lease, or
provider acceptance. Keep existing deployment/identity/quota checks. Never
equate parsing success with reachability or TLS success with HTTP success.
Only sanitized summaries may leave the lab; never upload binaries, configs,
credentials, source workspaces or build records as Actions artifacts.

Official references: [routing](https://xtls.github.io/config/routing.html),
[DNS](https://xtls.github.io/config/dns.html),
[outbound targetStrategy](https://xtls.github.io/config/outbound.html).
