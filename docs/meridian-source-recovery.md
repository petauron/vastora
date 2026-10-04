# Meridian entry identity recovery

After an explicitly reenrolled node changes its managed private peer identity,
ordinary authenticated reports withdraw its old landing authorization. They never
replace the endpoint's pinned identity. Native runtime readiness alone does not
make the fixed landing routes ready.

Restore the native Meridian runtime first, settle any uncertain executions in
Activity, and let the affected landings acknowledge removal of the old source.
In Meridian's advanced entry settings, inspect the replacement identity and
confirm that this is the reinstalled node and the previous node/tasks stopped.

The admin-only `GET /api/v1/meridian/endpoints/{id}/source-recovery` returns the
complete peer identity fingerprints, addresses, endpoint revision, and report
time. Its corresponding CSRF-protected POST requires both reviewed fingerprints,
the exact endpoint revision, `confirmReplacement`, and `executionStopped`.
The transaction rechecks fresh authenticated evidence, node ownership, runtime
readiness, execution fences on the entry and affected landings, and successful
withdrawal receipts. Changed evidence requires a new inspection.

The operation updates only that endpoint's source pin and its affected landing
plans. Accounts, credentials, names, usage watermarks and unrelated entries are
preserved. The Activity record `meridian.source.recover` retains the operator,
old/new fingerprints, evidence time and stop confirmation; a successful record
means the identity was replaced, not that transport is already healthy.

Routes remain blocked until a new successful landing authorization receipt and
fresh entry-to-landing transport evidence arrive. No database migration or manual
database edit is involved. Removed, failed, or still-applying landing services
must be repaired through their existing management workflow before recovery.
