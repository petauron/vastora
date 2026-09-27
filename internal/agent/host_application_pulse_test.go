package agent

import (
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/pulse"
)

func TestPulseSupportedHostReleases(t *testing.T) {
	for _, value := range []string{"debian:12", "debian:13", "ubuntu:24.04", "ubuntu:26.04"} {
		id, version, _ := strings.Cut(value, ":")
		if !pulseSupportedOS([]byte("ID=" + id + "\nVERSION_ID=\"" + version + "\"\n")) {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"ID=debian\nVERSION_ID=11", "ID=ubuntu\nVERSION_ID=22.04", "ID=alpine\nVERSION_ID=3.23"} {
		if pulseSupportedOS([]byte(value)) {
			t.Fatal("unsupported runtime accepted")
		}
	}
}

func TestPulseRetainsOnlySupportedLocationOverrides(t *testing.T) {
	base := pulse.AgentConfig{ServiceURL: "https://pulse.example.com/", ServiceApplicationID: "monitor", NodeName: "node"}
	for _, environment := range []string{
		"PULSE_NODE_REGION=SG\nPULSE_GEOIP_PROVIDER=disabled\n",
		"PULSE_NODE_REGION=\"SG\"\nPULSE_GEOIP_PROVIDER='disabled'\n",
	} {
		retained, err := pulseRetainLocation(base, []byte(environment+"PULSE_ENROLLMENT_TOKEN=must-not-be-copied\nOTHER=ignored\n"))
		if err != nil {
			t.Fatal(err)
		}
		output := string(pulseEnvironment(retained))
		if !strings.Contains(output, "PULSE_NODE_REGION=\"SG\"\n") || !strings.Contains(output, "PULSE_GEOIP_PROVIDER=\"disabled\"\n") || strings.Contains(output, "must-not-be-copied") || strings.Contains(output, "OTHER=") {
			t.Fatal("location preservation copied unsupported data or lost explicit settings")
		}
	}
	region, provider := "", "ipinfo"
	config := base
	config.NodeRegion, config.GeoIPProvider = &region, &provider
	retained, err := pulseRetainLocation(config, []byte("PULSE_NODE_REGION=SG\nPULSE_GEOIP_PROVIDER=disabled\n"))
	if err != nil || retained.NodeRegion == nil || *retained.NodeRegion != "" || retained.GeoIPProvider == nil || *retained.GeoIPProvider != "ipinfo" {
		t.Fatal("explicit administrator change did not override retained location")
	}
	for _, environment := range []string{
		"PULSE_NODE_REGION=SG\nPULSE_NODE_REGION=US\n",
		"PULSE_NODE_REGION=\"SG\n",
		"PULSE_NODE_REGION=$(id)\n",
		"PULSE_GEOIP_PROVIDER=untrusted\n",
		strings.Repeat("x", 16*1024+1),
	} {
		if _, err := pulseRetainLocation(base, []byte(environment)); err == nil {
			t.Fatal("invalid retained environment accepted")
		}
	}
}
