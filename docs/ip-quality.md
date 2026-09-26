# IP quality diagnostics

The unified Meridian node list and exit chooser expose the same manual
node-diagnostics panel. The IP tab separates IPv4 and IPv6 and, when a host has
multiple addresses in one family, offers an exact-address selector. Landing
rows show the report for their configured exit; entry rows use their observed
native public egress. An exit without its own report displays pending detection.
The Network tab retains host network diagnostics; entry-to-landing route tests
remain separate.

Center accepts probe targets only from the Agent's observed public egress and
validated local exit-address inventory. NAT probes retain the distinction
between public target and local bind address. Every report must match the exact
requested public IP. Reports are stored independently by node and canonical IP,
so testing IPv6 preserves IPv4 results and two IPv6 exits never share a report.
Removing an address from the node's eligible targets makes its retained report
stale; changing the selected exit simply selects that exit's own report.

Meridian owns the on-demand diagnostic container on both entry and managed
landing nodes. The Agent only supervises its lifecycle and relays the bounded
result through the authenticated, encrypted execution channel. A managed
landing node does not need a separate Meridian Xray installation merely to
measure its own exit. The current task contract still requires a Linux Agent
advertising `ipQuality` and Docker. The `node.ip-quality` task uses a lease.
Only one check per node can be pending/running. Ordinary detection failures
complete the diagnostic with an error code; they do not become configuration
failures. Lost execution authority or unconfirmed cleanup still requires the
normal Activity workflow. Nothing automatically retries or starts a check.

## Runner and privacy

- Upstream: [xykt/IPQuality](https://github.com/xykt/IPQuality), AGPL-3.0.
- Script: commit `ad222ab16778be2a13a174cd1acbd69fb4cac6b7`, checksum pinned in
  `internal/agent/ip_quality.go`. References use the same revision.
- Dependency image: `xykt/ipquality@sha256:26258a197cc03c186d41c26bd871d36ccc6ee269baf97762cfdd2b7d694c8dda`.
  The mutable upstream entrypoint is not used.
- Temporary container: unprivileged UID, no host mounts, read-only root,
  dropped capabilities, host network, 128 MiB memory including swap limit,
  half a CPU, 96 processes, 32 MiB temporary filesystem, 210-second lifetime.
  Agent explicitly removes its exact container on
  all return paths. The entire operation, including image pull, is limited to
  four minutes. The fixed image remains cached for later manual checks.
- Privacy/JSON mode; no online report upload, statistics counter, advertisement,
  SMTP scan or bulk DNS blacklist scan. No speed/return-route test. Provider
  and streaming requests necessarily expose the tested exit IP to those sites.
- No raw report, coordinates, command, upstream response or report URL is
  retained. The authenticated Center stores the latest normalized report per IP,
  bounded ASN/organization/location fields, provider classifications and risk
  factors, provider-specific risk values, unlock status/region/type and
  completion time.
  Missing values are not zero. The UI preserves provider-specific evidence
  alongside the explicitly versioned Meridian selection policy described below.
- Schemas 78, 80 and 99 use Center's forward-only migration backup/fail-closed
  flow. Schema 99 preserves existing reports and task ownership while changing
  the diagnostic key to node plus IP. Agent deletion cascades its diagnostics.
- All IP-sensitive upstream self-lookups use the requested local binding,
  including DB-IP. Reference-file downloads do not contribute exit evidence.

## Meridian exit suitability v2

`meridian-v2` is a product selection policy, **not** an industry standard,
fraud probability, or network-performance measurement. NodeQuality/IPQuality
supply evidence, not this formula. The score is 1–100, higher is better.

| Component | Weight / rule |
| --- | --- |
| Usage type | Residential 20, mobile 17, business 10, hosting 5 |
| Provider risk | Scamalytics 10, IPQS 10, AbuseIPDB 5 |
| IPPure | 25 |
| Unlocks | ChatGPT 15, Netflix 6, Disney+ 4, YouTube 2, Prime Video 2, TikTok 1 |

Known 0–100 risk scores contribute `weight * (1 - risk / 100)`.
[Scamalytics](https://docs.scamalytics.com/ip-fraud-risk-api/v3/),
[IPQS](https://www.ipqualityscore.com/documentation/proxy-detection-api/response-parameters),
and [AbuseIPDB](https://www.abuseipdb.com/faq) measure different risks: this
weighting is a preference, not a calibrated ensemble probability. AbuseIPDB 0
cannot establish residential type. Other provider scales remain display-only.
Repeated proxy/VPN/Tor/abuse flags are shown without an additional deduction.

Type requires at least two different providers and at least two thirds of
recognized usage-type votes. Company/organization ISP classifications never
vote. Unknown categories do not vote. An unconfirmed type with at least two
recognized votes ranges over the observed types; with fewer votes it ranges
over all four. Caps: residential 100, mobile 95, business 89, hosting 79.
Apply the cap to the sum, round once, then clamp to 1–100.

Complete service availability earns its weight. No/originals-only earns zero.
Errors and unknown results retain a 0-to-weight interval. Country is optional;
a different country is unavailable for that preference and an unknown country
is missing. Native/DNS unlock method stays visible without extra points.

### IPPure acquisition and provenance

The official [API documentation](https://ippure.com/MyIP-Info-API) identifies
`https://my.ippure.com/v1/info`, returning the caller's `ip` and `fraudScore`.
The [official FAQ](https://ippure.com/faq) confirms higher means more risk and
explicitly excludes IPv6 risk calculation. Verified on 2026-09-25; the API is
labelled beta by the provider. No third-party alias is used.

After the pinned IPQuality check, the same Meridian-owned temporary container
binds an HTTPS request to the same local address, forces IPv4, disables
inherited proxies and redirects, and limits it to 12 seconds and 16 KiB.
Agent verifies the returned IP before relaying the bounded fields. The whole
check still uses its existing four-minute deadline. No key is needed.
Non-200/timeout, malformed responses, IP mismatch and IPv6 unsupported status
are retained as missing evidence, never as a zero risk score. Only the bounded
risk/residential/broadcast fields and provenance are saved; no raw response.

When Center accepts a freshly completed, IP-bound IPQuality report, it records
the batch completion time, measured IP and ok/missing status for every source
and service. This is collection time, not an upstream claim about database
freshness. Reports already stored without provenance stay partial until
manually rechecked. IPPure does not silently supply missing IPQuality
classification votes.

### Assessment and recommendation API

`GET /api/v1/ip-quality` returns eligible `targets` and saved `checks`, each
identifying `agentId`, canonical public `address`, `family` (`ipv4` or `ipv6`),
and whether it is the `selected` exit. Checks add `assessment`, including version,
status, score or min/max, grade, type evidence, four contributions, missing
items, required-service results, advice, preferences and expiry. Optional query
parameters: `required=ChatGPT,Netflix,DisneyPlus`, `region=US`, and
`compareNodeId=<agent-id>` and `compareAddress=<public-ip>`. An omitted comparison
address uses the source node's native public egress. Candidate landings always
use their selected exact exit and never borrow another address's report.
`POST /api/v1/agents/{id}/ip-quality` requires a JSON body containing `address`;
Center resolves its trusted local binding and rejects unrecognized targets.
Omitted required uses the three defaults; explicit
empty required selects no mandatory services. Duplicate/unknown service names
and invalid region shapes return 400. Preferences change advice and regional
unlock scoring, not the six fixed service weights.

Missing evidence is never reweighted. When IPQS is the only missing item,
the assessment also publishes a **conservative score** equal to the interval's
lower bound: IPQS contributes 0 of its possible 10 points. This is a decision
score, not an inferred IPQS value or a claim that IPQS found high risk. The
missing source and full interval remain visible. For example, a confirmed
hosting IP with 14.3 known provider points, 13.5 IPPure points and 30 unlock
points receives conservative score 63 and interval 63–73. A fresh valid IPQS
value restores a complete score. Other missing items remain provisional.

Bounds enumerate possible type points
and caps with minimum/maximum missing contributions. A capped equal interval
is still provisional when evidence is missing, except for the explicit IPQS-only
conservative rule. Old/IP-changed reports have no usable score or recommendation.
Reports expire after 24 hours; all contributing
observations must independently be fresh and IP-bound. This is an evidence
interval, not a statistical confidence interval. Grades are excellent 90+,
premium 80+, good 60+, fair 40+, poor below 40; other incomplete/expired is neutral.

A confirmed mandatory-service failure requests comparison even with other
missing providers. Otherwise missing/expired evidence requests a recheck.
Complete or conservative score >=60 with all requirements met supports direct
use; below 60 requests comparison. A complete or conservative candidate must
meet every requirement and either fix a current required-service failure or
have its lower bound exceed the current upper bound by at least 10. Conservative
candidates are listed separately from complete scores. Other incomplete
candidates never enter ranking.

Comparison checks source and destination readiness, private-network enrollment,
blocked/paused state, and existing source-to-egress health (Meridian route
grants or VLESS landing peers). A configuration-eligible new pair can be
recommended by IP quality before a route exists, but is labelled connection
pending verification. This never creates grants or changes routes. The actual
route still requires the existing runtime workflow before it can be used.

The compact IP summary expands into evidence. Landing comparison allows
service/country selection and separates complete, conservative and provisional
results. User selection only fills the route form. Network measurements remain
in their separate tab. Saved assessments refresh while visible to expire old
results without starting probes. Raw evidence uses the existing report JSON;
Center computes assessments without inventing missing historical evidence.

## Network quality and return route

The diagnostics panel has two tabs: IP and Network. The Network tab groups
carrier quality, return route, international bandwidth, and host/TCP readings
in a compact grid. Its three active network checks use independent task
classes: `node.network-quality`, `node.return-route` and
`node.international-bandwidth`. Each class permits at most one
pending/running task per node and retains only its latest bounded structured
result. Both use the same encrypted Agent execution channel, leases, explicit
error states and Activity recovery rules as IP quality.

The three carrier targets were migrated from the operator's existing Komari
configuration into Vastora target revision 1. Runtime execution does not read
Komari, its database or its token. A network check performs four bounded TCP
connection samples for China Telecom, China Unicom and China Mobile and reports
mean latency, successive-sample jitter and connection-loss percentage. It is
manual only and closes every connection immediately.

Return route uses the maintained `golang.org/x/net/icmp` implementation already
present in the project, with one bounded IPv4 ICMP probe per TTL and no reverse
DNS or third-party geolocation requests. It reports at most 30 structured hops
for node to carrier target. It never labels the result as a forward route.
Only a root Linux Agent advertises the raw-socket capability.

The Return route section summarizes each saved trace as a carrier backbone name and
a color-coded quality tier; the hop table opens only when that carrier is
selected. The summary uses conservative IP backbone signatures informed by
backtrace (CN2, 163, AS9929, AS10099/CUG, 4837, CMIN2, CMI and CMNET). AS10099
is China Unicom Global and is distinct from AS9929. A single ordinary
backbone hop can be only the destination network, so insufficient evidence is
shown as unidentified. In particular, an unannounced Unicom hop is not proof
of 9929, and a 223.120 address is not necessarily CMI rather than CMIN2.
The tier is a route-class hint, not a latency, loss or service-quality grade.

International bandwidth is manual-only and uses the public iPerf3 endpoints
documented by Leaseweb for Singapore, Los Angeles and Frankfurt. It runs the
three regions sequentially, tries at most ports 5201-5203 when a shared port is
busy, and caps each download/upload direction at 8 MiB (48 MiB total). The
whole task has a two-minute deadline. The result is a bounded structured record;
raw command output is never retained.
These shared endpoints are a diagnostic sample, not an SLA or an unattended
capacity monitor. The Agent resolves each configured hostname to a public IPv4
address before execution and binds the test to its Center-confirmed public
egress address.

Vastora does not execute TcpQuality or copy its implementation: that repository
does not declare a software license. It only informed the choice of the same
standard iPerf3 protocol. The actual endpoints and supported port range are
verified against Leaseweb's own documentation. A Linux Agent advertises the
bandwidth capability only when `iperf3` is already installed; Vastora does not
silently install or modify host packages.

### Meridian entry-to-landing bandwidth

The Meridian node Network tab can start a separate two-host TCP iPerf3 check
against a selected, ready managed landing server. It measures the authenticated
private-peer path, not the public entry address, proxy application throughput,
or a guarantee about client experience. The Center checks that the source has
a running Meridian installation, both Agents are online and Docker-capable,
the landing is selected and ready, and both peer addresses are in the managed
100.64.0.0/10 range. The caller cannot supply an arbitrary host or port.

Center atomically queues one temporary listener task on the landing and one
client task on the entry. They use the same immutable multi-architecture
`ghcr.io/userdocs/iperf3-static` image digest, host networking with the
listener bound only to the landing's private IP, an unpredictable high port,
unprivileged UID, no host mounts, and a four-minute task limit. The landing
listener handles two connections then exits; both containers are explicitly
removed. Unconfirmed removal retains an uncertain execution for manual review.
The source measures one TCP stream for ten seconds in each direction,
sequentially. Both temporary containers are limited to one CPU each. Traffic
varies with measured throughput and is not byte-capped; a 1 Gbps link could
transfer about 2.5 GB across both directions. No scheduled or page-load test
runs. A result appears only after the paired server
task also succeeds. A new check replaces the previous pair result for that
entry and landing listener. The latest result is displayed in the Network tab,
with the source-to-landing and landing-to-source directions labelled.

No scan runs on page load or on a schedule. Opening the page reads saved
results; polling observes saved checks and refreshes score expiry/connection evidence.

## Targeted validation before release

Run the parser, runner-options and Center IPQuality tests, migration-equivalence
test, node-diagnostics validation and iPerf3 parser tests, and the frontend
IPQuality model/component checks when local verification is authorized. On a
non-production Linux node with iPerf3 installed, manually run one check, verify
the IP matches, close/reopen the panel, then check cancellation and container
cleanup. Confirm the six bandwidth samples remain within 48 MiB and are
sequential, then verify cancellation leaves no iPerf3 process. Verify old
Agents/offline nodes cannot queue a check and that changing egress marks the
previous report stale. Do not use production for validation.
