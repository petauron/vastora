package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestPublicEgressObserverDetectsOncePerProcess(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	calls := 0
	observer := newStartupPublicEgressObserver(func(_ context.Context, _ string, _ bool, candidates []networking.Candidate, observedAt time.Time) (*networking.PublicEgress, error) {
		calls++
		return &networking.PublicEgress{Address: "198.51.100.20", BindAddress: candidates[0].Address, Mode: networking.PublicModeNAT, ObservedAt: observedAt}, nil
	})
	first, err := observer(context.Background(), "https://helper.example.com/network/public-address", false, []networking.Candidate{{Address: "10.0.0.20", Kind: networking.KindLAN}}, now)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := observer(context.Background(), "https://helper.example.com/network/public-address", false, []networking.Candidate{{Address: "10.0.0.21", Kind: networking.KindLAN}}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first.ObservedAt != now || cached.ObservedAt != now || cached.BindAddress != "10.0.0.20" {
		t.Fatalf("calls=%d first=%#v cached=%#v", calls, first, cached)
	}
}

func TestPublicEgressObserverDoesNotRetryAFailedStartupObservation(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	calls := 0
	observer := newStartupPublicEgressObserver(func(_ context.Context, _ string, _ bool, _ []networking.Candidate, _ time.Time) (*networking.PublicEgress, error) {
		calls++
		return nil, errors.New("reflector unavailable")
	})
	if value, err := observer(context.Background(), "https://helper.example.com/network/public-address", false, nil, now); value != nil || err == nil {
		t.Fatalf("first observation = %#v, %v", value, err)
	}
	if value, err := observer(context.Background(), "https://helper.example.com/network/public-address", false, nil, now.Add(time.Hour)); value != nil || err != nil || calls != 1 {
		t.Fatalf("cached failure = %#v, %v calls=%d", value, err, calls)
	}
}
