# 3x-ui app package

This package pins the official `ghcr.io/mhsanaei/3x-ui` v3.7.0 image by
content digest. Its typed Agent executor follows the upstream storage layout:

- `db` persists the default SQLite state at `/etc/x-ui`.
- `cert` persists panel certificates.
- `acme` persists ACME account and renewal state.

The management panel and subscription server are bound to the Agent's
Center-confirmed private service address. Publish either service through an
explicit Vastora access point; the management panel is never public by default.
The package grants `NET_ADMIN` and `NET_RAW` because upstream enables Fail2ban
by default to enforce per-client IP limits.

The Vastora Agent installs this package through an explicit typed handler. It
does not expose arbitrary Docker, Compose, or shell execution.

Upgrades preserve a durable database snapshot before the new container starts.
The Agent records the API token returned after an upstream token rotation, so
later panel, subscription, node-sync, and REALITY operations use the active
credential.

## Keep a subscription-only controller

In **Applications → 3x-ui → Manage subscription controller**, choose
**Remove local node** beside the controller's VLESS service and confirm.
This removes that local inbound and its access points, not the 3x-ui application.
The panel, subscription URL, global users and remote VLESS nodes are retained.
Any enabled landing route is restored to the host's own exit before removal.

The subscription-only controller remains in the controller card and no longer
occupies a row in the VLESS node table. Use **Create VLESS REALITY** in its
management view to add a local node again. Failed or interrupted removals can
be retried; retries retain the original inbound identity.

This operation requires the updated Center and its controller Agent. Center
schema migration 69 adds the removal command kind using the standard backed-up,
forward-only migration flow; existing command history and recovery locks remain.
