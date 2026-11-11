# Automatic Agent updates and explicit pauses

Center automatically synchronizes every eligible online Agent to its own release.
Newly enrolled nodes need no allowlist entry. Disabled/revoked nodes, offline
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
opening the database and queuing updates. Restart Center after a deliberate
policy change, then verify the live rollout status (`paused`, `excluded`) and
actual task/Agent versions; changing a file alone does not change a running
process. Paused nodes are excluded from automatic/manual queueing, pending claims
and new execution authorization, including recovery updates. Already running or
authorized updates are not cancelled; inspect those before introducing a pause.

Only the Agent program update is paused. Node reporting, applications, proxies,
subscriptions and their existing authorization/quota rules remain unchanged.
Removing a pause does not bypass unresolved execution or failure recovery gates.
