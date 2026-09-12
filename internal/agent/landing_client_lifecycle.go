package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/petauron/vastora/internal/landing"
)

// Center only dispatches this command after every affected entry has applied
// its deny rules and terminated sessions. Persist the new plan before native
// writes; replay must not advance a reset baseline a second time.
func (s *Store) applyLandingParentMutation(ctx context.Context, baseURL, token string, command ThreeXUIClientCommandTask) error {
	if command.ManagedParentID == "" || command.OperationKey == "" || !slices.Contains([]string{"update", "set_enabled", "reset_traffic", "delete"}, command.Action) {
		return errors.New("agent: invalid shared account operation")
	}
	state, err := s.landingController(ctx)
	if err != nil || state == nil {
		return errors.New("agent: shared account journal is unavailable")
	}
	account, ok := state.Accounts[command.ManagedParentID]
	if !ok {
		return errors.New("agent: shared parent identity is unavailable")
	}
	intent := command
	intent.OperationKey = ""
	encoded, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(digest[:])
	if account.LastOperation == command.OperationKey {
		if account.OperationDigest != fingerprint {
			return errors.New("agent: shared operation content changed")
		}
		return nil
	}
	if account.PendingOperation != "" {
		if account.OperationDigest != fingerprint {
			return errors.New("agent: finish the pending shared account operation first")
		}
	} else {
		if account.Deleted || account.Email != command.Email {
			return errors.New("agent: shared parent ownership changed")
		}
		if command.Action != "delete" {
			if err := s.syncLandingAccount(ctx, baseURL, token, state, account.ID); err != nil {
				return err
			}
		}
		account = state.Accounts[account.ID]
		switch command.Action {
		case "update":
			if !clientInboundsAvailable(command.Inbounds, command.InboundIDs) {
				return errors.New("agent: selected entries are unavailable")
			}
			account.Email, account.Total, account.Expiry, account.ResetDays = command.NewEmail, command.TotalBytes, command.ExpiryTime, command.ResetDays
		case "set_enabled":
			account.Enabled = command.Enabled
		case "reset_traffic":
			account.Members = landing.ResetQuota(account.Members)
		case "delete":
			account.Enabled, account.Deleted = false, true
		}
		// Counter/ownership anomalies still fail the preceding sync.
		account.Blocked = false
		account.PendingOperation, account.OperationDigest = command.OperationKey, fingerprint
		state.Accounts[account.ID] = account
		if err := s.saveLandingController(ctx, state); err != nil {
			return err
		}
	}
	if command.Action == "delete" {
		names, err := landingAccountNames(state, account.ID)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(names))
		for id := range names {
			if id != account.ID {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		ids = append(ids, account.ID) // Parent last, after all owned shadows.
		for _, id := range ids {
			detail, found, err := findListedThreeXUIClient(ctx, baseURL, token, names[id])
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			if landing.Identity(clientJSONText(detail.Client, "id")) != id {
				return errors.New("agent: refusing to delete a replacement identity")
			}
			if err := deleteThreeXUIClientIfExists(ctx, baseURL, token, names[id]); err != nil {
				return err
			}
		}
		for id, grant := range state.Grants {
			if grant.Task.Grant.ParentID == account.ID {
				grant.Task.Grant.Enabled, grant.Phase = false, "revoked"
				grant.Material = landing.ControllerResult{}
				state.Grants[id] = grant
			}
		}
	} else {
		detail, email, err := getThreeXUIClientForUpdate(ctx, baseURL, token, command)
		if err != nil || landing.Identity(clientJSONText(detail.Client, "id")) != account.ID {
			return errors.New("agent: shared parent identity changed")
		}
		if command.Action == "update" {
			setClientJSONField(detail.Client, "email", account.Email)
			setClientJSONField(detail.Client, "limitIp", command.LimitIP)
			if err := updateLandingNativeClient(ctx, baseURL, token, email, detail.Client); err != nil {
				return err
			}
			observed, err := getThreeXUIClient(ctx, baseURL, token, account.Email)
			var limitIP int
			if err != nil || landing.Identity(clientJSONText(observed.Client, "id")) != account.ID || json.Unmarshal(observed.Client["limitIp"], &limitIP) != nil || limitIP != command.LimitIP {
				return errors.New("agent: shared account update was not confirmed")
			}
			if err := syncThreeXUIClientInbounds(ctx, baseURL, token, account.Email, observed.InboundIDs, command.InboundIDs); err != nil {
				return err
			}
			observed, err = getThreeXUIClient(ctx, baseURL, token, account.Email)
			if err != nil || !sameThreeXUIInboundIDs(observed.InboundIDs, command.InboundIDs) {
				return errors.New("agent: shared entry attachment was not confirmed")
			}
			for id, grant := range state.Grants {
				if grant.Task.Grant.ParentID == account.ID {
					grant.Task.Grant.BaseUser = account.Email
					state.Grants[id] = grant
				}
			}
			if err := s.saveLandingController(ctx, state); err != nil {
				return err
			}
		}
		if err := s.syncLandingAccount(ctx, baseURL, token, state, account.ID); err != nil {
			return err
		}
	}
	account = state.Accounts[account.ID]
	account.PendingOperation, account.LastOperation = "", command.OperationKey
	state.Accounts[account.ID] = account
	return s.saveLandingController(ctx, state)
}

// Child identities are implementation details, not additional editable/free
// clients. Present the durable aggregate and original parent plan instead of
// the native per-identity enforcement limits.
func (s *Store) projectLandingAccounts(ctx context.Context, clients []ThreeXUIClientView) ([]ThreeXUIClientView, error) {
	state, err := s.landingController(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ThreeXUIClientView, 0, len(clients))
	for _, client := range clients {
		if strings.HasPrefix(client.Email, "vastora-combination-") {
			continue
		}
		if state != nil {
			if account, ok := state.Accounts[client.ID]; ok {
				if account.Deleted {
					continue
				}
				_, used, err := landing.AllocateQuota(account.Total, account.Enabled, account.Members)
				if err != nil {
					return nil, err
				}
				client.HasLanding = true
				client.TotalBytes, client.UsedBytes, client.ExpiryTime, client.ResetDays = account.Total, used, account.Expiry, account.ResetDays
				client.Enabled = account.Enabled && !account.Blocked && account.PendingOperation == "" && (account.Expiry == 0 || account.Expiry > s.now().UnixMilli()) && (account.Total == 0 || used < account.Total)
			}
		}
		result = append(result, client)
	}
	return result, nil
}
