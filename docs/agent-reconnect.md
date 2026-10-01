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

## Reviewed replacement and persistent pause (#759)

`GET /api/v1/agents/{id}/reinstall-plan` provides an administrator-only,
non-cacheable review of one node from a consistent database snapshot. It does
not create a reconnect grant, retire identity, contact a host or run a command.
The response contains no saved configuration, keys, command payloads or raw results.

The inventory selects the latest deployment **intent**, including a pending or
failed uninstall, rather than resurrecting the last successful installation.
It keeps the exact saved version and validates its artifact description without
fetching or selecting the latest catalog version. Artifact and credential
verification is still required before any execution. Missing or inconsistent
saved artifacts require review. Meridian requires validation of its Center-owned
configuration and credentials; a Pulse collector requires verified original
monitoring identity and replacement credentials. Stateful and unknown applications
require data restoration on the replacement, even if a previous backup drill was recorded.

The `monitoring` inventory reads original registration IDs from authenticated,
encrypted task/result evidence for the same collector application and monitoring
service. It includes earlier installs after a configure/upgrade and preserves
expired registration evidence. Names and addresses are never identity matches.
Its review revision binds the retained evidence, without returning tokens or
task payloads. Missing, inconsistent or excessive evidence is explicit; partial
evidence must not select a remaining candidate automatically.

`inspection_required` means these registration IDs still need inspection through
Pulse's supported `enrollment inspect ID` command. Registration creation does not
prove that the collector consumed that token; an install may have retained an
older local credential. This inventory does not contact Pulse, rotate a token,
create a monitor node or establish that monitoring is restored. The managed
inspection and credential replacement steps remain unfinished in this draft.

Unclaimed work is listed separately from executions: retiring an execution
session does not dispose an unclaimed install, uninstall or network change.
Retired and unretired uncertain results keep their actual phase and state.
Retained inactive network profiles remain visible as dependencies, never as
authorization for a new identity. Managed private address preservation cannot be
promised with the current Headscale API, so explicit address migration remains
a requirement. No plan item implies completed restoration or working traffic.

The node action opens this review without changing credentials. Confirmation
posts `operationId`, `planRevision` and `confirmReplacement` to the reconnect
endpoint. The current administrator and the exact reviewed snapshot are recorded
before external bootstrap preparation; an outdated review is rejected without
revoking the node. Schema 105 stores this operation and invalidates unused legacy
replacement grants that have no review binding. Forward migration uses the normal
backup and fail-closed path.

The persistent operation fences all task claim and authorization paths, including
Agent updates and Xray repair exceptions. Heartbeats may report observations, but
cannot use a runtime-generation increase to reconstruct old applications or replay
unclaimed work. Enrollment preserves the old network profile under its **old**
Agent public key; matching addresses on the new machine cannot automatically
activate that old binding. The new machine must use a fresh key.

An unused, unexpired command can be retrieved with the same operation ID and
review revision after a lost response or Center restart. Its response is encrypted
at rest and never included in the read-only plan or node list. Repeated requests
cannot create another operation or repeat an interrupted external bootstrap call.
Preparing/failed operations remain paused for inspection. A separately confirmed
review can replace an unused grant; explicitly stopping node access also revokes
in-flight preparation, so a late response cannot restore the grant.

The page shows saved progress on both online and offline nodes. A replacement
reaches `review_required`, not completion. This draft deliberately has no action
that clears the pause merely because the Agent is online. Keep the PR unmerged
until private identity withdrawal, explicit address migration, reviewed historical
effect disposal, new application restoration tasks and real business verification
are integrated into a resumable flow. Stateful apps still need a verified backup;
retained successful business records do not prove they exist on the replacement.

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
