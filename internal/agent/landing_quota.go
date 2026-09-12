package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func (s *Store) syncLandingAccount(ctx context.Context, baseURL, token string, state *landingControllerState, parentID string) error {
	account, ok := state.Accounts[parentID]
	if !ok {
		return errors.New("agent: shared traffic account is unavailable")
	}
	if account.Deleted {
		return nil
	}
	if len(account.PendingLimits) > 0 {
		if err := s.applyLandingQuotaPlan(ctx, baseURL, token, state, parentID); err != nil {
			return err
		}
		account = state.Accounts[parentID]
	}
	clients, err := listThreeXUIClients(ctx, baseURL, token)
	if err != nil {
		return errors.New("agent: shared traffic inventory is unavailable")
	}
	byEmail := map[string]ThreeXUIClientView{}
	for _, client := range clients {
		byEmail[client.Email] = client
	}
	names, err := landingAccountNames(state, parentID)
	if err != nil {
		return err
	}
	counters := map[string]int64{}
	for id, email := range names {
		client, found := byEmail[email]
		if !found {
			return s.blockLandingAccount(ctx, baseURL, token, state, parentID)
		}
		detail, err := getThreeXUIClient(ctx, baseURL, token, email)
		if err != nil || landing.Identity(clientJSONText(detail.Client, "id")) != id {
			return s.blockLandingAccount(ctx, baseURL, token, state, parentID)
		}
		counters[id] = client.UsedBytes
		if !slices.ContainsFunc(account.Members, func(m landing.QuotaMember) bool { return m.ID == id }) {
			account.Members = append(account.Members, landing.QuotaMember{ID: id})
			account.Limits = append(account.Limits, landing.QuotaLimit{ID: id, Total: client.TotalBytes, Enabled: client.Enabled})
		}
	}
	account.Members, err = landing.ObserveQuota(account.Members, counters)
	if err != nil {
		return s.blockLandingAccount(ctx, baseURL, token, state, parentID)
	}
	for i := range account.Members {
		member := &account.Members[i]
		member.Active = member.ID == parentID
		for _, grant := range state.Grants {
			if grant.Task.Grant.ParentID == parentID && grant.Task.Grant.FixedIdentity == member.ID {
				member.Active = (grant.Phase == "ready" || grant.Phase == "activating") && grant.Task.Grant.Enabled && grant.Task.Grant.Mode.Fixed()
			}
		}
	}
	now := s.now().UTC()
	if account.Enabled && !account.Blocked && account.ResetDays > 0 && account.Expiry > 0 && account.Expiry <= now.UnixMilli() {
		period := int64(account.ResetDays) * int64(24*time.Hour/time.Millisecond)
		account.Expiry += ((now.UnixMilli()-account.Expiry)/period + 1) * period
		account.Members = landing.ResetQuota(account.Members)
	}
	enabled := account.Enabled && !account.Blocked && (account.Expiry == 0 || account.Expiry > now.UnixMilli())
	account.PendingLimits, _, err = landing.AllocateQuota(account.Total, enabled, account.Members)
	if err != nil {
		return err
	}
	account.PendingExpiry = account.Expiry
	state.Accounts[parentID] = account
	if err := s.saveLandingController(ctx, state); err != nil {
		return err
	}
	return s.applyLandingQuotaPlan(ctx, baseURL, token, state, parentID)
}

func landingAccountNames(state *landingControllerState, parentID string) (map[string]string, error) {
	account, ok := state.Accounts[parentID]
	if !ok || account.ID != parentID || account.Email == "" {
		return nil, errors.New("agent: invalid shared account ownership")
	}
	result := map[string]string{parentID: account.Email}
	for _, grant := range state.Grants {
		if grant.Task.Grant.ParentID != parentID {
			continue
		}
		// Advanced-only grants never create a child. A formerly fixed child
		// remains accounted for after its mode changes or it is revoked.
		known := slices.ContainsFunc(account.Members, func(member landing.QuotaMember) bool { return member.ID == grant.Task.Grant.FixedIdentity })
		if (grant.Phase == "ready" || grant.Phase == "activating") && grant.Task.Grant.Mode.Fixed() || known {
			result[grant.Task.Grant.FixedIdentity] = grant.Task.Grant.FixedUser
		}
	}
	return result, nil
}

func (s *Store) applyLandingQuotaPlan(ctx context.Context, baseURL, token string, state *landingControllerState, parentID string) error {
	account := state.Accounts[parentID]
	names, err := landingAccountNames(state, parentID)
	if err != nil {
		return err
	}
	previous := map[string]landing.QuotaLimit{}
	for _, limit := range account.Limits {
		previous[limit.ID] = limit
	}
	targets := slices.Clone(account.PendingLimits)
	if account.Blocked {
		for i := range targets {
			targets[i].Enabled = false
		}
	}
	// Confirm all budget reductions before making any released budget
	// spendable by another identity. Replay uses the same persisted targets.
	for _, decreaseOnly := range []bool{true, false} {
		for _, limit := range targets {
			email, ok := names[limit.ID]
			if !ok {
				return errors.New("agent: pending budget identity is unavailable")
			}
			old, ok := previous[limit.ID]
			if !ok {
				return errors.New("agent: previous budget is unavailable")
			}
			decrease := !limit.Enabled || limit.Total > 0 && (old.Total == 0 || limit.Total < old.Total)
			if decreaseOnly != decrease {
				continue
			}
			detail, err := getThreeXUIClient(ctx, baseURL, token, email)
			var actualTotal int64
			if err != nil || landing.Identity(clientJSONText(detail.Client, "id")) != limit.ID || json.Unmarshal(detail.Client["totalGB"], &actualTotal) != nil || actualTotal != old.Total && actualTotal != limit.Total {
				return errors.New("agent: native shared quota changed outside the saved plan")
			}
			setClientJSONField(detail.Client, "totalGB", limit.Total)
			setClientJSONField(detail.Client, "enable", limit.Enabled)
			expiry := account.PendingExpiry
			if !limit.Enabled {
				expiry = 1
			}
			setClientJSONField(detail.Client, "expiryTime", expiry)
			setClientJSONField(detail.Client, "reset", 0)
			if err := updateLandingNativeClient(ctx, baseURL, token, email, detail.Client); err != nil {
				return err
			}
			observed, err := getThreeXUIClient(ctx, baseURL, token, email)
			var enabled bool
			var total, observedExpiry int64
			if err != nil || landing.Identity(clientJSONText(observed.Client, "id")) != limit.ID || json.Unmarshal(observed.Client["totalGB"], &total) != nil || total != limit.Total || json.Unmarshal(observed.Client["enable"], &enabled) != nil || enabled != limit.Enabled || json.Unmarshal(observed.Client["expiryTime"], &observedExpiry) != nil || observedExpiry != expiry {
				return errors.New("agent: shared quota write was not confirmed")
			}
		}
	}
	account.Limits = targets
	account.PendingLimits, account.PendingExpiry = nil, 0
	state.Accounts[parentID] = account
	return s.saveLandingController(ctx, state)
}

// 3x-ui can save its controller DB while a worker is still pending. A DB
// read-back alone must never release quota for another credential to spend.
// Retrying the persisted write is safe, including after a lost response.
func updateLandingNativeClient(ctx context.Context, baseURL, token, email string, payload map[string]json.RawMessage) error {
	result, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/update/"+url.PathEscape(email), token, "application/json", payload)
	if err != nil {
		return errors.New("agent: shared account update requires confirmed retry")
	}
	if err := landingNativeWriteReady(result); err != nil {
		return err
	}
	return nil
}

func landingNativeWriteReady(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var status struct {
		NodePending bool `json:"nodePending"`
	}
	if json.Unmarshal(raw, &status) != nil || status.NodePending {
		return errors.New("agent: shared account is waiting for entry synchronization")
	}
	return nil
}

func (s *Store) blockLandingAccount(ctx context.Context, baseURL, token string, state *landingControllerState, parentID string) error {
	account := state.Accounts[parentID]
	account.Blocked = true
	state.Accounts[parentID] = account
	if err := s.saveLandingController(ctx, state); err != nil {
		return err
	}
	// Never mutate a replacement account merely because it reused a name.
	names, err := landingAccountNames(state, parentID)
	if err != nil {
		return err
	}
	for id, email := range names {
		detail, err := getThreeXUIClient(ctx, baseURL, token, email)
		if err != nil || landing.Identity(clientJSONText(detail.Client, "id")) != id {
			continue
		}
		setClientJSONField(detail.Client, "enable", false)
		setClientJSONField(detail.Client, "expiryTime", int64(1))
		setClientJSONField(detail.Client, "reset", 0)
		_, _ = threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/update/"+url.PathEscape(email), token, "application/json", detail.Client)
	}
	return errors.New("agent: shared account requires reconciliation; renewal is blocked")
}

func (s *Store) runLandingAccounts(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		request, cancel := context.WithTimeout(ctx, 30*time.Second)
		s.landingMutationMu.Lock()
		state, err := s.landingController(request)
		if err == nil && state != nil && len(state.Accounts) > 0 {
			baseURL, token, connectionErr := threeXUIClientAPIConnection(request, s)
			ids := make([]string, 0, len(state.Accounts))
			for id := range state.Accounts {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			for _, id := range ids {
				if state.Accounts[id].Deleted || state.Accounts[id].PendingOperation != "" {
					continue
				}
				syncErr := connectionErr
				if syncErr == nil {
					syncErr = s.syncLandingAccount(request, baseURL, token, state, id)
				}
				if syncErr != nil {
					err = errors.Join(err, syncErr)
				}
			}
		}
		s.landingMutationMu.Unlock()
		cancel()
		if err != nil && report != nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
