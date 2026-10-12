# Application startup and recovery

Managed services can perform a mandatory database backup or forward migration
before opening their listener. Agent startup readiness allows up to five minutes
per service. Connection attempts and HTTP health requests share that budget;
opening the TCP listener does not reset it. A shorter parent deadline or revoked
execution still cancels the wait. Timeout errors retain the last probe failure.
The existing health response policy is unchanged.

In Apps, **Review and recover** reads the selected deployment's latest execution
attempt directly. It does not retry a task, infer completion from node health,
or search only the first history page. A missing or mismatched execution fails
closed instead of falling back to the old reconciliation retry endpoint.

Recovery uses the same operator decision panel as Activity:

- Verify that the previous execution stopped and inspect actual resources.
- Supply verification notes before submitting a decision.
- Execute again only as an explicit new attempt, never replay the old authority.
- Confirm completed only when Center retains sufficient successful result evidence.
- Abandoning a task preserves history and does not undo existing server changes.

Already running, completed or disposed attempts do not offer another recovery
mutation. The server continues enforcing stale-attempt and authorization fences.
This change does not bypass migration backups, downgrade databases, automatically
upgrade applications or change signed catalog publication.

The required Alpha CI gate includes startup deadline/cancellation regressions,
exact task lookup and retained-evidence tests, and the Apps recovery UI tests.
These checks also remain part of the full Go and frontend suites.
