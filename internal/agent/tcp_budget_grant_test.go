package agent

import (
	"github.com/petauron/catalog/catalog"
	"testing"
)

func TestTCPBudgetGrantRequiresReviewedManager(t *testing.T) {
	for _, v := range []struct {
		key, id, kind, namespace string
		version                  int
		want                     bool
	}{
		{"vastora-official/glivela-node", "glivela-node", "systemd", "glivela", 2, true},
		{"vastora-official/glivela-entry", "glivela-entry", "native", "glivela", 2, true},
		{"vastora-official/glivela-dual", "glivela-dual", "systemd", "glivela", 2, true},
		{"untrusted/glivela-node", "glivela-node", "native", "glivela", 2, false},
		{"vastora-official/meridian", "meridian", "native", "glivela", 2, false},
		{"vastora-official/glivela-node", "glivela-node", "docker", "glivela", 2, false},
		{"vastora-official/glivela-node", "glivela-node", "native", "other", 2, false},
	} {
		a := catalog.AppManifest{ID: v.id, Runtime: &catalog.RuntimeSpec{Kind: v.kind, Version: v.version, NativeServices: &catalog.NativeServicesRuntime{Namespace: v.namespace}}}
		if got := budgetManifest(v.key, a); got != v.want {
			t.Fatalf("grant %s/%s/%s: %v", v.key, v.kind, v.namespace, got)
		}
	}
}
