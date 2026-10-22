# Retained legacy execution retirement

This local maintenance operation retires execution only. It is not application
uninstall, container removal, subscription retirement or database cleanup.
Keep the controller, shared gateway and application-owned native services running.

Prerequisites: back up Center and the selected node; verify native entry identity,
subscription delivery, double-hop routes and accounting continuity. The legacy
Docker Xray must already be stopped with restart policy `no`. The installed native
entry must have a ready ownership receipt, active units and a matching retained
`entry-source.json` checkpoint. Do not use this for controllers or unconverted nodes.

After installing a released Agent supporting this command, stop only that node's
Agent to exclude concurrent tasks. Run as root with the reviewed exact source ID
and the native state directory recorded in its ownership receipt:

```sh
vastora agent retire-legacy-execution --data-dir /var/lib/vastora/agent \
  --application-id REVIEWED_SOURCE_ID --native-state REVIEWED_NATIVE_STATE
```

The command verifies prerequisites, saves a private SQLite/key/receipt recovery
snapshot with SHA256 manifest, and atomically marks the existing resource receipt
`execution-retired`. It does not delete encrypted installation records, runtime
configuration, containers, counters, backups or Center records. A repeat is a no-op.
Restart only the Agent afterward, including after a rejected preflight; do not
restart the old container. Confirm heartbeat, native service health, subscriptions,
routes and monotonic accounting before continuing to the next selected node.

Retired receipts are excluded from legacy health/usage sampling and startup
restoration. Package mutation, maintenance that can resume a writer, and old
monitor recovery are rejected. Historical installation listings remain available;
this deliberately does not remove the old application from Center or relabel it as
a successful uninstall. Native health/usage remain owned by Glivela. There is no
automatic unretire or database downgrade; keep recovery evidence for reviewed repair.
