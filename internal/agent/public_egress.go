package agent

import (
	"context"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

type PublicEgressObserver func(context.Context, string, bool, []networking.Candidate, time.Time) (*networking.PublicEgress, error)
type publicEgressDetector func(context.Context, string, bool, []networking.Candidate, time.Time) (*networking.PublicEgress, error)

// NewPublicEgressObserver observes the public address once for this Agent
// process. Restarting the Agent starts a new observation.
func NewPublicEgressObserver() PublicEgressObserver {
	return newStartupPublicEgressObserver(func(ctx context.Context, endpoint string, allowPrivate bool, candidates []networking.Candidate, now time.Time) (*networking.PublicEgress, error) {
		client, normalizedEndpoint, err := networking.PublicAddressHTTPClient(endpoint, allowPrivate)
		if err != nil {
			return nil, err
		}
		return networking.DetectPublicEgress(ctx, client, normalizedEndpoint, candidates, now)
	})
}

func newStartupPublicEgressObserver(detect publicEgressDetector) PublicEgressObserver {
	var mu sync.Mutex
	var attempted bool
	var cached *networking.PublicEgress
	return func(ctx context.Context, endpoint string, allowPrivate bool, candidates []networking.Candidate, now time.Time) (*networking.PublicEgress, error) {
		mu.Lock()
		defer mu.Unlock()
		if attempted {
			if cached == nil {
				return nil, nil
			}
			value := *cached
			return &value, nil
		}
		attempted = true
		value, err := detect(ctx, endpoint, allowPrivate, candidates, now)
		if err != nil {
			return nil, err
		}
		cached = value
		if value == nil {
			return nil, nil
		}
		result := *value
		return &result, nil
	}
}
