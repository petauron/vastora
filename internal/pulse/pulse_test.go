package pulse

import (
	"encoding/json"
	"testing"
)

func TestCollectorRequiresCredentialFreeHTTPSRoot(t *testing.T) {
	for _, endpoint := range []string{"http://pulse.example.com/", "https://user:secret@pulse.example.com/", "https://pulse.example.com/?token=x", "https://pulse.example.com/path", "https://pulse.example.com/#fragment"} {
		if (AgentConfig{ServiceURL: endpoint, ServiceApplicationID: "service", NodeName: "node"}).Validate() == nil {
			t.Fatalf("accepted unsafe endpoint %s", endpoint)
		}
	}
	if err := (AgentConfig{ServiceURL: "https://pulse.example.com/", ServiceApplicationID: "service", NodeName: "node"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorLocationAndIntervalValidation(t *testing.T) {
	base := AgentConfig{ServiceURL: "https://pulse.example.com/", ServiceApplicationID: "service", NodeName: "node"}
	for _, code := range []string{"", "SG", "us", "HK"} {
		config := base
		config.NodeRegion = &code
		if err := config.Validate(); err != nil {
			t.Fatalf("valid region rejected: %q: %v", code, err)
		}
	}
	for _, code := range []string{"ZZ", "EU", "USA", "SG\n", "$(id)"} {
		config := base
		config.NodeRegion = &code
		if config.Validate() == nil {
			t.Fatalf("invalid region accepted: %q", code)
		}
	}
	for _, provider := range []string{"", "geojs", "ipinfo", "disabled"} {
		config := base
		config.GeoIPProvider = &provider
		if err := config.Validate(); err != nil {
			t.Fatalf("valid provider rejected: %q: %v", provider, err)
		}
	}
	provider := "https://arbitrary.example.com/"
	invalid := base
	invalid.GeoIPProvider = &provider
	if invalid.Validate() == nil {
		t.Fatal("arbitrary GeoIP provider accepted")
	}
	for _, interval := range []int{-1, 301} {
		invalid := base
		invalid.IntervalSeconds = interval
		if invalid.Validate() == nil {
			t.Fatalf("invalid interval accepted: %d", interval)
		}
	}
	for _, interval := range []int{0, 1, 3, 300} {
		valid := base
		valid.IntervalSeconds = interval
		if err := valid.Validate(); err != nil {
			t.Fatalf("valid interval rejected: %d: %v", interval, err)
		}
	}
}

func TestCollectorDistinguishesOmittedAndClearedLocation(t *testing.T) {
	var omitted, cleared AgentConfig
	if err := json.Unmarshal([]byte(`{}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"node_region":"","geoip_provider":"disabled"}`), &cleared); err != nil {
		t.Fatal(err)
	}
	if omitted.NodeRegion != nil || omitted.GeoIPProvider != nil || cleared.NodeRegion == nil || *cleared.NodeRegion != "" || cleared.GeoIPProvider == nil || *cleared.GeoIPProvider != "disabled" {
		t.Fatal("location presence was lost during JSON decoding")
	}
}
