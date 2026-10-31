package controlplane

// NativeLifecycleAllowed is the narrow application lifecycle available to a
// container-free, restricted host. Actual catalog validation and the ordinary
// sealed execution authorization remain mandatory at their existing boundaries.
// This does not grant a node-wide Docker, gateway, tunnel or host-path ability.
func NativeLifecycleAllowed(runtimeKind string, version int, operation string, grants []string) bool {
	if runtimeKind != "systemd" || version != 2 {
		return false
	}
	switch operation {
	case "install", "upgrade", "uninstall":
	default:
		return false
	}
	native := false
	for _, grant := range grants {
		switch grant {
		case "native-services":
			native = true
		case "root", "shared-caddy":
		default:
			return false
		}
	}
	return native
}
