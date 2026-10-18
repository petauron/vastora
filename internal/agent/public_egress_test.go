package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestPublicEgressObserverCachesWithinFreshnessWindow(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	calls := 0
	observer := newPublicEgressObserver(func(_ context.Context, _ string, _ bool, candidates []networking.Candidate, observedAt time.Time) (*networking.PublicEgress, error) {
		calls++
		return &networking.PublicEgress{Address: "198.51.100.20", BindAddress: candidates[0].Address, Mode: networking.PublicModeNAT, ObservedAt: observedAt}, nil
	})
	first, err := observer(context.Background(), "https://helper.example.com/network/public-address", false, []networking.Candidate{{Address: "10.0.0.20", Kind: networking.KindLAN}}, now)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := observer(context.Background(), "https://helper.example.com/network/public-address", false, []networking.Candidate{{Address: "10.0.0.21", Kind: networking.KindLAN}}, now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first.ObservedAt != now || cached.ObservedAt != now || cached.BindAddress != "10.0.0.20" {
		t.Fatalf("calls=%d first=%#v cached=%#v", calls, first, cached)
	}
}

func TestPublicEgressObserverRetriesFailedObservationAfterCooldown(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	calls := 0
	observer := newPublicEgressObserver(func(_ context.Context, _ string, _ bool, _ []networking.Candidate, _ time.Time) (*networking.PublicEgress, error) {
		calls++
		return nil, errors.New("reflector unavailable")
	})
	if value, err := observer(context.Background(), "https://helper.example.com/network/public-address", false, nil, now); value != nil || err == nil {
		t.Fatalf("first observation = %#v, %v", value, err)
	}
	if value, err := observer(context.Background(), "https://helper.example.com/network/public-address", false, nil, now.Add(30*time.Second)); value != nil || err != nil || calls != 1 {
		t.Fatalf("cached failure = %#v, %v calls=%d", value, err, calls)
	}
	if value, err := observer(context.Background(), "https://helper.example.com/network/public-address", false, nil, now.Add(time.Minute)); value != nil || err == nil || calls != 2 {
		t.Fatalf("retry = %#v, %v calls=%d", value, err, calls)
	}
}

func TestPublicEgressObserverRefreshesBeforeCenterEvidenceExpires(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	calls := 0
	fail := false
	observer := newPublicEgressObserver(func(_ context.Context, _ string, _ bool, _ []networking.Candidate, stamp time.Time) (*networking.PublicEgress, error) {
		calls++
		if fail {
			return nil, errors.New("reflector unavailable")
		}
		return &networking.PublicEgress{Address: "1.1.1.1", BindAddress: "1.1.1.1", Mode: networking.PublicModeDirect, ObservedAt: stamp}, nil
	})
	observe := func(offset time.Duration) (*networking.PublicEgress, error) {
		return observer(context.Background(), "https://helper.example.test/network/public-address", false, nil, now.Add(offset))
	}
	first, err := observe(0)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	refreshed, err := observe(5 * time.Minute)
	if err != nil || calls != 2 || refreshed.ObservedAt != now.Add(5*time.Minute) {
		t.Fatalf("evidence did not refresh: %v calls=%d", err, calls)
	}
	fail = true
	value, err := observe(10 * time.Minute)
	if err == nil || value != nil {
		t.Fatal("failed refresh returned fabricated fresh evidence")
	}
	value, err = observe(10*time.Minute + 30*time.Second)
	if err != nil || value != nil || calls != 3 {
		t.Fatal("failed refresh ignored cooldown")
	}
	fail = false
	value, err = observe(11 * time.Minute)
	if err != nil || value == nil || calls != 4 || value.ObservedAt != now.Add(11*time.Minute) {
		t.Fatal("refresh did not recover after cooldown")
	}
	// A consumer mutating the returned value must not change cached proof.
	value.ObservedAt = now.Add(time.Hour)
	value, err = observe(12 * time.Minute)
	if err != nil || value.ObservedAt != now.Add(11*time.Minute) || calls != 4 {
		t.Fatal("cached proof was mutated")
	}
}
