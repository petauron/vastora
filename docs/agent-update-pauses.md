# Automatic Agent updates and explicit pauses

Center and Agent updates are separate administrator actions. Upgrading or
restarting Center never queues Agent updates. In Software Update, push the
running Center version to all currently eligible online Agents or to selected
Agents. Newly enrolled or reconnected nodes need a later explicit push.
No update allowlist is maintained. Disabled/revoked nodes, offline
nodes, unsupported updates, unresolved executions and the administrator-owned
global task pause retain their existing protections.

To preserve an explicit maintenance pause, put `agent-update-pauses.json` beside
`center.db` in the persistent Center data directory:

```json
{"pausedAgentIds":["explicitly-paused-agent-id"]}
```

Use exact existing Agent IDs, not names or addresses. A missing file or empty
array pauses no nodes. Invalid content prevents startup. The retired
`agent-update-scope.json` is rejected at startup: translate only explicitly
approved maintenance pauses, then remove that old allowlist. Never translate its
unlisted nodes into pauses or retain an allowlist of current nodes.

Protect the pause file as host-owned configuration readable by Center, and
include it in the existing configuration recovery point. Center loads it before
opening the database and accepting explicit pushes. Restart Center after a deliberate
policy change, then verify the live rollout status (`paused`, `excluded`) and
actual task/Agent versions; changing a file alone does not change a running
process. Paused nodes are excluded from explicit queueing, pending claims
and new execution authorization, including recovery updates. Already running or
authorized updates are not cancelled; inspect those before introducing a pause.

Only the Agent program update is paused. Node reporting, applications, proxies,
subscriptions and their existing authorization/quota rules remain unchanged.
Removing a pause does not bypass unresolved execution or failure recovery gates.

## Independent update actions

Software Update contains separate Center and Agent cards. Center updates its
container, host updater and installation metadata. It neither creates fleet
Agent update tasks nor replaces/restarts a registered co-located Agent. When
that Agent shares the host CLI executable, the CLI remains at the Agent version
until that Agent is explicitly updated; the running Center version is reported
by Center itself. The existing explicitly coordinated one-time protocol
migration remains a separate maintenance command, not an ordinary Center update.

In the Agent card, review the running Center target version and node states,
then choose **Update all** or check nodes and choose **Update selected**. Both
require confirmation. The server freezes eligible online targets for that
request and persists ordinary per-node tasks; it never turns the selection into
a lasting allowlist. Paused, revoked, disabled, offline, unsupported or unresolved
nodes remain excluded. Missing/empty selected IDs are rejected, and a changed
Center version requires refreshing before pushing. Previously pushed tasks keep
their normal reconnect and recovery behavior; unrelated newly online nodes need
a new push. Agent updates do not upgrade applications or rewrite subscriptions.

`GET /api/v1/system/agent-updates` reads target/version/eligibility and task status
without creating tasks. The administrator/CSRF-protected `POST` accepts
`targetVersion`, `mode` (`all` or `selected`) and, only for selected mode,
`agentIds`. The receipt lists real queued and skipped IDs, including partial
failures. Center update status no longer embeds Agent rollout status.

### First deployment from the coupled updater

The previously released host update worker verifies that its co-located Agent
matches the Center target. The new installer cannot change the shell functions
already loaded by that running old worker. For the first deployment of this
separation, use the verified release bundle's normal direct `upgrade.sh` entry,
with no old host update job in flight; it installs the new worker and preserves
the Agent. Subsequent GUI Center updates use the independent worker normally.
If an old GUI job already installed the new Center and failed its old final
Agent-version check, inspect the actual versions and retry finalization using
the newly installed worker. Its existing same-release path verifies Center;
it does not reinstall, downgrade data, or upgrade the retained Agent.
