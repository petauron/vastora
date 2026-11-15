# Native IPv6 entry registration

An IPv4 SSH NAT mapping is not a public proxy endpoint. Select an assigned global
IPv6 candidate explicitly in the node network profile. Management continues to
use the existing private address. The publication uses an AAAA record.

Install the signed `vastora-official/glivela-entry` systemd application with its
own controller client certificate. Configure its new entry through Glivela's
existing authenticated `POST /v1/admin/entries` API. Use the installed application
ID as the entry ID, a fresh endpoint/credential identity, the selected IPv6
listener, and the same advertised/listening port. Glivela owns configuration,
REALITY target verification, execution and authenticated readiness/traffic
receipts. Never manufacture an old Docker migration or final-counter receipt.

Once the native entry actually reports ready, open the installed application's
**Register native entry** form. The admin-only
`POST /api/v1/meridian/native-entries` accepts only `applicationId`, `name` and
`regionCode`; it reads the fixed private Glivela controller snapshot itself.
It verifies the running installation, selected address, exact runtime identity
and fresh receipt, then atomically registers the existing account's new
credential and sole ledger checkpoint. Existing entries, subscription tokens
and counters are untouched. Repeated registration is rejected without writes.

This initial direct-entry slice accepts existing active unlimited accounts only;
limited, expired or disabled account policies must not be silently discarded.
The publication remains pending until normal DNS and public access verification
succeeds. A port check or Agent heartbeat alone is not end-to-end acceptance:
download the existing subscription, parse it with the intended client, then
perform certificate-verified HTTPS through that exact IPv6 node. An IPv4-only
client cannot directly reach this endpoint.

Private plan files, client keys and full subscription addresses must not appear
in public logs or reports. Keep the controller snapshot mount read-only and
retain normal installation/configuration backups.
