package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strconv"
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
			if err := updateLandingNativeClient(ctx, baseURL, token, email, detail.Client, detail.InboundIDs); err != nil {
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

type nativeSubscriptionMutation struct {
	action   string
	email    string
	newEmail string
}

func nativeSubscriptionMutationForCommand(command ThreeXUIClientCommandTask) *nativeSubscriptionMutation {
	switch command.Action {
	case "create", "update", "set_enabled", "reset_traffic", "delete":
		return &nativeSubscriptionMutation{action: command.Action, email: command.Email, newEmail: command.NewEmail}
	default:
		return nil
	}
}

func (mutation *nativeSubscriptionMutation) accepts(client ThreeXUIClientView, prior landingNativeSubscription, hasPrior bool) bool {
	if mutation == nil {
		return false
	}
	switch mutation.action {
	case "create":
		return client.Email == mutation.newEmail && (!hasPrior || prior.Email == mutation.newEmail)
	case "update":
		// An update may rename or change the plan, but it must address the
		// already journaled identity. Treating a missing prior ID as a new
		// subscription would let a concurrent controller-side UUID rotation
		// replace Vastora's authoritative credential during a rename.
		return hasPrior && client.Email == mutation.newEmail && prior.Email == mutation.email
	case "set_enabled", "reset_traffic":
		return hasPrior && prior.Email == mutation.email && client.Email == mutation.email
	default:
		return false
	}
}

func (mutation *nativeSubscriptionMutation) deletes(subscription landingNativeSubscription) bool {
	return mutation != nil && mutation.action == "delete" && subscription.Email == mutation.email
}

// Child identities are implementation details, not additional editable/free
// clients. Present the durable aggregate and original parent plan instead of
// the native per-identity enforcement limits.
func (s *Store) projectLandingAccounts(ctx context.Context, baseURL, token string, inbounds []ThreeXUIClientInbound, clients []ThreeXUIClientView, mutation *nativeSubscriptionMutation) ([]ThreeXUIClientView, error) {
	state, err := s.landingController(ctx)
	if err != nil {
		return nil, err
	}
	if state == nil {
		installation, err := s.AppliedInstallation(ctx, threeXUIKey)
		if err != nil {
			return nil, errors.New("agent: subscription controller is unavailable")
		}
		state = &landingControllerState{ControllerID: installation.ApplicationID, Grants: map[string]landingControllerGrant{}, Accounts: map[string]landingControllerAccount{}, Subscriptions: map[string]landingNativeSubscription{}}
	}
	if state.Subscriptions == nil {
		state.Subscriptions = map[string]landingNativeSubscription{}
	}
	// This durable marker, not an empty map, is the one-time migration boundary.
	// An empty authoritative set is valid and must remain empty after migration.
	initialImport := !state.AuthorityInitialized
	previousAuthorityInitialized := state.AuthorityInitialized
	previousSubscriptions, _ := json.Marshal(state.Subscriptions)
	priorIdentityByEmail := make(map[string]string, len(state.Subscriptions))
	for id, subscription := range state.Subscriptions {
		priorIdentityByEmail[subscription.Email] = id
	}
	result := make([]ThreeXUIClientView, 0, len(clients))
	observedSubscriptions := map[string]bool{}
	resolvedInbounds, err := resolveNativeSubscriptionInbounds(ctx, baseURL, token, inbounds)
	if err != nil {
		return nil, err
	}
	for _, client := range clients {
		if strings.HasPrefix(client.Email, "vastora-combination-") {
			continue
		}
		detail, detailErr := getThreeXUIClient(ctx, baseURL, token, client.Email)
		observedID := landing.Identity(clientJSONText(detail.Client, "id"))
		if client.ID == "" {
			client.ID = observedID
		}
		if detailErr != nil || observedID == "" || observedID != client.ID {
			return nil, errors.New("agent: native subscription identity inventory is incomplete")
		}
		if previousID := priorIdentityByEmail[client.Email]; previousID != "" && previousID != client.ID {
			return nil, errors.New("agent: native subscription identity changed; explicit reconciliation required")
		}
		subscriptionToken := clientJSONText(detail.Client, "subId")
		if client.HasSubscription != (subscriptionToken != "") {
			return nil, errors.New("agent: native subscription token inventory is incomplete")
		}
		if subscriptionToken == "" {
			return nil, errors.New("agent: native client has no Vastora subscription identity")
		}
		prior, hasPriorSubscription := state.Subscriptions[client.ID]
		acceptObservedPlan := initialImport || mutation.accepts(client, prior, hasPriorSubscription)
		acceptObservedCredentials := initialImport || acceptObservedPlan && mutation != nil && (mutation.action == "create" || mutation.action == "update")
		if !hasPriorSubscription && !acceptObservedPlan {
			return nil, errors.New("agent: unmanaged native client observed; explicit reconciliation required")
		}
		if hasPriorSubscription && !acceptObservedPlan && prior.Email != client.Email {
			return nil, errors.New("agent: native subscription display identity changed; explicit reconciliation required")
		}
		if hasPriorSubscription {
			// After first import Vastora owns the public token. A later 3x-ui
			// edit cannot silently rotate an already distributed subscription.
			if subscriptionToken != prior.Token {
				return nil, errors.New("agent: native subscription token changed; explicit reconciliation required")
			}
			subscriptionToken = prior.Token
		}
		if subscriptionToken != "" {
			links, linkErr := nativeSubscriptionLinks(inbounds, resolvedInbounds, detail)
			if linkErr != nil {
				return nil, linkErr
			}
			if hasPriorSubscription && !sameNativeSubscriptionCredentials(prior.Links, links, acceptObservedCredentials) {
				return nil, errors.New("agent: native subscription credentials changed; explicit reconciliation required")
			}
			if hasPriorSubscription && !acceptObservedPlan {
				prior.Used = client.UsedBytes
				state.Subscriptions[client.ID] = prior
			} else if hasPriorSubscription && mutation != nil && mutation.action == "set_enabled" {
				prior.Enabled, prior.Used = client.Enabled, client.UsedBytes
				state.Subscriptions[client.ID] = prior
			} else if hasPriorSubscription && mutation != nil && mutation.action == "reset_traffic" {
				prior.Used = client.UsedBytes
				state.Subscriptions[client.ID] = prior
			} else {
				state.Subscriptions[client.ID] = landingNativeSubscription{
					ID: client.ID, Email: client.Email, Token: subscriptionToken, Enabled: client.Enabled,
					Total: client.TotalBytes, Used: client.UsedBytes, Expiry: client.ExpiryTime, ResetDays: client.ResetDays, Links: links,
				}
			}
			observedSubscriptions[client.ID] = true
		}
		if authoritative, ok := state.Subscriptions[client.ID]; ok {
			client.Email, client.Enabled = authoritative.Email, authoritative.Enabled
			client.TotalBytes, client.UsedBytes = authoritative.Total, authoritative.Used
			client.ExpiryTime, client.ResetDays = authoritative.Expiry, authoritative.ResetDays
			client.HasSubscription = authoritative.Token != ""
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
	for id, subscription := range state.Subscriptions {
		if !observedSubscriptions[id] && mutation.deletes(subscription) {
			delete(state.Subscriptions, id)
		}
	}
	state.AuthorityInitialized = true
	currentSubscriptions, _ := json.Marshal(state.Subscriptions)
	if previousAuthorityInitialized != state.AuthorityInitialized || !bytes.Equal(previousSubscriptions, currentSubscriptions) {
		if err := s.saveLandingController(ctx, state); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func sameNativeSubscriptionCredentials(previous, observed []string, allowRouteChange bool) bool {
	byRoute := make(map[string]string, len(observed))
	for _, raw := range observed {
		link, err := url.Parse(raw)
		if err != nil || link.User == nil {
			return false
		}
		key := link.User.Username() + "\x00" + link.Host
		link.Fragment = ""
		if _, exists := byRoute[key]; exists {
			return false
		}
		byRoute[key] = link.String()
	}
	if !allowRouteChange && len(previous) != len(observed) {
		return false
	}
	for _, raw := range previous {
		link, err := url.Parse(raw)
		if err != nil || link.User == nil {
			return false
		}
		key := link.User.Username() + "\x00" + link.Host
		link.Fragment = ""
		current, ok := byRoute[key]
		if ok && current != link.String() || !allowRouteChange && !ok {
			return false
		}
	}
	return true
}

func resolveNativeSubscriptionInbounds(ctx context.Context, baseURL, token string, inbounds []ThreeXUIClientInbound) (map[int]threeXUIRealityInbound, error) {
	resolved := make(map[int]threeXUIRealityInbound, len(inbounds))
	for _, ref := range inbounds {
		if ref.ID <= 0 || ref.VLESSDisabled || ref.ConnectHostname == "" || ref.InboundTag == "" {
			continue
		}
		inbound, err := getThreeXUIInbound(ctx, baseURL, token, ref.ID)
		if err != nil || !inbound.Enable || inbound.Protocol != "vless" || inbound.Tag != ref.InboundTag {
			return nil, errors.New("agent: native subscription entry is unavailable")
		}
		resolved[ref.ID] = inbound
	}
	return resolved, nil
}

func nativeSubscriptionLinks(inbounds []ThreeXUIClientInbound, resolved map[int]threeXUIRealityInbound, detail threeXUIClientDetail) ([]string, error) {
	byID := make(map[int]ThreeXUIClientInbound, len(inbounds))
	for _, inbound := range inbounds {
		if _, ok := resolved[inbound.ID]; ok && inbound.ID > 0 && !inbound.VLESSDisabled && inbound.ConnectHostname != "" {
			byID[inbound.ID] = inbound
		}
	}
	ids := slices.Clone(detail.InboundIDs)
	sort.Ints(ids)
	links := make([]string, 0, len(ids))
	for _, id := range ids {
		ref, ok := byID[id]
		if !ok {
			continue
		}
		inbound, ok := resolved[id]
		if !ok {
			return nil, errors.New("agent: native subscription entry is unavailable")
		}
		link, err := realityClientLinkFromInbound(inbound, ref.ConnectHostname, clientJSONText(detail.Client, "email"))
		if err != nil {
			return nil, errors.New("agent: native subscription credential is unavailable")
		}
		parsed, err := url.Parse(link)
		if err != nil {
			return nil, errors.New("agent: native subscription credential is invalid")
		}
		name := strings.TrimSpace(ref.DisplayName)
		if name == "" {
			name = strings.TrimSpace(ref.NodeName)
		}
		if name == "" {
			return nil, errors.New("agent: native subscription entry name is unavailable")
		}
		parsed.Fragment = name
		links = append(links, parsed.String())
	}
	return links, nil
}

func nativeSubscriptionInbounds(ctx context.Context, baseURL, token string) ([]ThreeXUIClientInbound, error) {
	observed, err := listRealityInbounds(ctx, baseURL, token)
	if err != nil {
		return nil, errors.New("agent: native subscription entry inventory is unavailable")
	}
	result := make([]ThreeXUIClientInbound, 0, len(observed))
	for _, inbound := range observed {
		if !inbound.Enable || inbound.Protocol != "vless" || inbound.ID < 1 || inbound.Tag == "" {
			continue
		}
		groups, err := threeXUIRealityHostGroups(ctx, baseURL, token, inbound.ID)
		if err != nil {
			return nil, errors.New("agent: native subscription host inventory is unavailable")
		}
		groupID := "vastora-public-" + strconv.Itoa(inbound.ID)
		for _, group := range groups {
			if group.GroupID != groupID || group.IsDisabled || group.IsHidden || len(group.Hosts) != 1 || !validThreeXUIShareHostname(group.Hosts[0]) || group.Port != 443 {
				continue
			}
			result = append(result, ThreeXUIClientInbound{ID: inbound.ID, DisplayName: inbound.Remark, NodeName: inbound.Remark, ConnectHostname: group.Hosts[0], InboundTag: inbound.Tag})
			break
		}
	}
	return result, nil
}

func (s *Store) refreshNativeSubscriptions(ctx context.Context, baseURL, token string) error {
	inbounds, err := nativeSubscriptionInbounds(ctx, baseURL, token)
	if err != nil {
		return err
	}
	clients, err := listThreeXUIClients(ctx, baseURL, token)
	if err != nil {
		return errors.New("agent: native subscription client inventory is unavailable")
	}
	_, err = s.projectLandingAccounts(ctx, baseURL, token, inbounds, clients, nil)
	return err
}
