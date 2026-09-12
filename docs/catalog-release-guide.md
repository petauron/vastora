# Application catalog and program releases

This replaces the bundled-catalog release assumption described in issue #6.
The catalog source remains in this repository for review, but it is no longer
copied into the Center image or signed by Center on startup.

## Choose the correct release

| Change | Required release | Effect on installed applications |
| --- | --- | --- |
| Known app version, pinned digest/hash, or display text | Signed catalog revision | Shows availability; does not install or upgrade anything |
| New executor, installation semantics, or permission changes | Vastora program release, then compatible catalog | Administrator must upgrade the program before using the new contract |
| Upgrade an installed application | Existing application upgrade action | Authorized deployment task, with existing backup and audit controls |

An application-only catalog change must not be submitted as a Center version
bump. Catalog CI validates signatures and executor contracts independently of
the program release workflow. Container packaging does not use catalog JSON;
it ships the independently reviewed public bootstrap root instead. The packaged
Center command reads `/app/catalog-trust/1.root.json`, copied from `catalog/trust/`.
A new program release fails before release creation if that reviewed public
chain is missing or invalid. Direct binary deployments must provision the root
separately and pass `--official-catalog-root`; no initial trust is fetched from R2.

## Catalog release checklist

Distribution base: `https://downloads.petauron.com/vastora/catalog/`.
Objects belong under `vastora/catalog/` in the existing download bucket; do not
replace the bucket's other project or installer objects. The mutable pointer is
`vastora/catalog/timestamp.json`, not a file at the bucket root. This path choice
does not provision the signing environment or upload the first publication.

The uploader must use conditional S3 writes, not a check followed by an
unconditional overwrite. Immutable objects use `If-None-Match: *`; an existing
object is acceptable only after downloading it and comparing exact bytes.
The final timestamp write uses `If-Match` with the previously observed ETag
(or `If-None-Match: *` for first publication). A failed condition is a publication
conflict, never a reason to retry without the condition. R2 documents these
conditions in its [S3 API compatibility reference](https://developers.cloudflare.com/r2/api/s3/api/).

1. Review the application change and retain immutable content for all previously
   published app versions. Pin OCI digests and native platform-specific SHA256.
2. Run catalog validation and artifact verification before granting signing
   approval. A contract change is not approved merely because its JSON is valid.
3. Use the protected signing environment and independently approved root. Never
   put signing private keys in Git, the object store, or Center configuration.
4. Assign a new revision from the protected publication ledger; prepare the
   signed repository using `catalog-publish`.
5. Publish immutable targets and versioned metadata first, verify them, then
   update `timestamp.json` last. Serialize publications for the channel.
6. Independently run `catalog-verify` with the approved root, exact revision and
   target SHA256. Preserve the approved commit, workflow run and release record.
7. Refresh a Center without changing its program version. Check the new catalog
   revision and explicitly install or upgrade the selected application.

The hosting path is selected; initial root provisioning and the production
publication environment are not yet configured by this change. Do not describe the catalog
as live until those steps and the end-to-end publication check have succeeded.
See [distribution design](official-catalog-distribution.md) for the current
implementation checklist and trust limitations.

## Independent workflow and durable retries

`.github/workflows/catalog-publish.yml` is manually dispatched from `main` with
a monotonically increasing `revision`. It has no Center/Agent build, image push,
installer upload, or program-version bump. Its credential-free validation job
checks typed contracts, actual OCI manifests/config platforms, and native
artifact hashes and executable architectures before requesting signing approval.

The `catalog-signing` environment must already exist with a required reviewer
and deployment access restricted to protected branches. The job checks this
configuration and fails closed if it cannot read/confirm it. Operator setup:

- Commit an independently reviewed public root chain in `catalog/trust/`.
  Do not put root private keys in Actions. See that directory's README.
- Environment secret `CATALOG_SIGNERS`: JSON object with `targets`, `snapshot`,
  and `timestamp` arrays of PKCS8 Ed25519 PEM strings. Arrays support thresholds;
  the signer verifies authorization against the reviewed root. No root role is
  accepted by this interface. Keys are materialized only in a temporary private
  directory and removed at job exit; none enter artifacts or command output.
- Environment secrets `CATALOG_R2_ACCESS_KEY_ID` and
  `CATALOG_R2_SECRET_ACCESS_KEY`: dedicated object read/write credentials scoped
  as narrowly as R2 permits to the existing download bucket, not account-admin
  credentials. The script only writes `vastora/catalog/`; this is not a claim
  that R2 credentials themselves enforce a per-prefix boundary.
- Environment variables `CATALOG_R2_BUCKET_NAME` (the existing `download`
  bucket) and `CATALOG_CLOUDFLARE_ACCOUNT_ID`.

The GitHub record is `catalog-r<N>`, explicitly a prerelease and never marked
Latest. Before any R2 write, its draft asset `catalog-publication.json` retains
the exact signed files, full app-version content history, reviewed commit,
workflow URL and target hash. It is read back via authenticated GitHub access
before upload. The ledger is not reconstructed from CDN contents. Keep these
records/assets permanently, protect catalog tags, and restrict repository write
access; a repository administrator can still damage this publishing ledger.
Public TUF signatures remain the client's trust boundary, not the GitHub asset.
The completed ledger asset is public in this repository; "protected ledger"
means authenticated, controlled writes, not confidential storage. It contains
only public signed metadata and publication history, never credentials.

After uploading immutable files and conditionally activating the timestamp,
the verifier uses the independently reviewed initial root against the public
HTTPS endpoint. Only then does the draft become a completed catalog release.
All root versions remain available to clients that missed intermediate updates.

On a recoverable failure, **rerun the original workflow run** (same commit and revision).
The publisher loads the saved signatures instead of signing again. If R2
activation succeeded but its response was lost, it compares exact saved bytes,
then repeats public verification without overwriting. A new dispatch from a
later `main` commit is not an exact retry of the earlier approved run.

If the pending signatures expired or the run cannot be resumed, dispatch a
**new higher revision** with `supersede=true` and `bootstrap=false`, and approve
that new run. It retains the pending drafts and all durable app-version history;
reserved revisions are never reused. Before activation, the observed R2
timestamp must exactly match a saved prior ledger, and the replacement still
uses its ETag. Supersession does not permit clobbering unknown storage bytes.
Its own retry uses the original saved signatures and supersession record.

GitHub can create a draft before its asset upload finishes. Explicit
supersession may skip this incomplete reservation only when the authenticated
GitHub asset inventory confirms there is no completed ledger asset. Failed
downloads, corrupt uploaded assets, and missing published records are not
treated as empty history. No R2 write happens before a complete ledger asset
has been saved and read back. Keep all draft records; do not delete a pending
ledger, replace an asset, or bypass conditional writes to force a retry.

The publication target and online metadata are valid for **7 days** by default
(the offline CLI permits a shorter period, with a maximum of 30 days).
An earlier trust-root expiry can shorten the usable window. Publish a fresh
revision before expiry, including when app versions have not changed; signatures are not
extended by a 304 or cache hit. Reusing existing app versions is allowed only
when their canonical contents remain identical, including removed/re-added
versions retained in the protected ledger. No automatic renewal job is created
by this change: renewal still requires publication approval. After expiry,
Center keeps displaying its trusted cache and managing installed applications,
but new installations and upgrades require a fresh verified revision.
