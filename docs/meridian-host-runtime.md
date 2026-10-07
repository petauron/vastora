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

The former standalone proxy lab and its CI job were retired from the MVP
pipeline on 2026-10-07. The earlier isolated results were historical fixture
evidence, not public-provider or production acceptance. Production outbound
policy is unchanged; fixed landing routes remain TCP-only.
