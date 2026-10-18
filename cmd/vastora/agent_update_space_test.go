package main

import "testing"

func TestAgentUpdateSpaceBudgetRejectsShortfall(t *testing.T) {
	for _, required := range []int64{-1, 0, 101} {
		if err := validateAgentUpdateSpace(100, required); err == nil {
			t.Fatalf("accepted invalid space budget %d", required)
		}
	}
	if err := validateAgentUpdateSpace(100, 100); err != nil {
		t.Fatal(err)
	}
}
