# Archive superseded Glivela execution records

Legacy Docker proxy installations reused the `vastora-official/meridian` key
now used by the independent controller. Their old package version must not be
treated as a controller upgrade target. Current ownership receipts distinguish
the legacy `xray` component from the actual `controller` component.

Before archiving a record, an authorized operator must inspect the exact host:

1. Match the old and replacement application IDs to the same enrolled node and
   the current Center resource receipts. Check that no operation is active.
2. Confirm the current native entry and Xray services, controller receipt and
   actual proxy/subscription acceptance. Preserve credentials and accounting.
3. For a retained Docker source, verify its exact container ID, stopped state,
   zero PID and disabled restart policy. Verify the local `execution-retired`
   receipt and matching native `entry-source.json` / checkpoint checksum.
   Complete the existing local `agent retire-legacy-execution` operation first
   if the stopped source has not yet been marked retired; never edit receipts.
4. For a pending record left by an OS reinstall, verify that the old receipt and
   old container are both absent and that the existing native replacement is
   ready. Do not invent a handover receipt for a fresh installation.
5. Save a bounded operator evidence JSON with the exact Center receipt digests,
   inspected source state and a current verification time. It contains only
   identities, hashes and outcome flags, never credentials or proxy configuration.

After the normal immutable Center release and authorized upgrade, use:

```sh
vastora center retire-glivela-execution --data-dir /existing/center/state \
  --evidence-file /private/current-inspection.json --confirm
```

This local operator command accepts only matching, ready, same-node replacements
and inspections no older than 15 minutes. It records an explicit **operator
attestation**, not an authenticated Agent result or a restore rehearsal. The
new forward-only schema migration adds only the retirement evidence table; the
normal migration backup remains required.

Archiving hides the old execution from active installation/upgrade lists and
blocks its redeployment. It does not uninstall containers, remove services or
publications, alter subscription identities, change the live native runtime or
delete historical deployments/accounting. Retained stopped sources and their
recovery material remain available. Unverified/offline records stay separate
and cannot be upgraded into controllers.
