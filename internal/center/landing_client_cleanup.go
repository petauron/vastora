package center

import ()

// Called only after all entry fences AND the Agent's exact-identity native
// deletion have been confirmed. Task events remain the durable audit. Never
// use this for an offline/unconfirmed revocation: its deny must remain.
