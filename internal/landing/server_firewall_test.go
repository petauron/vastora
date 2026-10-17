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

func TestServerFirewallRemovalUsesFreshSnapshot(t *testing.T) {
	for _, lostReply := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "lost-reply"}[lostReply], func(t *testing.T) {
			policy := testServerFirewall()
			foreign := []map[string]nftObject{
				{"table": {"family": "ip", "name": "filter"}},
				{"chain": {"family": "ip", "table": "filter", "name": "output"}},
				{"rule": {"family": "ip", "table": "filter", "chain": "output", "expr": []any{nftObject{"accept": nil}}}},
			}
			document := nftDocument{Objects: append(policy.objects(), foreign...)}
			writes := 0
			run := func(_ context.Context, input []byte, _ ...string) ([]byte, error) {
				if input == nil {
					return json.Marshal(document)
				}
				writes++
				document.Objects = foreign
				if lostReply {
					return nil, errors.New("lost delete reply")
				}
				return nil, nil
			}
			if err := policy.remove(context.Background(), run); err != nil {
				t.Fatal("confirmed removal rejected", err)
			}
			if err := policy.remove(context.Background(), run); err != nil || writes != 1 {
				t.Fatalf("removal retry: %v, writes %d", err, writes)
			}
		})
	}
}

func TestServerFirewallRemovalRejectsRemainingOrChangedPolicy(t *testing.T) {
	for _, drift := range []bool{false, true} {
		policy := testServerFirewall()
		document := nftDocument{Objects: policy.objects()}
		writes := 0
		if drift {
			document.Objects[0]["table"]["comment"] = "foreign"
		}
		run := func(_ context.Context, input []byte, _ ...string) ([]byte, error) {
			if input != nil {
				writes++
				return nil, nil // Simulated delete which did not take effect.
			}
			return json.Marshal(document)
		}
		if err := policy.remove(context.Background(), run); err == nil {
			t.Fatal("unconfirmed or foreign policy removal accepted")
		}
		if drift && writes != 0 {
			t.Fatal("foreign policy was modified")
		}
	}
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
