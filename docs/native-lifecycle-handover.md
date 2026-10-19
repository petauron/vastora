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
