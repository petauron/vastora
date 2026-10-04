# Meridian host runtime

Refs #394. This change implements the host data plane for managed Meridian
entries. Account billing and finite-quota policy changes remain deferred under
the current unlimited-account MVP scope.

## Runtime and ownership

- Center owns complete, versioned Meridian configurations. Agent validates,
  replaces, journals and observes the pinned Xray container. No node panel or
  second configuration writer is added.
- Xray uses Docker host networking with no published ports, bridge attachment
  or Docker DNS alias. The REALITY backend listens on the entry's approved,
  locally assigned private IPv4 address, TCP `10443`. Public subscription
  addresses and port `443`, UUIDs, REALITY keys, short IDs and HY2 credentials
  remain unchanged.
- Node-local HAProxy keeps public TCP `443`, the exact SNI allowlist and
  route-scoped Proxy Protocol v2. Its backend changes only after the matching
  runtime receipt. HY2 binds public UDP `443` directly; Xray's API binds only
  `127.0.0.1:10085`. The ordinary application bridge is unchanged.
- A root Agent creates the dedicated primary group `vastora-meridian`. Xray
  runs as `0:<that GID>`, with all capabilities dropped; HY2 receives only
  `NET_BIND_SERVICE`. Docker did not retain an effective bind capability with
  a numeric non-root user in the tested configuration. This does not use
  privileged mode or change the host's unprivileged-port sysctl. Root UID in
  the container is not a claim that it is equivalent to a non-root container.
- Keep PID/mount isolation, a read-only root filesystem and configuration
  mount, no-new-privileges, and the existing resource limits. `SETGID` and
  supplementary groups are unavailable. Host sockets retain the dedicated GID.
- Landing leases match that GID plus the pinned peer and service ports in
  nftables output/input hooks. Conntrack marks associate replies with those
  connections. Expiry blocks established replies as well as new requests;
  packets cannot renew the lease. Agent probes use another GID and remain
  independent. Native traffic to other destinations is outside this gate.
- Docker restart policy is `no`. Agent installs closed gates before restarting
  the journaled runtime. It verifies network/security settings during recovery
  and rejects drift. Private addresses and occupied TCP/UDP ports are checked
  before stopping the applied runtime.

## Forward migration and rollout

Schema 102 adds the private backend address and changes internal backend ports
to `10443`. Center creates its normal pre-migration database backup. The
migration aborts if a runtime command is pending, running or unreconciled. It
does not regenerate identities or overwrite applied service/HAProxy endpoints.

Ready entries are marked pending with a new revision, so **starting this Center
version queues a fleet topology change**. Resolve outstanding commands, verify
each private service address and port, and retain the Center backup and
encrypted Agent journals before deployment. The managed release serves its
packaged Agent binaries and prioritizes `agent.update`; its existing version
fence prevents older Agents from claiming migrated work until they reconnect
on the new version. Thus each Agent upgrades before its runtime handover.
Verify those update receipts during rollout. This PR is not an instruction to
deploy it on a production fleet.

The successful runtime receipt updates the canonical service backend. Old
heartbeats cannot publish a new backend early or restore container DNS/443.
A private-address or listener mismatch fails closed. HY2-only entries do not
publish a TCP REALITY route.

Only retained bridge journals and leases are read for migration/recovery; new
Meridian containers cannot be created in bridge mode. Closed old leases are
removed by their recorded ownership after handover. The obsolete
`preserveLegacyAliases` task field and alias-creation branch are removed.
Failure restores the known configuration/runtime through the existing atomic
replacement boundary; uncertain outcomes retain evidence for explicit
reconciliation. There is no automatic database downgrade or 3x-ui fallback.

## Evidence and limits (2026-10-01)

The isolated Linux tests cover actual nftables install/readback/expiry, UDP
traffic while closed/open/expired, independent Agent probes, TCP/UDP socket
ownership and missing private addresses. These tests are part of CI. Center
tests cover forward migration and backup, busy-command rollback, preserved
subscription identities, applied-receipt backend changes and stale observations.

The [reproducible proxy lab](../scripts/meridian-fullcone-lab/README.md) uses
the pinned Xray `26.7.28` and HAProxy `3.2.7` images. Both VLESS/REALITY through
HAProxy in a separate bridge namespace + Proxy Protocol v2 and native HY2
passed, before and after container
restart:

| Check | VLESS/XUDP | HY2 |
| --- | --- | --- |
| Ordinary STUN binding response | Pass | Pass |
| Response from another IP and port | Pass | Pass |
| Response from another port | Pass | Pass |
| Same mapping when sending to another destination | Pass | Pass |

The protocol parser checks transaction IDs, response source addresses and
XOR-MAPPED-ADDRESS; a 32-packet capture independently confirms all four runs.
Unknown SNI was closed and the API was unreachable from the peer namespace.
Mappings are stable within each association; restarting Xray can allocate a
new source port, which is expected.

The lab shares Xray's network namespace with a private namespace holder to
avoid touching development-host listeners. There is no extra Docker NAT at
the Xray boundary, but this is **not** a public-provider FullCone result or an
actual production Docker host deployment. An exact private STUN allow rule
exists only in the fixture because Xray otherwise blocks private destinations.
Production outbound policy is unchanged. Fixed landing routes remain TCP-only.

No A1/production migration, public NAT/provider classification, handset/client
matrix, or before/after CPU and memory comparison was performed. These production acceptance items are separate from the requested functional
delivery of #394; an ordinary UDP response or an `Unknown` classifier result
must not be reported as FullCone.

## Functional delivery follow-up

The follow-up hardens UDP evidence parsing: exact SOCKS relay source, STUN
cookie/transaction/length checks, duplicate mapped-address rejection and
complete TCP handshake reads. Checks remain active under Python optimization.
Malformed data, timeout or destination-dependent mapping reports unconfirmed,
never a successful FullCone classification. The CI gate exercises parser
negative cases and the complete isolated VLESS/REALITY + HY2 paths before and
after restart, including the existing SNI and API boundaries.

Closing the feature after required CI and merge records software delivery.
It does not certify an arbitrary provider's NAT, a production migration, or
unmeasured CPU/memory savings. Fixed remote landing routes remain TCP-only.
