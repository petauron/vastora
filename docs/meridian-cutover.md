# Meridian cutover

Meridian replaces the remaining 3x-ui control and subscription ownership in
Vastora. It is a Petauron product module, not a renamed 3x-ui package and not a
general-purpose proxy panel.

## Final ownership

| Concern | Authority | Runtime executor |
| --- | --- | --- |
| Account plan, expiry, enable state | Meridian domain persisted by Center | Center |
| Stable native and routed credentials | Meridian domain persisted encrypted by Center | Agent-managed Xray |
| Entry and egress grants | Meridian domain persisted by Center | Agent-managed Xray and landing gate |
| Shared usage ledger and reset boundary | Meridian domain persisted by Center | Agent reports monotonic counters |
| Desired/applied revision and runtime health | Center | Agent confirms after Xray validation and atomic apply |
| VLESS/base64 and Mihomo subscriptions | Meridian renderer | Center subscription listener |
| Application and host lifecycle | Vastora | Vastora Agent |

3x-ui is not an authority in the final system. There is no panel database,
panel service, 3x-ui subscription response, client API adapter, or runtime
fallback after cutover.

## Repository boundary

`github.com/petauron/meridian` owns the runtime-independent account,
credential, route-grant, usage-ledger, and subscription rules. It must not
import Vastora packages or know about Center HTTP handlers, Agent task queues,
Docker, Headscale, HAProxy, or landing probes.

Vastora imports the Meridian module and owns persistence and orchestration.
The Agent receives complete versioned desired state; it does not infer an
account or fetch mutable configuration from Meridian or 3x-ui.

## Target topology

- Center is the single subscription authority.
- A Meridian installation owns either one Xray entry runtime or the single
  Center-colocated subscription service; there is no `master` or `worker`
  product role. The subscription-only installation deliberately has no local
  Xray revision and reports no proxy endpoint inventory.
- Each entry keeps its existing application identity, public address, UUID,
  REALITY material, subscription token, and traffic watermarks through the
  forward migration.
- The existing Center subscription URL remains stable. During the bounded
  cutover, the handler renders the immutable imported snapshot before any
  runtime replacement starts; after completion it resolves the token to a
  Meridian account and renders only applied healthy credentials whose
  shared-443 public listener and DNS verification are also applied. A later
  pending entry is omitted without taking already-ready entries offline.
- A fixed VLESS entry-to-egress route uses its own stable credential and shares
  the parent account plan. Adding routes never duplicates quota. Hysteria2 is
  native-only until Xray provides a reliable per-user routing boundary.
- An unavailable landing blocks only its fixed routed credential. Center
  rebuilds the entry without that credential, keeps unrelated native and
  routed credentials online, and excludes the unavailable route from both
  live output and last-applied subscription snapshots. If the route was
  configured to hide its native entry, that entry stays hidden so recovery
  cannot silently change the selected egress.

## Forward-only cutover

The cutover is one migration with explicit durable phases. Each phase is
idempotent and the next phase starts only after the previous result is
confirmed.

1. **Inspect**
   - Freeze new 3x-ui client mutations.
   - Read the selected controller inventory, node inventory, subscription
     tokens, account plans, identities, REALITY material, route grants, and
     monotonic counters.
   - Reject duplicate UUIDs, tokens, names, route ownership, missing workers,
     decreasing counters, or ambiguous in-flight operations.
   - Stop with an explicit incompatibility if an old client has a simultaneous
     IP limit or an old entry has its own traffic ceiling. Meridian has one
     shared account quota and Xray has no equivalent built-in per-client IP
     policy, so silently discarding either restriction is forbidden.
2. **Back up**
   - Create the normal Center pre-migration backup.
   - Ask the controller Agent for one final encrypted 3x-ui database snapshot
     and record its digest. Do not use the snapshot as a runtime fallback.
3. **Import**
   - Insert Meridian accounts, credentials, grants, watermarks, and deployment
     revisions without changing public identities or active Xray state.
   - Store subscription tokens and authentication material through existing
     encrypted Center secret storage.
   - Record the exact number of enabled landing routes. Retirement cannot
     begin until every imported route has an applied healthy runtime receipt;
     an unavailable landing stays visible as blocked and holds verification.
4. **Publish the imported snapshot**
   - Change the existing public subscription route to the Center listener and
     wait for its applied gateway or tunnel receipt before replacing any
     legacy runtime.
   - The old 3x-ui origin remains live until that receipt arrives. Both origins
     render the same imported identities and tokens, so a delayed route update
     does not create a gap.
   - While the cutover is incomplete, Center serves only identities rooted in
     the encrypted import digest. Automatic quota and private-peer revisions
     may still converge, but normal Meridian management remains locked so the
     imported identity set cannot become a second mutable authority.
5. **Project**
   - Send complete per-entry desired state directly to each owning Agent.
   - Install the audited Meridian package on the Center-colocated subscription
     host as well. When that host owns no entry, package presence and Xray
     runtime presence remain distinct: its heartbeat stays healthy without
     inventing an empty authoritative proxy inventory.
   - The Agent validates the candidate with the pinned Xray image, atomically
     replaces the old Xray process, confirms the applied revision and config
     digest, and reports runtime health. During this phase Center's imported
     snapshot continues to serve the stable subscription URL, and the new
     runtime temporarily keeps the old Docker aliases.
   - After the runtime receipt, Center marks the entry service ready, applies
     the node-local HAProxy SNI route, and verifies that the public hostname
     resolves to the selected node and reaches the new REALITY listener.
   - An uncertain result stops for explicit recovery. It never causes a second
     writer or automatic rollback to 3x-ui.
   - Recovery retains the failed execution evidence, requires an administrator
     to confirm that the previous executor stopped, and then permits exactly
     one replacement of the Agent pending journal with the current complete
     Center projection. Meridian never imports an unknown runtime Xray config
     back into account, credential, or route authority.
6. **Verify the projected data plane**
   - Require every VLESS entry's runtime, service, shared-443 listener, DNS,
     and public reachability receipt to be ready. Runtime health alone is not
     sufficient to switch authority.
   - Render both ordinary and Mihomo output from Meridian state at the existing
     URL and token, and compare identities and names to the imported inventory.
   - The subscription authority marker is already durable from the publication
     phase; verification advances only after every new runtime and public entry
     receipt matches the imported projection.
7. **Retire**
	- Only after every imported entry is applied, both subscription formats
	  render successfully, and every public subscription route targets Center,
	  remove the old installation receipt, database volume, account journal,
	  management API state, and temporary Docker aliases from every migrated
	  node.
	- Recheck the Center publication, every entry revision, and every enabled
	  egress route before each remaining retirement task and before marking the
	  cutover complete. A landing or entry change during this phase first applies
	  as an ordinary Meridian revision while retaining migration aliases; cleanup
	  resumes only after that revision is healthy.
   - A subscription-only controller host retires its old 3x-ui container,
     volume, local journal, and installation receipt through a separate
     receipt-bearing command because it has no entry revision on which to
     attach the normal runtime retirement step.
   - The transition release keeps the legacy schema and read-only export path
     solely as forward-migration input; they are unreachable after the durable
     Meridian authority marker is written.
   - Preserve the encrypted final snapshot according to the explicit migration
     retention policy; do not keep an executable fallback.
8. **Schema cleanup release**
   - After production cutover evidence confirms every installation is on
     Meridian, ship a separate tested forward-only migration that drops the
     legacy tables, export task, routes, source paths, and compatibility tests.
   - Do not mix this irreversible database cleanup into the runtime cutover
     release and do not provide a downgrade or dual-runtime path.

Released databases never migrate backward. A failed schema migration leaves
the existing release untouched and reports the backup path. A failed runtime
phase leaves the last confirmed data plane active and blocks verification and
legacy retirement; the already-published imported subscription snapshot stays
available from Center.

## Schema target

The fresh schema and migration must contain one canonical set of tables:

- `meridian_accounts`
- `meridian_credentials`
- `meridian_route_grants`
- `meridian_usage_watermarks`
- `meridian_deployments`
- `meridian_cutover`

Existing `three_x_ui_*` tables are migration input only. The cutover transaction
moves application keys from `vastora-official/3x-ui` to
`vastora-official/meridian`, after which no runtime or HTTP request reads the
old authority. A later forward-only cleanup migration drops those tables once
fleet-wide cutover evidence has been recorded. In-flight legacy
client/controller operations must be terminal or explicitly recovered before
migration; they are not translated by guesswork.

## Interface target

- Catalog ID and app key: `meridian` / `vastora-official/meridian`
- User-visible name: `Petauron Meridian`
- Command prefix: `meridian.*`
- API prefix: `/api/v1/meridian`
- Xray container: `meridian-xray`
- Internal Xray network alias: `meridian-xray`
- Center subscription protocol: `meridian/subscription`
- Subscription service record: `subscription` (the existing record is retained
  so its public URL and publication identity remain stable)

The old IDs and routes are removed rather than aliased. Existing public
subscription URLs are data records, not API aliases, and remain stable through
their publication records.

## Completion evidence

Completion requires all of the following, not merely a successful build:

- after the cleanup release, no shipped image, service, API route, task kind,
  source path, or UI copy depends on 3x-ui; during the transition release the
  only allowed dependency is the one-time read-only migration input;
- a released Center database migrates forward from the last 3x-ui schema with
  a usable backup and preserved identities, tokens, counters, and URLs;
- fresh install creates Meridian entry nodes without ever installing 3x-ui;
- ordinary and Mihomo subscriptions parse and contain only applied healthy
  credentials;
- one finite account with a native route and two fixed egress routes consumes
  one shared quota and disables/restores all credentials in one revision;
- restart, duplicate report, lost response, monthly reset, single-route
  revocation, and runtime-blocked route cases remain idempotent and observable;
- an explicitly authorized real client reaches the native exit and both
  egresses, sees the expected final public addresses, and continues using the
  original subscription URL.
