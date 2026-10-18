# Public-node security baseline (MVP)

Related: #955. This is opt-in, per node. A program upgrade does not install or
enable this baseline automatically. A public egress IP is not proof of public
ingress: the administrator explicitly reviews default-route interfaces, globally
routed IPv6, cloud NAT mappings, management sources and service ports.

**Draft limitation / merge blocker:** managed Meridian/3x-ui and native landing
nodes currently reject enablement. A default-deny public INPUT chain can block
different-source replies to dynamically bound outbound UDP sockets; approving
443/udp alone does not preserve FullCone behavior. The Agent also fences adding
these runtimes while this baseline is active. Socket/cgroup-scoped return rules
and real-host UDP acceptance are concrete remaining work for #955. Do not close
the issue or deploy this draft as protection for the proxy fleet.

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
   process start time, approved peer and port. A checkbox, old SSH connection,
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

Sources: [UFW manual](https://manpages.ubuntu.com/manpages/noble/man8/ufw.8.html),
[Docker iptables user rules](https://docs.docker.com/engine/network/firewall-iptables/),
[Docker nftables backend](https://docs.docker.com/engine/network/firewall-nftables/),
[Fail2ban configuration](https://github.com/fail2ban/fail2ban/tree/master/config).
