package landing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testServerFirewall() ServerFirewall {
	return ServerFirewall{Revision: 1, Address: "100.64.0.8", Sources: []string{"100.64.0.9"}, Interface: "tailscale0", UID: 990}
}

func TestServerFirewallRejectsUnscopedIdentity(t *testing.T) {
	for _, change := range []func(*ServerFirewall){
		func(p *ServerFirewall) { p.UID = 0 },
		func(p *ServerFirewall) { p.UID = 65534 },
		func(p *ServerFirewall) { p.Address = "0.0.0.0" },
		func(p *ServerFirewall) { p.Interface = "tailscale0; flush ruleset" },
		func(p *ServerFirewall) { p.Sources = []string{"100.64.0.0/10"} },
		func(p *ServerFirewall) { p.Sources = []string{p.Address} },
		func(p *ServerFirewall) { p.Sources = append(p.Sources, p.Sources[0]) },
	} {
		policy := testServerFirewall()
		change(&policy)
		if err := policy.validate(); err == nil {
			t.Fatal("unsafe firewall identity was accepted")
		}
	}
}

func TestServerFirewallConfirmsLostReplyWithoutReplacingDrift(t *testing.T) {
	policy := testServerFirewall()
	document := nftDocument{Objects: []map[string]nftObject{}}
	applies := 0
	run := func(_ context.Context, input []byte, _ ...string) ([]byte, error) {
		if input == nil {
			return json.Marshal(document)
		}
		applies++
		document.Objects = policy.objects()
		return nil, errors.New("simulated lost reply")
	}
	if err := policy.install(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := policy.install(context.Background(), run); err != nil || applies != 1 {
		t.Fatalf("idempotent apply: %v, writes %d", err, applies)
	}
	for _, object := range document.Objects {
		if chain := object["chain"]; chain != nil && chain["name"] == "output" {
			chain["prio"] = -400
		}
	}
	if err := policy.install(context.Background(), run); err == nil || applies != 1 {
		t.Fatalf("policy drift overwritten: %v, writes %d", err, applies)
	}
}

func TestServerFirewallRestrictsOnlyDedicatedService(t *testing.T) {
	policy := testServerFirewall()
	objects := policy.objects()
	table, _ := policy.identity()
	encoded, _ := json.Marshal(nftDocument{Objects: objects})
	var document nftDocument
	_ = json.Unmarshal(encoded, &document)
	if found, err := validateNFTPolicy(document, table, objects, false); err != nil || !found {
		t.Fatalf("rendered policy: %v", err)
	}
	data, _ := json.Marshal(objects)
	for _, required := range []string{`"skuid"`, `"reply"`, `"iifname"`, `"oifname"`, `"addr":"169.254.0.0"`, `"addr":"100.64.0.0"`} {
		if !strings.Contains(string(data), required) {
			t.Fatalf("missing boundary %s", required)
		}
	}
	for _, forbidden := range []string{`"flush"`, `"established"`, `"related"`, `"log"`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("unexpected shared policy bypass %s", forbidden)
		}
	}
}
