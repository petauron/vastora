# Vastora Proxy app package

The historical package ID remains `3x-ui` so installed applications, service
identities, subscriptions and recovery journals keep their identity during the
forward migration. Runtime ownership is role-specific:

- the single `master` keeps the pinned 3x-ui v3.7.0 image as a temporary,
  one-way migration adapter for its existing controller database;
- every `worker` runs the pinned official Xray image on the shared private
  Docker bridge as component and container `xray` / `vastora-xray`, and has no
  panel, panel database or node-side subscription service;
- the Agent exposes only the authenticated private endpoints needed by the
  controller adapter and renders the accepted state into Xray configuration.

Worker REALITY listens only on bridge-scoped container port 443 while the node
HAProxy remains the only owner of public TCP/443. HY2 explicitly publishes host
UDP/443 to the same container port with the pinned Xray 26.7.28 multi-architecture image. Hysteria `finalmask`
is rejected: upstream documents that a wildcard listener on a multi-homed host
can reply from the wrong source address, and treats that behavior as a known
limitation. The Agent rejects a listener route unless its upstream is the local
managed Xray Docker alias. It persists desired worker state
encrypted with separate desired/applied revisions, checkpoints traffic counters
across Xray restarts, and snapshots the old 3x-ui database before the first
cutover. Before promotion, a failed candidate restores the previous encrypted
state and configuration, removes the candidate, returns the old container to
its stable name and restarts it when it was previously running. An uncertain
rollback stops for explicit recovery instead of silently choosing a runtime.

The worker runs as a dedicated non-root identity. Its complete configuration,
including private protocol material, is readable only by that identity. Landing
health gates use the bridge-scoped runtime identity and exact peer service
ports; Agent probes remain outside that gate. An active landing route must be
explicitly disabled before changing runtime ownership so recovery never guesses
which gate owns it.

Every effective configuration change is rendered completely and checked by the
same pinned Xray image with `xray run -test` in a network-disabled validator.
Only a valid candidate is atomically promoted. Metadata and traffic updates do
not restart Xray; an interrupted desired revision is resumed before the private
management receiver reports the worker ready.

The management receiver binds only to the private service address, requires the
per-installation bearer token, implements a narrow explicit endpoint allowlist,
rejects panel/UI, controller inventory and updater endpoints, and does not
provide arbitrary Docker, Compose or shell execution. The compatibility surface
matches node synchronization operations; Vastora's node-local route adapter is
the only additional writer of complete Xray routing settings. Remote callers,
including the transitional controller, can read that document but cannot write
it even when they possess the node synchronization token.

Local Xray statistics are checkpointed with monotonic watermarks. The temporary
single controller may report its aggregate per-account counters, but it cannot
replace local counters or change identities. The worker uses the larger observed
value for quota enforcement, shares one account counter across protocols, and
keeps the last confirmed controller value while disconnected. A one-minute
Agent reconciliation loop handles quota and expiry transitions; ordinary
samples remain metadata-only, while crossing an enable/disable boundary creates
and applies exactly one new Xray revision. The loop stops after its first
control-plane error and leaves the desired/applied journal for explicit
recovery instead of retrying indefinitely.

Traffic resets are accepted only as authenticated, explicit controller
revisions. A disconnected worker does not guess a calendar boundary or clear
the last confirmed aggregate; it keeps enforcing the applied quota until the
controller confirms the reset. This bounds recovery to an auditable revision
and prevents an offline clock or replay from restoring exhausted credentials.

Host networking is only the deployment prerequisite for FullCone UDP, not its
acceptance result. A release must not claim FullCone until an explicitly
authorized node passes the complete client-to-proxy-to-exit tests required by
Issue #394 for VLESS/REALITY and HY2 separately.

The controller's subscription endpoint is implemented by Vastora's native
subscription listener. Worker nodes never publish a separate subscription URL.
After all workers have migrated, the remaining controller adapter can be
retired in the final controller slice without changing client links or node
identities.
