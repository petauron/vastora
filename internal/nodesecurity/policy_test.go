package nodesecurity

import (
	"strings"
	"testing"
)

func fixturePolicy() Policy {
	return Policy{PublicInterfaces: []string{"eth0"}, ManagementSources: []string{"203.0.113.10/32", "2001:db8::10/128"}, SSHPorts: []int{2222}, PublicRules: []Rule{{"tcp", 443}, {"udp", 443}, {"udp", 41641}}}
}

func TestPolicyValidation(t *testing.T) {
	if err := fixturePolicy().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*Policy)
	}{
		{"missing-management", func(p *Policy) { p.ManagementSources = nil }},
		{"world-v4", func(p *Policy) { p.ManagementSources = []string{"0.0.0.0/0"} }},
		{"world-v6", func(p *Policy) { p.ManagementSources = []string{"::/0"} }},
		{"whole-private", func(p *Policy) { p.ManagementSources = []string{"100.64.0.0/10"} }},
		{"noncanonical-prefix", func(p *Policy) { p.ManagementSources = []string{"203.0.113.10/24"} }},
		{"mapped-v4", func(p *Policy) { p.ManagementSources = []string{"::ffff:203.0.113.1/128"} }},
		{"shell-interface", func(p *Policy) { p.PublicInterfaces = []string{"eth0;reboot"} }},
		{"private-interface", func(p *Policy) { p.PublicInterfaces = []string{"tailscale0"} }},
		{"ssh-open-world", func(p *Policy) { p.PublicRules = append(p.PublicRules, Rule{"tcp", 2222}) }},
		{"invalid-protocol", func(p *Policy) { p.PublicRules = []Rule{{"tcp;reboot", 443}} }},
		{"invalid-port", func(p *Policy) { p.SSHPorts = []int{65536} }},
		{"duplicate", func(p *Policy) { p.PublicInterfaces = []string{"eth0", "eth0"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := fixturePolicy()
			test.edit(&p)
			if p.Validate() == nil {
				t.Fatal("unsafe policy accepted")
			}
		})
	}
}

func TestReviewedRevisionAndBoundedActions(t *testing.T) {
	p := fixturePolicy()
	task := Task{Action: "apply", Policy: p, Revision: p.Digest()}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	task.Policy.SSHPorts = []int{22}
	result := View{State: "verified", Policy: fixturePolicy(), Revision: task.Revision}
	if result.ValidateFor(task) == nil {
		t.Fatal("changed preview policy accepted as matching evidence")
	}
	for _, action := range []string{"confirm", "disable"} {
		if (Task{Action: action}).Validate() == nil {
			t.Fatal("unbound action accepted")
		}
	}
	for _, address := range []string{"203.0.113.1;reboot", "-a", "hostname.example", "::ffff:203.0.113.1"} {
		if (Task{Action: "unban", Address: address, Revision: p.Digest()}).Validate() == nil {
			t.Fatal("unsafe unban accepted")
		}
	}
	if (Task{Action: "unban", Address: "203.0.113.2", Revision: p.Digest()}).Validate() != nil {
		t.Fatal("canonical unban rejected")
	}
	if (Task{Action: "shell"}).Validate() == nil {
		t.Fatal("untyped command accepted")
	}
}

func TestEvidenceCannotClaimProtectionWithoutPolicy(t *testing.T) {
	for _, state := range []string{"preview", "verified", "pending_confirmation"} {
		if (View{State: state}).Validate() == nil {
			t.Fatal("empty protection evidence accepted")
		}
	}
	if (View{State: "unconfigured", Reasons: []string{strings.Repeat("x", 257)}}).Validate() == nil {
		t.Fatal("oversized result accepted")
	}
}
