# Scoped Agent rollout

Before upgrading Center, place `agent-update-scope.json` in its persistent data
directory (the directory containing `center.db`). For the official container,
this is `/var/lib/vastora` inside the Center data volume:

```json
{"agentIds":["pilot-controller-agent-id","pilot-worker-agent-id"]}
```

Use exact existing Agent IDs, not names or IPs. Protect the file as host-owned
configuration and include it in recovery backups. It survives image updates.
Center reads it before opening the database or starting automatic rollout;
restart Center after deliberately changing the policy. Invalid content prevents
startup. An empty array allows no Agent upgrades. An absent file preserves the
existing all-node policy; do not remove it during a pilot.

The policy covers automatic and manual Agent upgrades, pending-task claims, and
new execution authorizations, including recovery updates. Already authorized or
running updates are not cancelled: verify no excluded-node upgrade is in flight
before enabling the policy. Existing tasks and historical results are retained.
Rollout status counts only selected nodes and exposes `scoped` and `excluded`.

This is not a global task pause and does not grant installation permissions or
bypass version gates. Application upgrades still need their usual per-node
authorization. Other nodes remain on their existing versions; operations that
require the new Agent version remain blocked there. Do not disable their
running services or expand the rollout just to clear that expected condition.
