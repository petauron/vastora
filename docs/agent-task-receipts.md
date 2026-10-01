# Agent legacy task evidence

## Current execution model

Center owns durable task authorizations, results and operator disposition.
Agent keeps only the current execution in memory. It does not write new task
receipts, poll a completion outbox, periodically clean receipt history or replay
management commands after a restart. See [the execution contract](agent-execution-stop-on-error.md).

## One-time cutover evidence

The old `task_receipts` schema remains readable only to transfer unresolved
pre-cutover evidence. The Agent decrypts and validates one record at a time,
submits it through the authenticated Center API, and verifies the returned
archive identity and digest before retiring that exact local record. A changed
record, damaged ciphertext or mismatched acknowledgement stops the transfer.
Temporary transport failure may retry this idempotent archival operation; it
never reexecutes the associated business command.

Center stores the original evidence encrypted. Unknown or failed work remains
blocked until an administrator confirms the old execution has stopped and
records an explicit disposition. Missing or superseded business records can be
archived without recreating tasks. Secrets must not appear in ordinary lists,
logs or errors.

## Migration and preservation

The historical schema 18 to 19 migration is forward-only and still runs for
existing databases. It first snapshots the database including committed WAL
under `<data-dir>/schema-18-backup-*/agent.db`, then adds the historical indexes
in a transaction. Backup or migration failure aborts opening the Store. These
indexes do not imply that the old polling or cleanup implementation still runs.

Keep `agent.key` with the backup and preserve identity, installed application
state, configuration journals and protected update recovery points. Do not
delete `agent.db`, discard unresolved evidence or start an older binary against
a newer schema to reduce I/O.

## Regression coverage

- `task_receipt_schema_test.go`: WAL-aware backup, archive preservation and
  migration failure without partial state.
- `legacy_receipt_export_test.go`: evidence export without replay and rejection
  of damaged ciphertext.
- `legacy_receipt_transfer_test.go`: acknowledgement identity/digest validation,
  changed evidence and retained state on failure.
- Center `execution_legacy_*_test.go`: encrypted archival, lost acknowledgement,
  administrator disposition, exact-attempt fencing and audit atomicity.

These checks establish evidence handling, not a measured production memory or
physical I/O improvement. Full-daemon low-memory qualification is deferred by
the agreed MVP scope.
