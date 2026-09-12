# Agent task receipts on small nodes

Task receipts are a durable execution journal and completion outbox, not access
logs. The Agent records intent before applying an external effect, persists its
result locally, and acknowledges it only after Center accepts the result. Keep
this state: deleting the database to reduce I/O loses duplicate-delivery and
interrupted-operation safeguards.

## Bounded steady-state work

- Pending completions use an ordered partial index containing only `completed`
  and `reconciliation_required` receipts.
- The startup fence uses a separate ordered partial index for unresolved
  `application.apply` and `legacy` receipts. Acknowledged history is excluded
  from both hot indexes; completion payloads are not copied into indexes.
- Task acquisition no longer sweeps historical receipts. After completion
  delivery and startup recovery, the task loop attempts maintenance at most
  once every five minutes per Store, including after a failed cleanup attempt.
- Maintenance uses an indexed, atomic DELETE of at most 128 oldest eligible
  receipts, with a two-second context timeout. It does not read payloads into Go
  memory, repeatedly drain a backlog, or run periodic VACUUM. Failures are
  reported but do not fail the next task solely because cleanup failed.
- The retention window remains 30 days. Only acknowledged receipts and the
  existing abandoned `agent.update` processing case are eligible. Pending
  completions, unresolved application/legacy work, and acknowledged
  reconciliation fences are never removed by maintenance.

Cleanup is best-effort, so retained history can exceed 30 days during an outage
or while a backlog drains. Restarting the Agent allows one new bounded batch.
SQLite can reuse freed pages; the database file does not immediately shrink.
There is no enlarged page cache, full-history memory cache, added polling
goroutine, or new dependency.

## Upgrade and verification

Agent schema 19 adds the three indexes with a forward-only transaction. Existing
schema-18 databases are snapshotted first, including committed WAL data, under
`<data-dir>/schema-18-backup-*/agent.db` in a private directory. Backup or migration
failure aborts opening the Store. New databases receive the same indexes.
Existing encrypted results and task identities are not rewritten or discarded.

The snapshot and index creation incur one-time upgrade I/O and require spare
disk space. Retain the original `agent.key` with the backup. Do not automatically
start an older Agent against a schema-19 database or roll back only the database
while external task effects remain applied.

Regression tests in `internal/agent/task_receipts_test.go` cover query plans with
historical payloads, ordering, batch/interval bounds, concurrent maintenance,
retention fences, failure throttling, WAL-aware migration, rollback on migration
failure, and completion replay after reopening. These tests must pass before
release. After deployment, compare physical device I/O and Agent cgroup I/O over
equivalent intervals; query-plan improvements alone do not establish an actual
memory or throughput reduction on a production node.

The index approach follows SQLite's [partial-index support](https://www.sqlite.org/partialindex.html).
Query-plan tests check the pinned SQLite dependency for an index access path and
absence of a temporary sorting tree, as described in [EXPLAIN QUERY PLAN](https://www.sqlite.org/eqp.html);
production code does not parse diagnostic plan text.
