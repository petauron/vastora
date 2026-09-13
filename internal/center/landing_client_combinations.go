package center

import (
	"context"
	"errors"
	"net/http"

	"github.com/petauron/vastora/internal/landing"
)

type LandingClientCombinationsInput struct {
	ParentID            string                     `json:"parentId"`
	ServiceID           string                     `json:"serviceId"`
	Targets             []LandingCombinationTarget `json:"targets"`
	ConfirmSessionReset bool                       `json:"confirmSessionReset"`
}

type LandingCombinationTarget struct {
	LandingNodeID string `json:"landingNodeId"`
	Revision      uint64 `json:"revision"`
}

// Add all requested combinations atomically. The first entry check fences
// pre-existing commands; subsequent checks share that entry and transaction,
// so commands just queued by this request are not treated as competing work.
func (s *Store) ConfigureClientLandingCombinations(ctx context.Context, input LandingClientCombinationsInput) ([]LandingClientGrantView, error) {
	if input.ParentID == "" || input.ServiceID == "" || !input.ConfirmSessionReset || len(input.Targets) == 0 || len(input.Targets) > 32 {
		return nil, errors.New("center: select an entry and landing servers and confirm the change")
	}
	seen := map[string]bool{}
	for _, target := range input.Targets {
		if target.LandingNodeID == "" || seen[target.LandingNodeID] {
			return nil, errors.New("center: invalid or duplicate landing server")
		}
		seen[target.LandingNodeID] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := make([]LandingClientGrantView, 0, len(input.Targets))
	for i, target := range input.Targets {
		view, err := s.configureClientLanding(ctx, tx, LandingClientGrantInput{
			ParentID: input.ParentID, ServiceID: input.ServiceID,
			LandingNodeID: target.LandingNodeID, Revision: target.Revision,
			Mode: landing.FixedMode, Enabled: true, ConfirmSessionReset: true,
		}, i == 0)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Server) handleLandingClientCombinations(w http.ResponseWriter, r *http.Request) {
	var input LandingClientCombinationsInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	views, err := s.store.ConfigureClientLandingCombinations(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, views)
}
