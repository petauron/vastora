package agent

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

type PublicEgressObserver func(context.Context, []networking.Candidate, time.Time) (*networking.PublicEgress, error)
type publicEgressDetector func(context.Context, []networking.Candidate, time.Time) (*networking.PublicEgress, error)

// NewPublicEgressObserver observes the public address once for this Agent
// process. Restarting the Agent starts a new observation.
func NewPublicEgressObserver(client *http.Client) PublicEgressObserver {
	return newStartupPublicEgressObserver(func(ctx context.Context, candidates []networking.Candidate, now time.Time) (*networking.PublicEgress, error) {
		return networking.DetectPublicEgress(ctx, client, candidates, now)
	})
}

func newStartupPublicEgressObserver(detect publicEgressDetector) PublicEgressObserver {
	var mu sync.Mutex
	var attempted bool
	var cached *networking.PublicEgress
	return func(ctx context.Context, candidates []networking.Candidate, now time.Time) (*networking.PublicEgress, error) {
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
		value, err := detect(ctx, candidates, now)
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
