package nodeprotocol

import "testing"

func TestSelectionRequiresAtLeastOneProtocol(t *testing.T) {
	for _, selection := range []Selection{{VLESS: true}, {HY2: true}, {VLESS: true, HY2: true}} {
		if err := selection.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if (Selection{}).Validate() == nil {
		t.Fatal("empty protocol selection accepted")
	}
	if HY2Tag("vastora-node") != "vastora-node-hy2" {
		t.Fatal("unstable sibling identity")
	}
}
