package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/realitytarget"
)

func suggestRealityTargets(ctx context.Context, publicAddress string) ([]realitytarget.Candidate, error) {
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	values := []realitytarget.Candidate{}
	for _, hostname := range realitytarget.Hosts() {
		wg.Go(func() {
			probe, stop := context.WithTimeout(ctx, 60*time.Second)
			defer stop()
			value, err := realityTargetVerifier(probe, hostname, hostname, publicAddress)
			if err != nil {
				return
			}
			// Repeat the pinned handshake, not DNS. A transient single success
			// is not enough to advertise this address as a stable suggestion.
			for sample := 1; sample < 3; sample++ {
				started := time.Now()
				if verifyPinnedRealityTLS(probe, &value) != nil {
					return
				}
				value.LatencyMillis += max(1, time.Since(started).Milliseconds())
			}
			value.Samples = 3
			value.LatencyMillis /= 3
			mu.Lock()
			values = append(values, value)
			mu.Unlock()
		})
	}
	wg.Wait()
	if len(values) == 0 {
		return nil, errors.New("agent: no reviewed REALITY target passed this node's checks; select another independent .com target")
	}
	realitytarget.Rank(values)
	return values, nil
}
