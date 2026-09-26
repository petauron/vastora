# Landing source IP selection

Meridian's landing manager supports automatic IPv4 routing (empty selection)
and one explicit IPv4 or global IPv6 address assigned to the landing host.
The edit is revision-fenced and preserves existing source/account grants.
Agents report a separate dual-stack egress inventory on each heartbeat. The UI
lists local IPv4 and global IPv6 addresses with their interface names; loopback,
link-local, ULA, container and overlay interfaces are excluded. Offline inventory
is not offered. Manual entry remains available and is revalidated on application.
A NAT server uses its local interface address. Center schema 98 adds the inventory
column through a forward-only migration with the mandatory pre-migration backup.

An explicit selection is strict, not an address-family preference. An IPv6
selection cannot reach IPv4-only targets. No preferred-family fallback is
implemented. Dante binds the source and family; the dedicated UID firewall
also constrains source, destination family and management-port exclusions.
The only cross-family exception is DNS to the two fixed public resolvers.
IPv6 requires TCP-only Meridian source grants. It does not add UDP support.

Agent verifies local interface ownership and routing at application time. It
stops the previous daemon before validating an explicit replacement, so an
invalid binding fails closed rather than retaining a working but wrong exit.
Failures are visible in the existing activity/recovery workflow. Clearing the
selection restores automatic IPv4 after reconciliation.

Landing and entry Agents must advertise landingEgressIP before a selection is
accepted. Health evidence uses exitIp for either family, removing the former
IPv4-specific field. During an Agent rollout, missing fresh evidence withholds
fixed routes until upgraded entries report it; there is no legacy health alias.

The selected binding remains in the existing versioned landing plan JSON.
Existing node IP-quality scores still describe the
IP of their collected report, not an assertion about a newly selected source.
This change neither replaces those reports nor claims the selected IPv6 is
residential. End-to-end IPv6 evidence must be observed after application.
