package controlplane

import "slices"

// NativeLifecycleAllowed is the narrow application lifecycle available to a
// container-free, restricted host. Actual catalog validation and the ordinary
// sealed execution authorization remain mandatory at their existing boundaries.
// This does not grant a node-wide Docker, gateway, tunnel or host-path ability.
func NativeLifecycleAllowed(runtimeKind string, version int, operation string, grants []string) bool {
	if (runtimeKind != "systemd" && runtimeKind != "native") || version != 2 {
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

// NativeWebsiteConfigureAllowed only admits the existing owned website backend.
// Center must first verify the prepared hostname migration. The Agent backend
// independently verifies its prepared journal, unchanged settings and secrets.
func NativeWebsiteConfigureAllowed(appKey, runtimeKind string, version int, operation string, grants []string) bool {
	return operation == "configure" && (appKey == "vastora-official/daylight" || appKey == "vastora-official/daylight-https") && slices.Contains(grants, "shared-caddy") && NativeLifecycleAllowed(runtimeKind, version, "upgrade", grants)
}
