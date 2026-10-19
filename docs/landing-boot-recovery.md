# Native landing boot recovery

Vastora Agent resumes the native landing service once at startup, before its
first Center heartbeat. This is local boot recovery, not replay of an update or
an offline authorization change. It works on ordinary and Docker-free nodes.

Recovery requires a complete committed host journal belonging to the enrolled
node, unchanged root-owned service files, the original service UID, and an
enabled service. Removed instances, pending file/policy transitions, unknown
firewall ownership, and changed files are not adopted. The saved exact-source
firewall is restored before starting Dante; success requires its UID-owned
private listener. Entry-to-egress probes still decide route publication.

An obsolete `90-local-dns.conf` drop-in can refer to an older policy-hashed nft
table and fail its `ExecStartPost`, preventing the dependent SOCKS service from
starting. Startup recovery and explicit landing apply retire only the recognized
DNS-only workaround when the isolated managed public resolver is already in
place. Shell commands, extra directives/rules, other destinations/ports/tables,
and untrusted files are not adopted. Other administrator drop-ins are preserved.
The original hook is renamed to `90-local-dns.conf.vastora-retired`; systemd does
not load this suffix. An existing recovery copy is never overwritten. The nft
source file is retained unchanged. No firewall flush or permission expansion is
performed, and no database downgrade or compatibility runtime is introduced.

## Validation

The existing native-host integration gate reproduces a failed stale-table
prerequisite and checks recovery, repeated recovery, backup preservation,
disabled/tombstoned services, pending transactions, foreign node identity and
configuration drift. Parser tests constrain the one-way workaround migration.

`TestLandingNativeBootFixture`, guarded by
`VASTORA_NATIVE_LANDING_BOOT_FIXTURE=1`, is only for a newly created disposable
systemd LXC. A separate harness owns its synthetic network, actual container
reboots and resource cleanup. The integration test binary's
`agent landing-recover-test` entrypoint calls the same recovery implementation
as Agent startup; it is not a production CLI. No production Center, private
keys, subscription, or node identity is used.

Verified on 2026-10-18 in an unprivileged Debian 13 LXC with real systemd,
nftables and the source-verified bundled Dante 1.4.4. The lifecycle integration
test passed. A stale-table hook then reproduced the prerequisite failure before
an actual container reboot; startup recovery retired it and restored traffic.
A second real reboot retained the repair without a service restart loop. Both
boots passed a SOCKS HTTP roundtrip to a synthetic destination assigned only
inside the container and an explicit SOCKS ruleset denial for a private target.
The backup and unrelated drop-in remained unchanged. No production enrollment,
IPv6 egress acceptance, host reboot, or production upgrade is claimed.
