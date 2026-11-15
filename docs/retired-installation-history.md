# Purging a superseded Meridian installation

This local administrator command permanently deletes one obsolete Meridian
installation and its legacy endpoint, counters, task history and unshared secrets.
It does not archive the deleted data, contact an Agent, uninstall anything, or
change Glivela's independent identity, configuration or traffic ledger.

Use it only after an existing execution-retirement record identifies a ready
native Glivela installation on the same active host. The current private Glivela
receipt must be fresh and ready. Shared routes, unfinished work, already disposed
execution records and unexpected dependencies refuse the whole operation.
Shared accounts, services, publications and other installations are retained.

First review the rollback-only preview on the Center host using the same released
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
