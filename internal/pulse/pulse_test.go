package pulse

import "testing"

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
