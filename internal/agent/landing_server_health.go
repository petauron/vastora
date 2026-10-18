package agent

import (
	"context"
	"os"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func (s *Store) observeNativeLandingHealth(ctx context.Context) *landing.ServerHealth {
	s.landingMutationMu.Lock()
	defer s.landingMutationMu.Unlock()
	journal, err := readLandingHostJournal()
	if err != nil || journal == nil || journal.Removed {
		return nil
	}
	health := &landing.ServerHealth{Revision: journal.Revision, CheckedAt: time.Now().UTC()}
	if journal.Policy == nil || len(journal.PendingFiles) != 0 || verifyLandingFiles(journal) != nil {
		return health
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if err = journal.Policy.Verify(ctx); err != nil {
		// The service files were verified as ours. A missing/changed kernel
		// guard must not leave the owned proxy serving while marked unhealthy.
		// Verification may have consumed its deadline. Give fail-closed
		// cleanup its own bounded budget, including during heartbeat shutdown.
		stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		defer stopCancel()
		_ = stopLandingService(stopCtx)
		return health
	}
	if _, err = landingCommand(ctx, "systemctl", "is-active", "--quiet", "vastora-landing.service"); err != nil {
		return health
	}
	listeners, err := os.ReadFile("/proc/net/tcp")
	health.Healthy = err == nil && landingListenerPresent(listeners, journal.Policy.Address, journal.UID)
	return health
}
