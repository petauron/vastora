package nodeswap

import (
	"strings"
	"testing"
)

func TestTierForMemoryKB(t *testing.T) {
	p512 := TierForMemoryKB(475136)
	if p512.TierMiB != 512 || p512.ZramBytes != 256<<20 || p512.ZramAlgorithm != "lz4" || p512.ZramPriority != 100 || p512.SwapfileBytes != 512<<20 || p512.SwapfilePriority != -2 || p512.Swappiness != 60 {
		t.Fatalf("unexpected 512 MiB policy: %+v", p512)
	}
	p1024 := TierForMemoryKB(951296)
	if p1024.TierMiB != 1024 || p1024.ZramBytes != 256<<20 || p1024.Swappiness != 60 {
		t.Fatalf("unexpected 1 GiB policy: %+v", p1024)
	}
	p2048 := TierForMemoryKB(2020352)
	if p2048.TierMiB != 2048 || p2048.ZramBytes != 0 || p2048.ZramAlgorithm != "" || p2048.SwapfileBytes != 512<<20 || p2048.Swappiness != 10 {
		t.Fatalf("unexpected 2 GiB policy: %+v", p2048)
	}
	if p4g := TierForMemoryKB(4194304); p4g.TierMiB != 0 {
		t.Fatalf("expected 4 GiB host to be ineligible, got %+v", p4g)
	}
}

func TestTaskAndViewValidation(t *testing.T) {
	rev := strings.Repeat("a", 64)
	if err := (Task{Action: "inspect"}).Validate(); err != nil {
		t.Fatalf("inspect validate: %v", err)
	}
	if err := (Task{Action: "inspect", Revision: rev}).Validate(); err == nil {
		t.Fatal("expected inspect with revision to fail")
	}
	if err := (Task{Action: "apply"}).Validate(); err == nil {
		t.Fatal("expected apply without revision to fail")
	}
	if err := (Task{Action: "apply", Revision: rev}).Validate(); err != nil {
		t.Fatalf("apply validate: %v", err)
	}
	if err := (Task{Action: "restore", Revision: rev}).Validate(); err != nil {
		t.Fatalf("restore validate: %v", err)
	}

	eligible := View{
		State:                "eligible",
		Revision:             rev,
		MemoryTotalBytes:     512 << 20,
		MemoryAvailableBytes: 320 << 20,
		DiskFreeBytes:        4 << 30,
		Virtualization:       "kvm",
		Systemd:              true,
		Swappiness:           60,
		Target:               TierForMemoryKB(524288),
		CanApply:             true,
	}
	if err := eligible.Validate(); err != nil {
		t.Fatalf("eligible view validate: %v", err)
	}
	if err := eligible.ValidateFor(Task{Action: "apply", Revision: rev}); err == nil {
		t.Fatal("expected unapplied view to fail ValidateFor(apply)")
	}

	applied := eligible
	applied.State = "applied"
	applied.CanApply = false
	applied.CanRestore = true
	if err := applied.ValidateFor(Task{Action: "apply", Revision: rev}); err != nil {
		t.Fatalf("applied ValidateFor(apply): %v", err)
	}
}
