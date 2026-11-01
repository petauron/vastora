# Senlun native handover

The systemd v2 Senlun Agent recipe uses one application-owned account, one
collector and one persistent `agent-state` directory. The landing-only execution
boundary is unchanged; a v1 recipe is not made executable there.

A first installation requires either a one-time enrollment token or explicitly
provided private `existing_identity` JSON. Import checks protocol version, the
exact configured Service URL and bounded credential fields before installing
anything. Stop an independently installed collector first. Native resources are
installed without activation, the existing identity is placed in the newly owned
state directory, then the collector starts. No new monitoring node is created.
An import cannot replace an identity in an existing installation.

An authorized upgrade from a managed v1 Senlun Agent verifies the current
application ID, local historical manifest digest and owned resource receipt.
It preserves effective location settings, verifies the existing identity,
stops the original writer and retains one bounded private recovery archive of
its receipt, unit, configuration and identity. Reproducible binaries and the
monitoring Service database are not copied. The executor retires only verified
old runtime files; old persistent state remains separate from new state.
The normal native installer then owns the new account, files and unit, imports
the same identity before activation, and checks that the node ID is unchanged.
Partial effects retain review-required evidence; there is no automatic retry,
downgrade, token rotation or registration replacement.

The `senlun-v1-handover` recovery archive is for operator-reviewed source recovery,
not an ordinary native v2 restore target. Its original signed package is needed
if source runtime files must be reconstructed. After real deployment acceptance,
apply the authorized backup retention policy to the obsolete source state and
recovery records. Source/CI success alone is not monitoring acceptance.

Do not publish the matching v2 catalog recipe before delivering this executor.
For a rollout, verify each probe's actual binary version, monitoring identity,
continued samples and retained historical samples; offline nodes remain untouched.

For an operator-reviewed delivery when the release origin is unreachable, place
only the exact signed archive at
`<Agent data>/packages/<instance identity>/incoming/<archive SHA256>`.
The directory must be owned by the Agent operator and private (0700), and the
regular archive file must have that same owner with 0600 or 0400 permissions.
The executor rejects symlinks, changed files, oversize data and digest mismatches
without falling back to another input. The signed URL and digest remain required;
normal extraction, declared-member, ELF/architecture, resource ownership and
identity checks still run. No manifest or resource receipt is edited. Remove the
staged distribution archive after actual installation acceptance; it is not a
recovery backup. With no staged archive, normal verified HTTPS download applies.
