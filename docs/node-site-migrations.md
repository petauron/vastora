# Managed website hostname migration

A website hostname is an installed runtime input. A DNS record alone does not
authorize changing it, and a normal native service-port update cannot rename it.

The migration uses the existing Daylight application, shared Caddy transaction,
Center certificate issuer and authenticated Agent task channel. It does not
create another website process or transfer provider credentials to an Agent.

1. Prepare the exact new hostname with the installed hostname as a precondition.
   Center issues its independent certificate. The Agent retains the installed
   site and prepares the new hostname on the same owned listeners and content.
2. Change only the installed `hostname` through the normal configuration task.
   Both Center and Agent require the prepared migration, unchanged package,
   permissions and other settings. The existing website handover journal owns
   rollback; the old site remains available while clients refresh subscriptions.
3. Verify the public website, REALITY and formal subscription using the new SNI.
   Explicitly accept the website migration to retire its previous binding.
   Preparation, configuration and acceptance are separate operations.

Preparing or accepting a website never enables a paused subscription, changes a
Glivela identity, or edits account and traffic data. Public ingress routing must
also be ready before changing REALITY. Old DNS/publications are retained until
their clients and recovery dependencies have been checked.

Only one migration per application may be pending. Recipient, application,
hostname, certificate and revision are bound to the normal execution journal.
Lost responses replay the same operation; unresolved executions prevent another
migration. Failed preparation keeps the original site. An interrupted handover
uses the existing recovery journal instead of replacing application state by
guessing. Schema changes are forward-only.
