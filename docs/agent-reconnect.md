# Reconnecting a reinstalled node

The existing reconnect command replaces the Agent identity while preserving the
logical node and its business associations. It does not establish that its
applications, private-network identity or traffic have recovered.

## Execution boundary

Creating a reconnect command revokes the previous credential and execution
session in the same transaction. An active execution or update helper becomes
unknown; failed outcomes, phases, encrypted task/result evidence and unresolved
fences remain. No old command is replayed or marked successful by this operation.
Session history prevents the replacement credential from reviving an old session.

Unresolved executions acquire a durable `identity_retired_at` marker. Their old
results cannot be automatically projected during session registration or Center
restart, nor offered as confirmation that an application exists on the new
machine. This matters even when the replacement advertises the same runtime
generation. Ordinary process restarts on the same enrolled machine still recover
verified retained results through the existing path.

Schema 104 adds this marker and audits retirement. The normal forward migration
backs up schema 103 before changing it and fails closed on errors. Historical
rows default to no retirement marker: the migration does not invent which old
results predate a past reinstall. Existing uncertain historical work still needs
inspection. Schema downgrade is not supported.

## Remaining recovery work (#759)

`GET /api/v1/agents/{id}/reinstall-plan` provides an administrator-only,
non-cacheable review of one node from a consistent database snapshot. It does
not create a reconnect grant, retire identity, contact a host or run a command.
The response contains no saved configuration, keys, command payloads or results.

The inventory selects the latest deployment **intent**, including a pending or
failed uninstall, rather than resurrecting the last successful installation.
It keeps the exact saved version and validates its artifact description without
fetching or selecting the latest catalog version. Artifact and credential
verification is still required before any execution. Missing or inconsistent
saved artifacts require review. Meridian requires validation of its Center-owned
configuration and credentials; a Pulse collector requires a replacement managed
enrollment. Stateful and unknown applications require data restoration on the
replacement, even if a previous backup drill was recorded.

Unclaimed work is listed separately from executions: retiring an execution
session does not dispose an unclaimed install, uninstall or network change.
Retired and unretired uncertain results keep their actual phase and state.
Retained inactive network profiles remain visible as dependencies, never as
authorization for a new identity. Managed private address preservation cannot be
promised with the current Headscale API, so explicit address migration remains
a requirement. No plan item implies completed restoration or working traffic.

This is an execution-isolation repair, not the complete reinstall workflow.
Private identity withdrawal, approved address migration, a new plan from saved
application intent, data restore requirements and end-to-end business evidence
still need the unified recovery flow. In particular, retained successful business
records do not prove the replacement machine is serving those applications.

The pinned Headscale 0.29.3 [control API](https://github.com/juanfont/headscale/blob/v0.29.3/proto/headscale/v1/headscale.proto)
does not provide an atomic node-identity replacement or explicit per-node IP
assignment operation. Deleting and reenrolling relies on allocator behavior and
cannot guarantee address preservation. The [upstream request](https://github.com/juanfont/headscale/issues/3133)
for manual address assignment remains separate. Recovery must report unavailable
address preservation and require explicit migration, rather than edit Headscale's
database or silently accept a new address.

The focused regressions cover a real Center enrollment/task/result/reconnect
sequence, late execution calls, preserved uncertainty, transaction rollback and
forward migration. They do not simulate a complete host reinstall or claim that
private networking and proxy traffic have recovered.
