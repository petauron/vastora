# Landing-only nodes

Issue: #940. This mode is for a **new** small-disk Linux egress host, not an
automatic conversion or Docker cleanup tool.

## Enrollment

Configure Headscale first. In Nodes → Add node, select **Landing-only node (no
Docker)**. Generate the usual one-time command and run it on the new host.
The Center binds the purpose to the enrollment; a local CLI flag alone cannot
grant it. Confirm the discovered private network, then use the existing landing
pool and source authorization controls to configure native Dante.

An online Agent is not proof of a working egress. Wait for the landing revision
and perform the existing supported TCP/UDP and actual-exit checks before using
the route. No FullCone guarantee is added.

The host uses the same Agent and signed binary distribution as ordinary nodes,
with Tailscale and the existing native Dante/nftables dependencies. The loader
authenticates the one-time enrollment before the second-stage script chooses
dependencies. Landing-only installation does not install or probe Docker.
Existing stopped Docker services are not started or removed.

## Restrictions and lifecycle

- Container and native application packages, gateways, tunnels and public node
  listeners are unavailable. The Center rejects application deployment and
  disallowed task delivery; the Agent also rejects tasks outside its landing,
  update and decommission lifecycle.
- Purpose survives enrollment replay, restart and reconnect. Heartbeats cannot
  convert a landing-only node to an application node, or vice versa. The local
  configure command cannot override the Center-issued purpose.
- Private SOCKS binding, authorized-source firewall policy, destination
  restrictions and egress address selection remain in the existing landing
  implementation. No new route source of truth is introduced.
- Update downloads are size-bounded. Free-space checks run before staging and
  scheduling replacement, allowing room for binary copies and SQLite recovery
  data. Insufficient space returns an error without replacing the running
  service. A space check cannot prevent a concurrent process filling the disk;
  existing atomic writes and failure handling remain necessary.
- Agent log emission is rate-limited through its own systemd unit. Global
  journald policy and unrelated files are unchanged. This is not a claimed hard
  per-service disk quota.
- Removal uses the owned native landing cleanup instead of Docker cleanup and
  keeps the existing Agent/Tailscale ownership and data-retention rules. No
  existing production host is converted or cleaned by enabling this feature.
  Managed Tailscale is stopped and purged locally, including its keys, without
  requiring a successful remote logout. A retained external Tailscale still
  requires logout. Local uninstall does not claim to delete the Headscale node
  record; Center retirement is a separate operation.

## Verification status

The first delivery is one complete vertical slice: new-node enrollment and
managed private networking, Center-selected native Dante, authorized TCP/UDP
and fail-closed health, and Docker-free update/removal. Existing-node conversion,
an Agent rewrite and a general plugin system are explicitly excluded.

With explicit permission for isolated local acceptance, the Go regression
tests for the CLI, Agent and Center have been run. Purpose now persists in the
connection record, independently of the disposable installation operation.
Agent schema 21 to 22 is a backed-up forward migration; old nodes remain ordinary
nodes. Tests cover removal of the completed installation operation and reopening
the store, as well as the preserved schema-21 backup. The three landing-only
onboarding/management component tests and the TypeScript/Vite production build pass.

### Preliminary amd64 lifecycle result (2026-10-18)

On a disposable unprivileged Debian 13 amd64 LXC, the opt-in
`TestLandingOnlyLinuxLifecycle` passed: two-stage installer, systemd restart,
Center-queued update to a differently versioned test executable, new-version
landing-only heartbeat, successful update receipt, and `agent uninstall --purge
--delete-data`. Docker/containerd executables and the Docker socket were absent;
the Agent binary, service unit and database were absent after removal. An
unrelated pre-existing Postfix service remained active. No production node was
used and no release was published.

This is deliberately **not full #940 acceptance**: the preliminary fixture
authorizes enrollment through the internal Center enrollment routine to isolate
the Agent lifecycle. It does not exercise Headscale provisioning, the page,
native Dante deployment, source grants, real proxy traffic or Tailscale removal.
Its two test versions use the same source with distinct version markers; it is
not evidence of upgrading a previously released landing-only implementation.
The fixture cannot substitute for the standard public enrollment path below.

The root filesystem used 838144000 bytes at the fixture baseline, 883777536
after installation and after the completed update, and 838144000 after removal.
These are lifecycle snapshots including lab prerequisites, **not** upgrade peak,
the complete landing footprint, Docker savings or a minimum disk recommendation.
The release-script Dante bundle has separately been built on disposable Ubuntu
22.04 amd64 and exercised in the private-network acceptance below.

### Real private-network amd64 acceptance (2026-10-18)

The opt-in `TestLandingOnlyPrivateLinux` passed in 283 seconds against a
dedicated Debian 13 landing LXC and Ubuntu 22.04 control LXC. Neither is a
production node. This tests the working tree based on `bbf98afc`, not a published
release. The lab control plane runs Headscale 0.29.3 natively; its test adapter
renders, checks and installs real Headscale policy instead of using the
production Docker control-plane wrapper. The landing host has no Docker.

- The public enrollment handlers, two-stage installer, real managed Tailscale
  join and real Agent heartbeats establish the private connection. Center selects
  the landing and dispatches native Dante deployment through its task/receipt
  pipeline. Source policies use the production landing queue with explicit test
  plans, not the browser account-grant flow.
- Authorized SOCKS TCP HTTP and UDP DNS roundtrips pass. Unauthorized and revoked
  sources fail; TCP-only authorization rejects UDP. A separate rejection test
  requires the explicit SOCKS ruleset-denied reply for loopback, link-local
  metadata, management ports and the control LAN, rather than counting any
  network error as successful denial.
- Official Xray 26.9.30, the actual client configuration renderer, VLESS/REALITY,
  HAProxy PROXY v2 and private Dante pass an HTTP request with matching actual
  public exit. This fixed VLESS route supports TCP only; native Dante UDP was
  tested separately and is not a claim of VLESS UDP or FullCone support.
- Revoking the source and stopping Dante block traffic, including the real
  VLESS route without direct fallback. Deleting only the owned nftables table
  causes the Agent to stop its owned Dante, Center to show failure, and TCP/UDP
  to fail. Center can subsequently reapply a healthy deny-all configuration.
- Center queues an update from lab.1 to lab.2 while managed Tailscale and Dante
  are installed. New-version heartbeat and successful update receipt are both
  required. These are versioned builds of the same development source, not a
  compatibility claim for a previously released landing-only implementation.
- Final `agent uninstall --purge --delete-data` passes with the lab HTTPS control
  endpoint deliberately stopped. The first attempt exposed remote logout
  blocking managed removal; the corrected local managed-removal path has a
  regression test. Agent binary/database/unit, native Dante unit and owned
  firewall table, managed Tailscale binary/state are absent afterward. Docker
  and containerd packages/socket remain absent. The pre-existing Postfix service
  remains active; an unrelated sentinel file and independent nftables table
  retain exactly the same hashes. Shared dependencies and the dedicated native
  UID/tombstone are intentionally retained to prevent ownership reuse.

Native landing status now checks fresh runtime evidence separately from the
configuration receipt: owned files, service, listener and exact firewall policy.
Missing or unhealthy evidence is not displayed as ready. Evidence expires after
90 seconds; failure detection is heartbeat-driven, not instantaneous. Center
schema 112 to 113 has a tested forward-only migration and backup. This evidence
does not independently prove Internet availability or private-network reachability.

Regression results: CLI, Agent, Center and landing Go suites pass; the three
onboarding/management component tests and production web build pass. The lab fixtures are
explicitly opt-in and do not run host mutations in ordinary CI.

The maintainer explicitly deferred real arm64 acceptance for this delivery.
Cross-compilation is not an arm64 runtime result. Keep the broader architecture
checklist open; no arm64 installation, upgrade or removal result is claimed.

### Browser acceptance progress (2026-10-18)

An isolated Chrome/Playwright session exercised the actual Center UI with the
signed official application catalog. A newly created Debian 13 amd64 guest
enrolled using the page-generated one-time command, joined real managed
Tailscale, and confirmed its private network through the page. Docker/containerd
executables and the Docker socket remained absent. The ordinary application
installer excluded the landing-only node. Node management displays its purpose
and disables incompatible roles without exposing the unrelated Glivela exit
policy controls.

Selecting the new guest in Glivela deployed native Dante through Center and
the real Agent. The page became ready after runtime health arrived, changed to
failed when the owned service was deliberately stopped, and returned to ready
after restoration. A reused guest with a different, previously removed native
identity was correctly rejected; its journal was not cleared to force success.

This is not a browser account/grant/public-subscription acceptance. The
maintainer explicitly excluded public subscription and arm64 runtime testing
from this delivery. The earlier real Xray/TCP/UDP tests use an explicitly
documented source-plan fixture, not the browser account flow. Public ingress is
not required for the isolated landing-only acceptance and must not be added to
production nodes for this test.

The original broader #940 checklist still includes deferred dedicated arm64 clean-host
acceptance: enroll through the page, verify no Docker CLI/daemon/socket access,
confirm the private network, authorize an entry, transfer TCP/UDP through the
real client, revoke sources, restart/reconnect, update, and uninstall. Include
an installed-but-stopped user Docker fixture. Exercise interrupted downloads,
integrity failures, low disk and service/firewall failures. Record OS/architecture,
the tested commit, actual exit and baseline/installed/upgrade-peak disk usage.
No minimum VPS disk size or savings figure is asserted without those results.

### Isolated amd64 rerun and firewall regression

The follow-up uses two new unprivileged Debian 13 amd64 LXC guests on the
maintainer-designated test host, with no production changes or public ingress.
The control guest is limited to 2 vCPU / 2 GiB RAM / 8 GiB disk; the landing guest
to 2 vCPU / 1 GiB RAM / 8 GiB disk, both with swap disabled. These limits are test
conditions, not minimum supported resources.

Repeated source-policy changes with an unrelated nftables table exposed an
existing removal read-back bug: unmarshalling into the previous snapshot reused
map elements and retained deleted objects. Removal now decodes a fresh snapshot.
`TestServerFirewallRemovalReadsFreshSnapshot` reproduces the old failure and
verifies the fix, idempotent removal, and the exact owned-table delete command.
The unrelated table is never deleted to make the test pass.

The final fixture uses synthetic HTTP, UDP DNS and TLS 1.3 targets inside the
isolated control guest. A temporary guest-only /32 route sends the public-shaped
test destination to that guest; the production destination policy remains
unchanged. The ordinary host and shared network routes are not modified. Source
observations compare native SOCKS with VLESS/HAProxy/Dante, so direct fallback
would not satisfy the test. This is **not** public subscription or Internet
availability acceptance. Earlier external handshake/IP-echo targets proved
unstable and were removed from the repeatable fixture.

Lab Agent builds must set **both** `internal/center.Version` (CLI version) and
`internal/agent.Version` (heartbeat/update version) to the same marker. The
private fixture accepts `VASTORA_LAB_TARGET_VERSION` for repeat upgrades; its
served binary must match that version. A mismatched experimental build was
correctly rejected, not treated as successful upgrade evidence. Failed lab
attempts are retained rather than hidden by clearing the database.

Final rerun: `TestLandingOnlyPrivateLinux` passed in 198.63 seconds with the
firewall fix and synthetic targets; the separate restricted-destination test
passed while the source was authorized. The managed lab.2→lab.3 update had
already succeeded with Dante installed, including the new-version heartbeat
and receipt. The firewall repair was subsequently installed as lab.4 in this
disposable guest; that manual repair is not counted as a managed-update result.

After the final test stopped the HTTPS control endpoint, `agent uninstall
--purge --delete-data` succeeded. Agent executable/database/unit, native Dante
unit/owned table and managed Tailscale executable were absent. Docker/containerd
and the Docker socket remained absent. Pre-existing Postfix was still active;
the unrelated sentinel hash and independent nftables table were unchanged.
No production node, public subscription or arm64 host was tested or modified.
