# Restricted host native application lifecycle

This is the generic lifecycle prerequisite for Glivela issue #54, not a proxy
control protocol or permission to deploy an unfinished migration.

## Explicit authorization

Keep the existing node identity, enrollment purpose, credential, private network
membership and history. Do not convert the node into a Docker worker, gateway
or tunnel. No node-wide permission flag or automatic enrollment rewrite is used.

For the first native installation on a restricted host, submit the ordinary
installation request with `backupsConfirmed: true` and an explicit
`authorizedCapabilities` list. The verified package must use the version 2
systemd executor. Only `native-services` and, when required by that package,
`root` may be granted. The existing package permission validator still rejects
missing permissions, undeclared grants and unreviewed permission expansion.

The Center checks this boundary both when creating and claiming the task. The
Agent independently checks the same narrow lifecycle policy after authenticating
the normal sealed task, then uses its ordinary installer and real health receipt.
Subsequent upgrades and uninstall remain subject to the ordinary application
permission checks, including explicit review of privileged upgrades; they do not
grant additional node capabilities. Configuration commands, arbitrary
application commands, Docker packages and host-path/device grants are outside
this exception. A revoked or changed enrollment purpose remains an error.

The Linux root Agent advertises its existing systemd executor on these hosts;
this describes available execution machinery, not installation authorization.
It does not connect to Docker merely to advertise or execute a native package.

## Retained installation identity

Where an original Docker installation record must be retained, the separate
backup-first `agent stage-native-package` command prepares disabled native
services without changing the active registration. Its verified source receipt,
private recovery backup and reviewed inputs are subsequently bound to the
ordinary installation request by `replaceApplicationId` and
`sourceReceiptSha256`. Center migration 116 only adds this digest field; it does
not grant permissions or rewrite prior migrations.

Only a matching healthy installation result may change the application key.
Existing application, service and publication IDs remain associated; old private
recovery evidence and history are retained. Partial activation requires the
existing reviewed execution-recovery flow, not automatic fallback or reinstall.

Completing these generic prerequisites is not the complete proxy cutover. The
Glivela state handover, removal of legacy business writers, signed catalog
publication and runtime route verification must finish before deployment.

## Preserve the shared gateway

The native deployment decision applies to Glivela node and Xray processes, not
to Vastora's existing shared gateway. Keep HAProxy's deployment, public 443
ownership, other SNI routes and unmatched-SNI behavior unchanged. Do not replace
a shared listener with an application-exclusive listener.

A ready native installation supplies its ordinary private TCP service endpoint.
The shared listener uses the forwarding mode declared by the signed package and
stored with that service. The endpoint must match the application's node and
its confirmed service address; loopback, public addresses and missing or stale
installation ownership evidence are rejected. Literal private backends do not
use a Docker DNS alias. The generic gateway does not select Xray versions or
understand Glivela roles, accounts, DNS policy or business health.

This does not prove live forwarding or change existing firewall permissions.
Retain service/publication identities during the separately reviewed handover,
and do not clear a route alert merely because a listener configuration exists.

## Source completion audit

The following is an implementation checklist, not evidence of an installed
cutover. Do not merge or publish the whole migration while active legacy
business writers remain.

Already implemented:

- `agent stage-native-package`, `package_native_stage.go` and
  `package_native_replace.go` stage stopped native resources and bind the
  original receipt, backup and installation identity to the ordinary executor.
- `native_installation_handover.go` and forward migration 116 preserve the
  application/service/publication associations through a verified result.
- `controlplane/native_lifecycle.go`, the Center claim check and Agent task
  check enforce explicitly reviewed native lifecycle grants on restricted
  hosts. The installation sheet requires backup confirmation.
- `node_listeners.go` and the generic listener renderer support the confirmed
  private endpoint of an owned native application behind the existing gateway.
- The Agent task switch no longer executes Glivela-specific link-server/link
  bandwidth diagnostics or Xray configuration inspection/recovery. It no
  longer advertises the removed link diagnostic capability. General host,
  return-route and bandwidth diagnostics are unchanged.

Still active and requiring removal before this migration is complete:

- `task_runner.go` still dispatches proxy-related `application.command`
  payloads (runtime, acceptance, legacy export/retirement, protocols, REALITY,
  subscriptions, clients, nodes and controller), `landing.server.apply` and
  `landing.proxy.apply`. Remove these executors, not just their labels.
  Preserve the separate Senulun command branches and generic lifecycle tasks.
- `application_command_tasks.go` still resets legacy account/endpoint cycles
  and queues proxy runtime/client commands before claiming commands.
  `deployment_tasks.go` still claims landing and Xray recovery tasks. These
  producers must be removed alongside the legacy HTTP handlers and UI paths,
  not left queuing tasks that the new Agent cannot execute.
- `server.go` still registers Meridian inventory/account/route/cutover and
  landing operations, proxy application commands, Xray recovery and proxy
  diagnostics. `store_agent.go` and `orchestration.go` still contain legacy
  heartbeat/reconciliation business paths. The `agent landing-firewall` local
  command also still restores legacy proxy firewall state. Frozen migration input is not
  permission to keep any of these writers running.
- The reviewed catalog runtime recipes are not yet final native application
  entries. Materialize the entries through the existing reviewed publication
  flow only after immutable Glivela release assets exist; do not publish
  placeholder URLs/digests or invent another publication framework.

After source closure and required CI, verify the retained installation and
shared-gateway entry handover, adopted business data/traffic, node reporting
and real proxy routes. Only then may the already reviewed release/catalog and
deployment workflow proceed. Staging, a ready listener or green source CI
alone is not this evidence. No additional user decision is needed for the
approved source work; these are unfinished implementation and runtime checks,
not a request to expand permissions or delete historical data.
