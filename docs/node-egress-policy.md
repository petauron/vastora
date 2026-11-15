# Retired Center native egress controls

Refs #977. The former node settings egress selector and
`/api/v1/nodes/{id}/egress-policy` API are removed. They targeted the retired
Center-owned Meridian runtime and selected a different core image. They do not
configure independent Glivela native entries.

Center requires offline business handover before serving a database containing
legacy proxy state. After handover, permanent database triggers forbid writes
to that state and legacy runtime task scheduling is disabled. Historical policy
rows and failed execution evidence remain retained; removing this obsolete
interface does not migrate, reset or delete them.

Glivela owns its runtime configuration and installed core compatibility. Changing
public entry addresses does not select an IPv4/IPv6 egress policy. This cleanup
does not introduce a replacement policy selector or enable any proxy operation.
