# Public-node security baseline (MVP)

Related: #955. This is opt-in, per node. A program upgrade does not install or
enable this baseline automatically. A public egress IP is not proof of public
ingress: the administrator explicitly reviews default-route interfaces, globally
routed IPv6, cloud NAT mappings, management sources and service ports.

**Current MVP scope, clarified by the owner on 2026-10-19:** ordinary proxy TCP
and UDP requests and their corresponding replies, with a TCP 443 public proxy
entry. FullCone, P2P hole punching and different-source UDP replies are not
required. Existing connection-tracking rules handle ordinary replies; no proxy
UDP port range, socket classifier, packet mark or dynamic port cache is added.
Necessary existing private-transport/service ports still require explicit review.
Proxy/landing topology alone no longer rejects enablement. Existing SSH,
ownership, backend, publication and rollback checks remain mandatory. This PR
remains a draft pending review, new CI and the remaining applicable matrix checks.

## Workflow

1. Open **Nodes → Manage → Public-node security baseline**. Reading the page
   does not queue work. Explicitly prepare missing Debian/Ubuntu distribution
   packages if needed. Only missing packages are requested; no apt upgrade,
   Docker/Tailscale upgrade or OS reboot is requested.
2. Preparation masks Fail2ban before installation so a package post-install
   cannot start an unreviewed SSH jail. An interrupted preparation retains its
   root-owned mask and marker. Retry the explicit preparation operation to
   reconcile it; do not manually unmask/start a partially prepared jail.
3. Enter all public interfaces, actual SSH ports, explicit management CIDRs,
   and approved TCP/UDP public service ports. Include the actual Tailscale UDP
   listener where present. Examples in the form are not detected configuration.
4. Request a preview. Existing managed public ports and current public Docker
   bindings must be preserved. Unowned UFW/Fail2ban configuration, unknown input
   rules, missing IPv6 handling, or an unsupported Docker backend reject enablement.
5. From an approved management source, open a **new, non-multiplexed SSH
   connection**. Run the displayed root `security-verify-ssh REVISION` command
   with `SSH_CONNECTION` preserved. It checks the actual sshd process ancestry,
   TCP-socket ownership/start time, approved peer and port. A checkbox, old SSH connection,
   TCP port probe or Center heartbeat is not an SSH receipt.
6. Explicitly approve application. Agent reruns preflight and checks the exact
   locally reviewed preview, its fresh SSH receipt and its deadline. Before any
   firewall change it saves a root-only recovery checkpoint and enables a
   systemd rollback watchdog independent of the Agent and Center connection.
7. Within five minutes, open **another new SSH connection** and run the displayed
   command for the applied revision. It must have started after actual application
   finished, not just after preview. Verify private networking and real existing
   proxy requests, then explicitly confirm the revision in Center.
8. Center requires fresh healthy evidence for configured landing and Meridian
   links, collected after actual application. Agent also requires the post-apply
   SSH receipt. Otherwise the watchdog rolls back the pending policy on expiry
   or on an intervening reboot. The previous connection staying open is not proof.

Preview revisions are unique per operation even when policy content is identical.
An old confirmation cannot cancel a subsequent pending application. Lost HTTP
responses are recovered by reading durable task state, never by silently retrying
a mutation. Uncertain task executions use the existing Activity disposition
workflow; they are not automatically replayed as successful firewall changes.

## Firewall ownership and network behavior

- UFW is accepted only when inactive with distribution configuration and no
  pre-existing user rules. Existing administrator managers/rules are not adopted.
  Owned configuration is backed up and checked before replacement/restoration.
- Only approved public interfaces enter the deny-by-default owned input chain.
  Approved service ports, management-only SSH, established traffic, necessary
  ICMP/ICMPv6 and DHCP replies return to the existing distribution rules.
  Other private interfaces are not given a blanket new allow rule. UFW's default
  routed policy is not changed to DROP; kernel network tunables are preserved.
- Docker support is deliberately bounded to an already verified iptables
  `DOCKER-USER` backend. Docker's native nftables backend is rejected, not switched
  or silently treated as protected. An owned child chain uses conntrack original
  destination ports for public DNAT traffic and leaves other forwarding alone.
  Docker's own chains, NAT, daemon settings and container network modes are not
  rewritten. Both families are prepared; the boot watchdog service orders before
  Docker and restores only its owned child-chain hook after a reboot.
- Node listener and public gateway changes check approved ports before their
  normal runtime mutation. New ports require reviewing a replacement baseline;
  the old baseline is not silently widened. Existing landing nftables tables,
  UID/source/egress checks and fail-closed proxy grants remain separate owners.
- Fail2ban enables only the maintained sshd filter with the systemd backend and
  nftables multiport action. Five failures in ten minutes cause a ten-minute
  SSH-port-only ban. Only explicit management CIDRs and loopback are ignored;
  private subnets are not automatically exempted. No HTTP forwarding header is
  used for SSH identity. Administrator-only inspection/unban is audited.
- Rollback disables only the newly owned UFW/Fail2ban baseline, restores its
  configuration, and removes its Docker child-chain hook. Packages, unrelated
  host rules, landing tables, and backups are retained. Administrator edits to
  owned files cause `action_required`, not an overwrite of unknown configuration.
  The protected checkpoint requires out-of-band review in that conflict case.

## API and persistence

- `GET /api/v1/agents/{id}/security`: last durable task/evidence, not a live probe.
- `POST /api/v1/agents/{id}/security`: administrator + CSRF, bounded typed action:
  `inspect | prepare | preview | apply | confirm | disable | unban`.
- Reuses encrypted task delivery, execution authorization, leases, receipts,
  task events and node execution serialization. No arbitrary command/path input.
- Forward migration 114 adds one per-node security task row. Existing migration
  backup/fail-closed handling is retained; no database downgrade path is added.
- Local recovery is root-owned under `/var/lib/vastora-node-security`. No
  passwords, private keys, subscription URLs, or raw apt/authentication output
  are returned to Center. Public/private addresses are administrator-only data.

## Validation and rollout gate

Unit/contract tests cover bounded policy/action validation, unique preview
binding, task attempts, publication-port checks, SSH-only jail generation,
UFW public/private boundaries, Docker user-chain command scope, malformed process
evidence, expired evidence and forward migration. UI tests cover no mutation on
mount, confirmation gating, preview-only status and lost responses.

These are **not a substitute for real-host acceptance**. Before merge/production
enablement, validate on disposable Debian/Ubuntu LXC/VMs with independent console
access: new SSH before/after apply, timeout/Agent kill/Center loss/reboot rollback,
UFW reload, Docker bridge and host traffic, both IP families, private transport,
landing SOCKS and real VLESS/REALITY/UDP requests, ban/unban, conflict recovery,
and small-node memory overhead. Do not test by applying a firewall to production.
This document does not claim those real-host checks have already passed.

### Isolated Debian acceptance, 2026-10-18

An explicitly authorized disposable unprivileged Debian 13 LXC (1 CPU, 1 GiB
RAM, 256 MiB swap, 4 GiB disk) exercised the real Host security engine, generated
native Dante policy and Xray 26.9.30 with synthetic namespace-local endpoints.
This was not a production rollout or full Center/catalog enrollment.

- Before enablement, Dante and host-network Xray passed TCP, normal UDP and
  different-source UDP replies after a multi-target association.
- With the baseline enabled, TCP and normal UDP still passed, including
  Xray → SOCKS5 → Dante → synthetic target. Different-source UDP replies failed
  for both runtimes, with the owned public deny counter increasing. This was a
  reproduced blocker under the original scope; different-source replies were
  explicitly excluded by the subsequent owner clarification above.
- The approved public TCP port remained reachable; an unapproved listening port
  and public SSH from a non-management source were denied. The native landing
  source restriction continued rejecting an unauthorized private source.
- Old SSH multiplexed connections were rejected; genuinely fresh approved SSH
  connections were accepted before and after application. The tests uncovered
  and corrected LXCFS/kernel boot-clock mismatch and dual-stack mapped socket
  display handling; neither correction widens management CIDRs.
- UFW reload preserved the native landing nftables table byte-for-byte and
  normal UDP on all three tested paths remained usable.
- Without confirmation or a running Agent/Center, the real five-minute systemd
  watchdog rolled back the pending policy. UFW/Fail2ban stopped and all three
  different-source UDP probes recovered. Reapplying revealed leftover unhooked
  UFW chains; cleanup now removes only the owned input child, and preflight
  accepts only known, empty inactive distribution hooks, never active rules.
- A separate temporary experiment matching each test service's actual socket
  cgroup restored different-source UDP while keeping unapproved ports blocked.
  Those native nft rules were removed after the experiment: inserting them into
  an iptables-nft-owned table is not a supported production integration. Correct
  ownership, reload, container replacement and boot ordering remain unresolved.
  Production identities must come from verified applied runtime ownership (the
  inspected container/process and its actual cgroup, or the owned native service
  receipt), not the experiment's unit names or an assumed global Xray service.
  Unmerged service-identity changes are not treated as an available dependency;
  missing/stale ownership evidence must continue to reject enablement.
- Distribution application-profile preparation, actual IPv6 UFW chain names,
  empty user-rule inventory and executable ownership/size checks were corrected.
  Known package profiles require exact ownership and package integrity; custom,
  changed, symlinked or rule-bearing configurations remain refused.

These results do not establish Docker bridge/container acceptance, a complete
VLESS/REALITY handshake, real Tailscale, IPv6 packet delivery, Ubuntu coverage,
or reboot recovery. Those applicable checks remain necessary; preservation of
different-source UDP replies is no longer part of the clarified MVP.

### Follow-up isolated socket experiments, 2026-10-19

A new dedicated unprivileged Debian 13.6 LXC was used, without reusing other
tests or opening public ingress: 1 CPU, 1 GiB RAM, 256 MiB swap and an 8 GiB
disk. Public-shaped IPv4/IPv6 peers were synthetic namespaces inside that LXC.
Neither production nodes nor host networking were changed. No project
dependency or application capability was added.

The socket experiments are **not a shipped implementation**:

- The existing deny-by-default guard reproduced the failure in both families:
  ordinary replies passed, but replies from a different address/port failed.
- A separately owned nft table classified sockets by their actual cgroup and
  passed a test-only packet mark to a persistent UFW child chain. This restored
  both families without editing an iptables-nft-owned table through native nft.
  Explicitly excluded test listeners remained blocked across UFW reload.
- Restarting the service left the old nft cgroup selector referring to the old
  kernel identity; alternative-source replies failed closed until the selector
  was regenerated. Fixture-only start/stop hooks regenerated it from the new
  cgroup; a subsequent LXC reboot restored the synthetic paths and listener
  exclusions. Fixed fixture unit names are not production ownership evidence.
- A counterexample invalidated blanket cgroup authorization: another UDP
  listener in the same service, not explicitly excluded, became reachable.
  Process ownership alone does not establish an outbound socket's purpose.
- A second counterexample invalidated a recent-outbound-port cache, even when
  combined with the current socket's cgroup: an outbound socket sent traffic and
  closed; a new, otherwise unapproved listener in the same process rebound its
  port and inherited the cached permission. A timeout does not bind a grant to
  the lifetime of the original socket.

Therefore neither workaround is enabled in the product. Under the original
scope, a required next capability would have been trustworthy
outbound-socket identity, or a demonstrably complete runtime listener/egress
contract that excludes internal listeners throughout configuration changes and
socket reuse. Current Docker inspection/systemd invocation evidence establishes
process ownership, not socket purpose. nft socket/cgroup lookup and timed sets
are available but do not by themselves close the demonstrated gap. Do not
substitute port polling, blanket UDP permission, or wider application privileges.
After the owner's ordinary-proxy clarification, this experimental authorization
is unnecessary and the topology-only refusal is removed without adding it.

An independent confirmation defect was fixed: both first confirmation and an
already-verified replay now recheck effective UFW rules and the live SSH jail
before accepting success. Docker evidence also requires an available
DOCKER-USER chain actually reached from FORWARD, rather than silently accepting
a missing/detached backend. Confirmation also rechecks its deadline after the
runtime inspection, so a slow check cannot confirm an expired revision. Failed
checks leave recovery state/deadline intact.

- All nodesecurity unit tests passed in the new LXC, including confirmation
  failure/replay and missing/detached Docker-chain fixtures.
- A temporary, restricted acceptance driver exercised the real host verifier:
  UFW stopped, IPv4 DROP removed, IPv6 DROP removed and SSH jail stopped each
  refused confirmation without changing pending recovery state. Restoring each
  component permitted a healthy verified replay. The driver waited for jail
  readiness; its first run had correctly refused a not-yet-ready jail.
- This is not new SSH-authentication, complete apply/watchdog, real Docker,
  Dante/Xray, bridge/NAT, Tailscale or Ubuntu acceptance. The synthetic reboot
  experiment does not establish production runtime reconciliation or complete
  security-policy reboot recovery. #955/#957 remain incomplete.

No permission expansion is implicit in this investigation. Xray documents an
outbound `SO_MARK` option, but using it is not a firewall-only change: the current
managed runtimes drop capabilities and native landing has an empty capability
bounding set. Its permission requirements and Dante parity would need a separate
reviewed runtime contract. Introducing a new dependency/kernel mechanism,
widening application capabilities, or relaxing preservation of existing UDP
behavior requires an explicit decision, not an automatic workaround.

### Clarified ordinary-proxy acceptance, 2026-10-19

Following the owner's clarification, the same dedicated LXC exercised an actual
synthetic chain: client → HAProxy TCP 443 with exact SNI → VLESS+REALITY → native
Dante 1.4.4 → local synthetic HTTP/UDP target. Xray 26.9.30 was used as the entry
and client, not as a replacement landing runtime. Its official release archive
digest matched the cached archive. Native landing configuration, unit and nft
policy were generated by the existing project implementation; its non-root and
empty-capability boundary was retained. Docker 29.9.1 used its existing
iptables/DOCKER-USER backend, without switching the daemon firewall backend.

The narrowed slice removes the topology-only refusals in Center/Agent/Host and
their unused helpers. Publication-port checks, typed task authorization,
serialization, ownership refusal, fresh SSH evidence and rollback remain.
The node page states the ordinary TCP/UDP scope using its existing components.
No experimental socket classifier, mark, port cache, blanket UDP range or new
project dependency is shipped.

Real Docker acceptance exposed and corrected two additional defects:

- DNAT is a conntrack **state**, not `--ctstatus DNAT`; use `--ctstate DNAT`.
- A failed installation could leave the owned child chain before attaching its
  parent hook. Rollback/reconciliation now removes a verified generated prefix
  even without that hook, and refuses unknown/edited rules instead of flushing
  arbitrary contents. The observed failed apply rolled back and the orphan was
  subsequently reconciled before a supported preview and successful apply.

Confirmation checks parent reachability/first position and the child's complete
ordered policy, not only rule membership/count. Comparison accepts iptables'
canonical conntrack-option order, while retaining exact firewall rule order.
A real same-count/same-members DROP-before-allow reordering was refused by
confirmation and restored exactly; regression tests cover that counterexample.

Passed in the isolated fixture:

- IPv4 TCP and corresponding UDP replies through the REALITY/Dante chain
  before enablement, while pending, after UFW reload, after confirmation and
  after confirmed-policy reboots. IPv6 client entry passed under the confirmed
  policy and after reboot; both entry families also passed after boot rollback.
  This does not claim IPv6 Dante egress or different-source UDP support.
- An old multiplexed SSH connection was refused for the new revision; fresh
  pre/post-apply SSH evidence passed, as did a new authenticated SSH connection
  after reboot. Internal proxy backends and unapproved SSH sources stayed denied.
- Approved IPv4 bridge/host and IPv6 bridge Docker TCP 443 bindings passed; a subsequently created
  unapproved published port was blocked. Container outbound NAT and internal
  DNS/interconnect remained usable after apply/reload/reboot.
- IPv6 tests covered real listeners through both INPUT and Docker FORWARD:
  approved 443 passed, unapproved published/host ports and SSH source failed.
  With IPv6 FORWARD policy ACCEPT after reload, the owned Docker DNAT DROP and
  public INPUT DROP counters increased for the respective negative probes.
- Without a running Agent/Center or an explicit disable, the genuine five-minute
  watchdog expired the pending policy and rolled it back. Separately, reboot of
  an unconfirmed revision triggered independent rollback **before its original
  deadline**, with SSH and ordinary proxy access still working.
- Native landing policy survived reload and reboot. The first raw-hash mismatch
  was exactly reproduced by changing only table handle 4 back to 2; the original
  digest was not replaced. A subsequent captured before/after reboot comparison
  had no normalized semantic difference. The existing read-only policy verifier
  passed; the exact authorized private source worked and another private source
  was still denied. No actual rule change was hidden by accepting a new hash.
- Final nodesecurity tests, the focused Center security/task/migration tests,
  and focused Agent task tests passed inside the LXC. Temporary acceptance
  drivers are not part of the PR or a release.
- The final idle fixture snapshot was 163 MiB used / 860 MiB available out of
  1 GiB, with no swap use. This is not a peak-memory or production-load budget.

Boundaries: not an enrolled production Center/Agent run, Ubuntu matrix, real
Headscale/Tailscale deployment, public WAN/NAT or throughput benchmark. The LXC
shares the PVE kernel and is not an independent VPS-kernel acceptance. Future
Xray landing replacement needs its own ordinary TCP/UDP, private/UID boundary,
reload and reboot verification; Dante results do not establish that result.
FullCone/P2P/different-source UDP remain out of scope. Frontend assertions were
updated but not executed locally; exact-head CI/CodeQL results must be checked
separately on PR #957 after the authorized push. No production node, dependency
manifest or original draft was changed. Submission does not authorize merge,
release or production deployment.
The fixture policy was rolled back, its services/containers stopped, ephemeral
SSH credentials removed, and the dedicated LXC stopped with disk/evidence retained.

Sources: [UFW manual](https://manpages.ubuntu.com/manpages/noble/man8/ufw.8.html),
[Docker iptables user rules](https://docs.docker.com/engine/network/firewall-iptables/),
[Docker nftables backend](https://docs.docker.com/engine/network/firewall-nftables/),
[Fail2ban configuration](https://github.com/fail2ban/fail2ban/tree/master/config),
[Xray socket options](https://xtls.github.io/en/config/transports/sockopt.html),
[Xray 26.9.30 release](https://github.com/XTLS/Xray-core/releases/tag/v26.9.30).
