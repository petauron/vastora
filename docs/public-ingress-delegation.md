# Website TLS delegation and dual-stack DNS

A managed node site may explicitly set `tlsLoopbackOnly: true` through its
existing authenticated certificate configuration API. This creates a forward
revision, reuses the current managed certificate and retains content plus the
private TLS backend. It releases only the exact registered public TCP TLS binding for an
external protocol owner such as REALITY. Verify that owner and its HTTPS fallback
before applying. A gateway on another public address remains intact. A different
hostname sharing the upstream does not authorize delegation. Another public
website on the delegated socket or a separately owned shared frontend
prevents delegation; never delete foreign listeners to make a bind succeed.
Content refresh, service resume, native/OpenRC and Docker reconciliation preserve
the selected mode. Setting false in a new revision restores the retained binding.
UDP443 and other service ports are not changed.

Managed-domain input may specify `secondaryAddress`, a confirmed opposite-family
public address of the same enrolled node. On an existing ready resource, changing
it requires the exact hostname and expected revision. Omission retains the old
secondary; an explicit empty string removes it. Primary family migration requires
removing the secondary first. Both A/AAAA records have independent tracked provider
identities under the same managed-domain owner. Partial provider failures do not
advance the ready revision; retries recover only exact owned records. DNS changes
do not themselves configure application listeners or change SNI/subscriptions.

Schema133 is a forward-only migration preserving existing certificates, keys and
managed-domain records. Back up a live deployment consistently before migration.
See Glivela's public-ingress443 operation for the separate proxy transition and
identity-preserving forward recovery. Deployment is a separately authorized step.
