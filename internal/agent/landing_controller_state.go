package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/secret"
)

// The controller journal contains child credentials and native subscription
// tokens. Like the route checkpoint it is encrypted before any upstream write.
type landingControllerGrant struct {
	Task              landing.ControllerTask   `json:"task"`
	Phase             string                   `json:"phase"`
	ChildSubscription string                   `json:"childSubscription"`
	Material          landing.ControllerResult `json:"material"`
}

type landingControllerAccount struct {
	ID                string                 `json:"id"`
	Email             string                 `json:"email"`
	SubscriptionToken string                 `json:"subscriptionToken"`
	Mode              landing.PublishingMode `json:"mode"`
	Enabled           bool                   `json:"enabled"`
	Total             int64                  `json:"total"`
	Expiry            int64                  `json:"expiry"`
	ResetDays         int                    `json:"resetDays"`
	Members           []landing.QuotaMember  `json:"members"`
	Limits            []landing.QuotaLimit   `json:"limits"`
	PendingLimits     []landing.QuotaLimit   `json:"pendingLimits,omitempty"`
	PendingExpiry     int64                  `json:"pendingExpiry,omitempty"`
	Blocked           bool                   `json:"blocked"`
	Deleted           bool                   `json:"deleted,omitempty"`
	PendingOperation  string                 `json:"pendingOperation,omitempty"`
	OperationDigest   string                 `json:"operationDigest,omitempty"`
	LastOperation     string                 `json:"lastOperation,omitempty"`
}

type landingControllerState struct {
	ControllerID string                              `json:"controllerId"`
	Grants       map[string]landingControllerGrant   `json:"grants"`
	Accounts     map[string]landingControllerAccount `json:"accounts"`
}

func (s *Store) landingController(ctx context.Context) (*landingControllerState, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM landing_controller_state WHERE id=1`).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("agent: cannot read landing account journal")
	}
	plain, err := secret.Open(s.key, sealed, []byte("agent-landing-controller"))
	if err != nil {
		return nil, errors.New("agent: cannot decrypt landing account journal")
	}
	var state landingControllerState
	if json.Unmarshal(plain, &state) != nil || state.ControllerID == "" || state.Grants == nil || state.Accounts == nil {
		return nil, errors.New("agent: invalid landing account journal")
	}
	installation, err := s.AppliedInstallation(ctx, threeXUIKey)
	if err != nil || installation.ApplicationID != state.ControllerID {
		return nil, errors.New("agent: subscription controller identity changed")
	}
	for id, g := range state.Grants {
		if id != g.Task.Grant.ID || g.Task.Grant.Validate() != nil || g.Task.Revision == 0 || g.Task.ControllerID != state.ControllerID || landing.Identity(g.Task.FixedUUID) != g.Task.Grant.FixedIdentity || g.ChildSubscription == "" {
			return nil, errors.New("agent: invalid landing account ownership")
		}
	}
	return &state, nil
}

// All callers hold the existing application/landing mutation lock.
func (s *Store) saveLandingController(ctx context.Context, state *landingControllerState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return errors.New("agent: cannot encode landing account journal")
	}
	sealed, err := secret.Seal(s.key, data, []byte("agent-landing-controller"))
	if err != nil {
		return errors.New("agent: cannot encrypt landing account journal")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO landing_controller_state(id,sealed_state) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET sealed_state=excluded.sealed_state`, sealed)
	return err
}
