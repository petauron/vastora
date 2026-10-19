# Managed service handover

This generic installation-ownership operation accepts existing service IDs,
not caller-selected endpoints or commands. It is limited to the same node/site,
protocol, forwarding mode and management boundary. No listener or access policy
is added. It is not a channel for application business configuration.

1. Back up the platform database/key and both applications' private state.
   Stop/freeze the previous writer using its own maintenance flow. Resolve
   active/unknown installation or command outcomes explicitly; handover does
   not cancel them, acknowledge success or delete historical evidence.
2. Install and independently verify the replacement application with retained
   data. Its target service must have a ready catalog-derived endpoint, owned
   resource receipt and no publication. Check its actual backend first.
   Installation status alone is not application/route acceptance.
3. As administrator, request
   `GET /api/v1/services/{sourceServiceID}/handover?targetServiceId={targetServiceID}`.
   Review the source/target fingerprints and publication count.
4. Submit those exact `sourceSha256`, `targetSha256`, `targetServiceId` and
   `backupsConfirmed: true` to `POST /api/v1/services/{sourceServiceID}/handover`.
   Changed ownership/resources/services/publications invalidate the preview.
   Active or uncertain maintenance/adoption/deployment/command work blocks it.
5. The transaction preserves the original service/display-name/publication/
   route IDs, hostname, DNS/access references and TLS boundary. It transfers
   that service to the target's verified application/endpoint. The unpublished
   installation-created target row remains stopped history. Only this service's
   published bindings are requeued. An identical lost-response retry does not
   requeue or append another audit event. The new metadata event is not a
   successful old task, nor proof of a working remote application.
6. The response is `binding-updated-verification-pending`, never healthy.
   Verify the actual retained HTTPS URL/path/token, applied gateway revision,
   application identities/counters and fresh application route evidence before
   retiring old resources. A controlled maintenance interruption is possible;
   zero downtime and existing TCP-session preservation are not promised.

Prepare the new independent application and final frozen-data adoption before
retiring an embedded origin. Keep shared HAProxy and unrelated SNI routes. Do
not replace the public hostname, issue new tokens, rewrite history or keep an
old business writer alive through aliases. Restored snapshots need fresh health.

Anonymous regressions exercise real certificate-verified HTTPS at an unchanged
URL across the generic backend switch. The application separately verifies its
actual subscription bytes and traffic metadata after private restoration and
refuses downloads before fresh health. These checks do not replace installed
HAProxy/application acceptance. Investigate failed binding/verification and use
explicit matched recovery; no automatic database downgrade is supported.
