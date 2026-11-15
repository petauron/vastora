package controlplane

import "testing"

func TestNativeWebsiteConfigureDoesNotExpandRestrictedLifecycle(t *testing.T) {
	grants := []string{"native-services", "shared-caddy"}
	for _, app := range []string{"vastora-official/daylight", "vastora-official/daylight-https"} {
		if !NativeWebsiteConfigureAllowed(app, "systemd", 2, "configure", grants) {
			t.Fatal("owned website configure refused")
		}
	}
	for _, tc := range []struct {
		app, kind, operation string
		version              int
		grants               []string
	}{
		{"other/app", "systemd", "configure", 2, grants},
		{"vastora-official/daylight", "docker", "configure", 2, grants},
		{"vastora-official/daylight", "native", "configure", 1, grants},
		{"vastora-official/daylight", "native", "execute", 2, grants},
		{"vastora-official/daylight", "native", "configure", 2, []string{"native-services"}},
		{"vastora-official/daylight", "native", "configure", 2, []string{"native-services", "shared-caddy", "host-path"}},
	} {
		if NativeWebsiteConfigureAllowed(tc.app, tc.kind, tc.version, tc.operation, tc.grants) {
			t.Fatal("restricted boundary expanded", tc.app)
		}
	}
	if NativeLifecycleAllowed("native", 2, "configure", grants) {
		t.Fatal("general configure opened")
	}
}
