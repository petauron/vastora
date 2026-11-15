# Purging a superseded Meridian installation

This local administrator command permanently deletes one obsolete Meridian
installation and its legacy endpoint, counters, task history and unshared secrets.
It does not archive the deleted data, contact an Agent, uninstall anything, or
change Glivela's independent identity, configuration or traffic ledger.

Use it only after an existing execution-retirement record identifies a ready
native Glivela installation on the same active host. The current private Glivela
receipt must be fresh and ready. Unfinished work, cross-installation/shared references and unexpected
dependencies refuse the whole operation. When an obsolete task has any disposed
execution, every execution and event for that task remains unchanged as an
independent audit; original IDs, sealed envelopes and disposition actors are
retained, rather than reassigned to the replacement.
Shared accounts and every current Glivela service/publication are retained.
An old installation-owned service is removable only when all its publications
are stopped, withdrawal is complete and it has no gateway routes or unknown
references. Only its exact legacy projections, credentials and terminal
management records are removed. Historical route `enabled` flags do not grant
live authority after permanent business handover. A stale `running` label on
the old registration requires an existing verified execution-retirement proof;
it never authorizes stopping a running process. Inspect actual legacy resources
and current native identity before invoking the command.

During an explicitly authorized short management-service stop, first review the
rollback-only preview on the Center host using the same released
binary and state directory as Center:

```sh
vastora center purge-superseded-glivela-history \
  --data-dir /var/lib/vastora \
  --application-id OLD_INSTALLATION_ID \
  --replacement-id CURRENT_INSTALLATION_ID \
  --confirm-name 'EXACT HOST NAME'
```

After explicitly approving the permanent deletion, repeat with `--commit`.
The command uses the existing transaction-scoped frozen-history deletion
mechanism. Only exact validated primary keys are eligible; insert/update guards
stay active and the original delete guards are restored before commit. Foreign
keys remain enabled. Any failure rolls back all changes, including guard changes.
A successful commit is not a runtime restart or a new application deployment.
Resume Center immediately after the operation, including on error. Inspect
`deleted` and `preservedExecutions` in the output, verify the original disposed
audit set and current runtime identities, then test the published proxy paths.
No database schema, signing/catalog or Agent upgrade is involved.
