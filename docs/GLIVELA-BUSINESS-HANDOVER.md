# Final Glivela business handover

A Center with retained Glivela accounts/endpoints must complete the one-time
offline adoption before serving this release. Fresh platform installations do
not require a handover. This is a forward-only schema-121 transition; do not
start an older Center against the migrated database.

Back up the platform data and existing controller state first. Keep proxy node
processes and their subscription credentials unchanged. Stop the old Center and
disable its restart policy, then use the released Glivela `legacy-export`
`capture-final`, export, stage and `adopt-final` commands. Preparation copies are
not final captures. Use a separate private Glivela storage key. See the Glivela
repository's `tools/legacy-export/README.md` for checked command arguments.

The adopter verifies the still-stopped exact writer and final database/key hashes,
commits owned encrypted business data and the enrolled-node transition, then
writes a private receipt into the original Center data directory. On first open,
Center checks the pre-migration standalone database against that receipt. The
migration retains all original rows and installs permanent write fences on the
retired business tables. Later restarts compare the recorded receipt, not an old
whole-file hash. Ordinary platform writes and generic Agent heartbeat/task
selection continue; legacy proxy commands and business projections are fenced.
No legacy HTTP subscription/business API remains as an alternate authority.

Upgrade the managed controller through the signed application catalog. Preserve
existing service/publication identities and the enrolled controller address,
install the adopted private state and activate the persistent bootstrap. Do not
invent installed versions, mark observations healthy or replay enrollment merely
to expose the management UI. Check actual node receipts and fresh/reused client
requests through the unchanged public subscription after activation.

Acceptance must compare final persisted accounts, credential identities,
subscription tokens, traffic watermarks and reset cycles against the adopted
material. Verify all full desired documents, revisions and node certificates
remain unchanged. Small unsampled tails follow the existing MVP policy; never
reset counters or manufacture samples. Preserve the stopped-writer final backup
and interrupted checkpoints for explicit recovery. No automatic downgrade or
replay of an older subscription snapshot is permitted.
