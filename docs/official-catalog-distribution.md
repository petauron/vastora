# Official catalog consumer trust boundary

Vastora consumes the versioned [petauron/catalog](https://github.com/petauron/catalog)
module. Protocol, schema, signing, artifact verification and publication belong
there. Center acceptance, application integration, Agent runtime execution and
independently reviewed bootstrap trust remain here.

Schema 4 is implementation work with production gates closed. Historical schema
3 tests/publications do not establish acceptance of this migration/executor.
See the [maintenance guide](catalog-release-guide.md) before operational changes.

## Independent trust and persistent acceptance

A reviewed initial TUF root ships from `catalog/trust/`, independently of the
download origin. Images use `/app/catalog-trust/1.root.json`; direct binaries
must be provisioned with the root via `--official-catalog-root`. Never bootstrap
trust from the download server or promote a Center-generated signing key.

The maintained go-tuf client validates rotation, expiry, hashes and rollback.
The signed target also binds `vastora-official`, `stable`, lifetime and monotonic
catalog revision. That revision is distinct from application package revisions.

TUF metadata, target hash, high-water marks, manifest history and trusted time
persist independently of deletable source rows. Failed refreshes preserve the
verified displayed cache. Valid root revocations checkpoint even if a later
target fails, without extending the old cache or authorizing installation.
Root-only catalog revision zero differs from historical package revision zero;
neither permits resetting accepted trust.

During cutover retain raw schema 3 audit bytes/digests, but use schema 4 as the
only new execution contract. Restored databases require fresh trust validation
before catalog-dependent deployment. Clock regression fails closed; this does
not solve compromised clocks, all-key compromise or loss of every independent
recovery record.

## Refresh, admission and execution

Expired catalogs are display-only. Install/upgrade admission and first dispatch
require fresh verified metadata and the reviewed revision/digest. Configure and
uninstall use recorded installed resources, not the current online recipe.
Never-issued rejected upgrades restore captured prior app state atomically;
issued/unknown executions retain explicit reconciliation.

Unknown runtime protocols/capabilities block only that package/node. Signature
verification never replaces Agent-side artifact and ownership checks.
Product integrations manage pairing/topology/credentials, not separate ordinary
installers. Privilege grants are explicit, never preselected in the install
sheet; changed revision/digest requires review again.

Catalog-source status loads on the apps screen without a prior Settings visit.
A stale UI cannot bypass server admission. Refreshing a source never queues
upgrades or changes running resources.

## Publication and evidence

After maintenance handoff only the independent publisher writes the established
`https://downloads.petauron.com/vastora/catalog/` layout. Its ledger preserves
exact signatures and complete history, including imported records. GitHub
assets are publisher evidence, not client trust roots.

No catalog private keys or publisher workflow belong in Vastora. Do not revoke
other projects' shared credentials. The independent publisher validates upstream
provenance without trusting notification URLs, uses immutable/conditional
storage writes and verifies the public accepted revision before completion.

Consumer CI's `internal/center/testdata/reviewed-catalog-v4.json` is a frozen
integration snapshot, not an authoritative distribution list. The separate
`catalog-v4.json` is a minimal synthetic generic-app fixture. Portable
schema/crypto tests live in the shared
module; runtime and migration tests remain here. Test success is not a live
maintenance window, no-restart proof, full recovery drill or production release.
