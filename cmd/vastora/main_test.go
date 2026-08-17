package main

import "testing"

func TestNodeRolesAndCapabilitiesAreIndependent(t *testing.T) {
	roles, err := parseNodeRoles("worker,gateway")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := parseNodeCapabilities("docker")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 2 || !containsValue(roles, "gateway") || !capabilities.Docker || capabilities.Gateway {
		t.Fatalf("roles and capabilities were not parsed independently: roles=%v capabilities=%#v", roles, capabilities)
	}
}

func TestUnimplementedCapabilitiesCannotBeAdvertised(t *testing.T) {
	for _, capability := range []string{"metrics", "logs"} {
		if _, err := parseNodeCapabilities(capability); err == nil {
			t.Fatalf("unimplemented capability %q was accepted", capability)
		}
	}
}
