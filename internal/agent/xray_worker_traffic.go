package agent

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
)

func acceptXrayWorkerControllerStats(state xrayWorkerState, request *http.Request) (any, *xrayWorkerState, error) {
	raw, err := readJSONObject(request)
	if err != nil {
		return nil, nil, err
	}
	var payload struct {
		ControllerID string `json:"masterGuid"`
		Traffics     []struct {
			Email string `json:"email"`
			Up    int64  `json:"up"`
			Down  int64  `json:"down"`
		} `json:"traffics"`
	}
	if json.Unmarshal(raw, &payload) != nil || strings.TrimSpace(payload.ControllerID) == "" {
		return nil, nil, errors.New("invalid controller traffic snapshot")
	}
	if state.ControllerID != "" && state.ControllerID != payload.ControllerID {
		return nil, nil, errors.New("agent: Xray worker traffic controller changed")
	}
	state.ControllerID = payload.ControllerID
	if state.ControllerStats == nil {
		state.ControllerStats = map[string]xrayWorkerTraffic{}
	}
	for _, traffic := range payload.Traffics {
		email := strings.TrimSpace(traffic.Email)
		if email == "" || traffic.Up < 0 || traffic.Down < 0 || !xrayWorkerHasClient(state, email) {
			continue
		}
		// Ordinary controller observations are monotonic watermarks. A stale
		// controller restart or missed response must not reduce the shared
		// account usage already enforced by this worker. The authenticated
		// resetTraffic mutation is the only path allowed to clear both local
		// and controller counters.
		current := state.ControllerStats[email]
		state.ControllerStats[email] = xrayWorkerTraffic{Up: max(current.Up, traffic.Up), Down: max(current.Down, traffic.Down)}
	}
	return true, &state, nil
}

func reconcileXrayWorkerAccounts(state xrayWorkerState, nowUnixMilli int64) xrayWorkerState {
	blocked := map[string]bool{}
	for _, raw := range state.Inbounds {
		var inbound map[string]any
		_ = json.Unmarshal(raw, &inbound)
		settings, _ := inbound["settings"].(map[string]any)
		clients, _ := settings["clients"].([]any)
		for _, value := range clients {
			client, _ := value.(map[string]any)
			email, _ := client["email"].(string)
			if email == "" {
				continue
			}
			local := state.AccountStats[email]
			controller := state.ControllerStats[email]
			usedUp, usedDown := max(local.Up, controller.Up), max(local.Down, controller.Down)
			total, _ := jsonInteger64(client["totalGB"])
			expiry, _ := jsonInteger64(client["expiryTime"])
			if total > 0 && (usedUp >= total || usedDown >= total-usedUp) || expiry > 0 && expiry <= nowUnixMilli {
				blocked[email] = true
			}
		}
	}
	state.BlockedAccounts = blocked
	return state
}

func xrayWorkerAccountStats(state xrayWorkerState) map[string]xrayWorkerTraffic {
	result := make(map[string]xrayWorkerTraffic, len(state.AccountStats))
	for email, traffic := range state.AccountStats {
		result[email] = traffic
	}
	if state.AccountStats != nil {
		return result
	}
	// Legacy panel imports may repeat the same account counters on each
	// protocol inbound. Use the maximum observed value, never their sum.
	for _, raw := range state.Inbounds {
		var inbound map[string]any
		_ = json.Unmarshal(raw, &inbound)
		stats, _ := inbound["clientStats"].([]any)
		for _, rawStat := range stats {
			stat, _ := rawStat.(map[string]any)
			email, _ := stat["email"].(string)
			up, _ := jsonInteger64(stat["up"])
			down, _ := jsonInteger64(stat["down"])
			current := result[email]
			current.Up = max(current.Up, up)
			current.Down = max(current.Down, down)
			result[email] = current
		}
	}
	return result
}

func xrayWorkerHasClient(state xrayWorkerState, email string) bool {
	for _, raw := range state.Inbounds {
		var inbound map[string]any
		_ = json.Unmarshal(raw, &inbound)
		settings, _ := inbound["settings"].(map[string]any)
		clients, _ := settings["clients"].([]any)
		if slices.ContainsFunc(clients, func(value any) bool {
			client, _ := value.(map[string]any)
			return client["email"] == email
		}) {
			return true
		}
	}
	return false
}

func resetXrayWorkerClientTraffic(inbound map[string]any, email string) {
	stats, _ := inbound["clientStats"].([]any)
	for _, raw := range stats {
		stat, _ := raw.(map[string]any)
		if stat["email"] == email {
			stat["up"], stat["down"] = int64(0), int64(0)
		}
	}
	inbound["clientStats"] = stats
}
