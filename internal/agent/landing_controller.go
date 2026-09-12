package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"

	"github.com/petauron/vastora/internal/landing"
)

func applyLandingClientCommand(ctx context.Context, store *Store, task landing.ControllerTask) (landing.ControllerResult, error) {
	result := landing.ControllerResult{GrantID: task.Grant.ID, Revision: task.Revision, Phase: task.Phase}
	if task.Grant.Validate() != nil || task.Revision == 0 || task.Phase != "retire" && task.InboundID <= 0 || task.ControllerID == "" || !task.Mode.Valid() || landing.Identity(task.FixedUUID) != task.Grant.FixedIdentity {
		return result, errors.New("agent: invalid landing account command")
	}
	installation, err := store.AppliedInstallation(ctx, threeXUIKey)
	if err != nil || installation.ApplicationID != task.ControllerID {
		return result, errors.New("agent: subscription controller changed")
	}
	state, err := store.landingController(ctx)
	if err != nil {
		return result, err
	}
	if state == nil {
		state = &landingControllerState{ControllerID: task.ControllerID, Grants: map[string]landingControllerGrant{}, Accounts: map[string]landingControllerAccount{}}
	}
	previous, exists := state.Grants[task.Grant.ID]
	if task.Phase == "retire" && exists {
		task.InboundID = previous.Task.InboundID
	}
	if exists && (previous.Task.Revision > task.Revision || previous.Task.Grant.ParentID != task.Grant.ParentID || previous.Task.FixedUUID != task.FixedUUID || previous.Task.InboundID != task.InboundID || previous.Task.Grant.InboundTag != task.Grant.InboundTag) {
		return result, errors.New("agent: stale or conflicting landing identity")
	}
	if task.Phase == "activate" && (!exists || previous.Task.Revision != task.Revision) {
		return result, errors.New("agent: landing identity has not been prepared")
	}
	baseURL, token, err := threeXUIClientAPIConnection(ctx, store)
	if err != nil {
		return result, err
	}
	if task.Phase == "retire" {
		if !exists {
			_, found, err := findListedThreeXUIClient(ctx, baseURL, token, task.Grant.FixedUser)
			if err != nil || found {
				return result, errors.New("agent: unjournaled child identity cannot be retired")
			}
			return result, nil
		}
		previous.Task, previous.Phase = task, "revoked"
		previous.Material = landing.ControllerResult{}
		state.Grants[task.Grant.ID] = previous
		if err := store.saveLandingController(ctx, state); err != nil {
			return result, err
		}
		if err := disableLandingChild(ctx, baseURL, token, previous); err != nil {
			return result, err
		}
		child, found, err := findListedThreeXUIClient(ctx, baseURL, token, task.Grant.FixedUser)
		if err != nil {
			return result, err
		}
		if found {
			if err := verifyLandingChild(child, previous); err != nil {
				return result, err
			}
			if previous.Task.InboundID > 0 {
				// Replay the journaled attachment even if the controller DB already
				// removed it before returning nodePending or losing its response.
				raw, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/"+url.PathEscape(task.Grant.FixedUser)+"/detach", token, "application/json", map[string]any{"inboundIds": []int{previous.Task.InboundID}})
				if err != nil {
					return result, errors.New("agent: child detachment requires confirmed retry")
				}
				if err := landingNativeWriteReady(raw); err != nil {
					return result, err
				}
			}
			observed, err := getThreeXUIClient(ctx, baseURL, token, task.Grant.FixedUser)
			if err != nil || verifyLandingChild(observed, previous) != nil || len(observed.InboundIDs) != 0 {
				return result, errors.New("agent: child detachment was not confirmed")
			}
		}
		// Keep a disabled record and its native counters. Removing it before
		// late node traffic is folded in would refund already consumed bytes.
		if _, ok := state.Accounts[task.Grant.ParentID]; ok {
			if err := store.syncLandingAccount(ctx, baseURL, token, state, task.Grant.ParentID); err != nil {
				// Confirmed revocation does not require a healthy traffic counter.
				// Preserve the ledger and block budget changes; never refund missing
				// counters or retain topology references forever because of them.
				account := state.Accounts[task.Grant.ParentID]
				account.Blocked = true
				state.Accounts[task.Grant.ParentID] = account
				if err := store.saveLandingController(ctx, state); err != nil {
					return result, err
				}
			}
		}
		return result, nil
	}
	parent, err := getThreeXUIClient(ctx, baseURL, token, task.Grant.BaseUser)
	if err != nil || landing.Identity(clientJSONText(parent.Client, "id")) != task.Grant.BaseIdentity || !slices.Contains(parent.InboundIDs, task.InboundID) {
		return result, errors.New("agent: parent identity or entry attachment changed")
	}
	inbound, err := getThreeXUIInbound(ctx, baseURL, token, task.InboundID)
	if err != nil || inbound.Protocol != "vless" || inbound.Tag != task.Grant.InboundTag {
		return result, errors.New("agent: landing entry ownership changed")
	}
	if _, ok := state.Accounts[task.Grant.ParentID]; !ok {
		var enabled bool
		var total, expiry int64
		var reset int
		if json.Unmarshal(parent.Client["enable"], &enabled) != nil || json.Unmarshal(parent.Client["totalGB"], &total) != nil || json.Unmarshal(parent.Client["expiryTime"], &expiry) != nil || json.Unmarshal(parent.Client["reset"], &reset) != nil || !enabled || total < 0 || expiry < 0 || reset < 0 {
			return result, errors.New("agent: parent traffic plan is unavailable")
		}
		// Journal the original plan during preparation, before the first child
		// exists. Cancellation or a parent change need not wait for activation.
		state.Accounts[task.Grant.ParentID] = landingControllerAccount{ID: task.Grant.ParentID, Email: task.Grant.BaseUser, SubscriptionToken: clientJSONText(parent.Client, "subId"), Mode: task.Mode, Enabled: enabled, Total: total, Expiry: expiry, ResetDays: reset, Members: []landing.QuotaMember{{ID: task.Grant.ParentID, Active: true}}, Limits: []landing.QuotaLimit{{ID: task.Grant.ParentID, Total: total, Enabled: enabled}}}
	}
	if task.Phase == "prepare" {
		if exists && previous.Task.Revision == task.Revision {
			old, next := previous.Task, task
			old.Phase, next.Phase = "", ""
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(next)
			if string(a) != string(b) {
				return result, errors.New("agent: landing revision content changed")
			}
			if previous.Phase == "ready" {
				return result, nil
			}
		}
		if !exists {
			previous.ChildSubscription, err = randomClientToken()
			if err != nil {
				return result, err
			}
		}
		previous.Task, previous.Phase = task, "prepared"
		previous.Material = landing.ControllerResult{}
		state.Grants[task.Grant.ID] = previous
		if err := store.saveLandingController(ctx, state); err != nil {
			return result, err
		}
		child, found, err := findListedThreeXUIClient(ctx, baseURL, token, task.Grant.FixedUser)
		if err != nil {
			return result, errors.New("agent: child inventory is unavailable")
		}
		if !found && task.Grant.Enabled && task.Grant.Mode.Fixed() {
			// Upstream add defaults Enable to true. An expired identity with no
			// inbound is safe even if that default is applied before read-back.
			payload := map[string]any{"client": map[string]any{"email": task.Grant.FixedUser, "id": task.FixedUUID, "subId": previous.ChildSubscription, "flow": "xtls-rprx-vision", "enable": false, "expiryTime": int64(1), "totalGB": int64(1), "reset": 0}, "inboundIds": []int{}}
			_, writeErr := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/add", token, "application/json", payload)
			child, err = getThreeXUIClient(ctx, baseURL, token, task.Grant.FixedUser)
			if err != nil {
				return result, errors.Join(errors.New("agent: child preparation was not confirmed"), safeLandingAPIError(writeErr))
			}
			found = true
		}
		if found {
			if err := verifyLandingChild(child, previous); err != nil {
				return result, err
			}
			if err := disableLandingChild(ctx, baseURL, token, previous); err != nil {
				return result, err
			}
			if task.Grant.Enabled && task.Grant.Mode.Fixed() {
				if err := syncThreeXUIClientInbounds(ctx, baseURL, token, task.Grant.FixedUser, child.InboundIDs, []int{task.InboundID}); err != nil {
					return result, errors.New("agent: child entry attachment was not confirmed")
				}
			}
		}
		return result, nil
	}
	if task.Phase != "activate" {
		return result, errors.New("agent: unsupported landing account phase")
	}
	previous.Task, previous.Phase = task, "activating"
	state.Grants[task.Grant.ID] = previous
	if err := store.saveLandingController(ctx, state); err != nil {
		return result, err
	}
	if err := store.syncLandingAccount(ctx, baseURL, token, state, task.Grant.ParentID); err != nil {
		return result, err
	}
	inbound, err = getThreeXUIInbound(ctx, baseURL, token, task.InboundID)
	if err != nil {
		return result, errors.New("agent: applied entry inventory is unavailable")
	}
	result.BaseLink, err = realityClientLinkFromInbound(inbound, task.ConnectHostname, task.Grant.BaseUser)
	if err != nil {
		return result, err
	}
	if task.Grant.Enabled && task.Grant.Mode.Fixed() {
		result.FixedLink, err = realityClientLinkFromInbound(inbound, task.ConnectHostname, task.Grant.FixedUser)
		if err != nil {
			return result, err
		}
	}
	result.SubscriptionToken, err = ensureThreeXUIClientSubscriptionID(ctx, baseURL, token, task.Grant.BaseUser)
	if err != nil {
		return result, errors.New("agent: native subscription identity is unavailable")
	}
	previous.Phase, previous.Material = "ready", result
	account := state.Accounts[task.Grant.ParentID]
	account.SubscriptionToken, account.Mode = result.SubscriptionToken, task.Mode
	state.Accounts[task.Grant.ParentID] = account
	state.Grants[task.Grant.ID] = previous
	if err := store.saveLandingController(ctx, state); err != nil {
		return result, err
	}
	return result, nil
}

func safeLandingAPIError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New("agent: upstream account operation failed")
}

func verifyLandingChild(child threeXUIClientDetail, grant landingControllerGrant) error {
	if landing.Identity(clientJSONText(child.Client, "id")) != grant.Task.Grant.FixedIdentity || clientJSONText(child.Client, "subId") != grant.ChildSubscription || clientJSONText(child.Client, "email") != grant.Task.Grant.FixedUser {
		return errors.New("agent: child identity ownership changed")
	}
	for _, id := range child.InboundIDs {
		if id != grant.Task.InboundID {
			return errors.New("agent: child is attached to an unexpected entry")
		}
	}
	return nil
}

func disableLandingChild(ctx context.Context, baseURL, token string, grant landingControllerGrant) error {
	child, found, err := findListedThreeXUIClient(ctx, baseURL, token, grant.Task.Grant.FixedUser)
	if err != nil {
		return errors.New("agent: child inventory is unavailable")
	}
	if !found {
		return nil
	}
	if err := verifyLandingChild(child, grant); err != nil {
		return err
	}
	setClientJSONField(child.Client, "enable", false)
	setClientJSONField(child.Client, "expiryTime", int64(1))
	setClientJSONField(child.Client, "reset", 0)
	if err := updateLandingNativeClient(ctx, baseURL, token, grant.Task.Grant.FixedUser, child.Client); err != nil {
		return err
	}
	observed, err := getThreeXUIClient(ctx, baseURL, token, grant.Task.Grant.FixedUser)
	var enabled bool
	var expiry int64
	if err != nil || verifyLandingChild(observed, grant) != nil || json.Unmarshal(observed.Client["enable"], &enabled) != nil || enabled || json.Unmarshal(observed.Client["expiryTime"], &expiry) != nil || expiry != 1 {
		return errors.New("agent: child disable was not confirmed")
	}
	return nil
}
