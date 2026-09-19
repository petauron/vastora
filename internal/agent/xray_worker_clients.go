package agent

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

func mutateXrayWorkerClients(state *xrayWorkerState, request *http.Request, path string) (any, bool, error) {
	if path == "/panel/api/clients/onlines" || path == "/panel/api/clients/lastOnline" || path == "/panel/api/clients/onlinesByGuid" || path == "/panel/api/clients/clientIpsByGuid" {
		return nil, false, errors.New("not a mutation")
	}
	var body map[string]any
	if raw, err := readJSONObject(request); err == nil {
		_ = json.Unmarshal(raw, &body)
	} else if request.ContentLength > 0 {
		return nil, false, err
	}
	ids := map[int]bool{}
	if rawIDs, ok := body["inboundIds"].([]any); ok {
		for _, raw := range rawIDs {
			if id, ok := jsonInteger(raw); ok {
				ids[id] = true
			}
		}
	}
	for _, raw := range request.URL.Query()["inboundIds"] {
		for _, part := range strings.Split(raw, ",") {
			if id, err := strconv.Atoi(part); err == nil {
				ids[id] = true
			}
		}
	}
	for id := range ids {
		if !slices.ContainsFunc(state.Inbounds, func(raw json.RawMessage) bool { return inboundID(raw) == id }) {
			return nil, false, errors.New("client target inbound not found")
		}
	}
	if path == "/panel/api/clients/add" && len(ids) == 0 {
		return nil, false, errors.New("client target inbounds are required")
	}
	client, _ := body["client"].(map[string]any)
	if client == nil && strings.HasPrefix(path, "/panel/api/clients/update/") {
		client = body
	}
	email := ""
	for _, prefix := range []string{"/panel/api/clients/update/", "/panel/api/clients/del/", "/panel/api/clients/resetTraffic/"} {
		if strings.HasPrefix(path, prefix) {
			email, _ = url.PathUnescape(strings.TrimPrefix(path, prefix))
		}
	}
	if marker := "/panel/api/clients/"; strings.HasPrefix(path, marker) && strings.HasSuffix(path, "/detach") {
		email, _ = url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(path, marker), "/detach"))
	}
	changed := false
	for index, raw := range state.Inbounds {
		id := inboundID(raw)
		if len(ids) != 0 && !ids[id] {
			continue
		}
		var inbound map[string]any
		_ = json.Unmarshal(raw, &inbound)
		settings, _ := inbound["settings"].(map[string]any)
		if settings == nil {
			return nil, false, errors.New("inbound settings unavailable")
		}
		clients, _ := settings["clients"].([]any)
		inboundChanged := false
		if path == "/panel/api/clients/add" && client != nil {
			clientEmail, _ := client["email"].(string)
			if strings.TrimSpace(clientEmail) == "" || slices.ContainsFunc(clients, func(value any) bool {
				current, _ := value.(map[string]any)
				return current["email"] == clientEmail
			}) {
				return nil, false, errors.New("invalid or duplicate client")
			}
			clients = append(clients, client)
			changed, inboundChanged = true, true
		} else {
			for clientIndex := 0; clientIndex < len(clients); clientIndex++ {
				current, _ := clients[clientIndex].(map[string]any)
				currentEmail, _ := current["email"].(string)
				if currentEmail != email {
					continue
				}
				if strings.HasPrefix(path, "/panel/api/clients/update/") && client != nil {
					protocol, _ := inbound["protocol"].(string)
					if !sameXrayWorkerClientCredential(current, client, protocol) {
						return nil, false, errors.New("client credential changes require explicit Vastora reconciliation")
					}
					clients[clientIndex] = client
				} else if strings.Contains(path, "resetTraffic") {
					resetXrayWorkerClientTraffic(inbound, email)
				} else {
					clients = append(clients[:clientIndex], clients[clientIndex+1:]...)
					clientIndex--
				}
				changed, inboundChanged = true, true
			}
		}
		if inboundChanged {
			settings["clients"] = clients
			inbound["settings"] = settings
			state.Inbounds[index], _ = json.Marshal(inbound)
		}
	}
	if changed && strings.Contains(path, "resetTraffic") {
		if state.AccountStats == nil {
			state.AccountStats = xrayWorkerAccountStats(*state)
		}
		state.AccountStats[email] = xrayWorkerTraffic{}
		if state.ControllerStats != nil {
			state.ControllerStats[email] = xrayWorkerTraffic{}
		}
	}
	if changed && strings.HasPrefix(path, "/panel/api/clients/update/") && client != nil {
		newEmail, _ := client["email"].(string)
		if newEmail != "" && newEmail != email {
			if state.AccountStats == nil {
				state.AccountStats = xrayWorkerAccountStats(*state)
			}
			state.AccountStats[newEmail] = state.AccountStats[email]
			delete(state.AccountStats, email)
			if state.ControllerStats != nil {
				state.ControllerStats[newEmail] = state.ControllerStats[email]
				delete(state.ControllerStats, email)
			}
			if state.BlockedAccounts != nil {
				state.BlockedAccounts[newEmail] = state.BlockedAccounts[email]
				delete(state.BlockedAccounts, email)
			}
		}
	}
	if changed && strings.HasPrefix(path, "/panel/api/clients/del/") {
		if state.AccountStats == nil {
			state.AccountStats = xrayWorkerAccountStats(*state)
		}
		if !xrayWorkerHasClient(*state, email) {
			delete(state.AccountStats, email)
			delete(state.ControllerStats, email)
			delete(state.BlockedAccounts, email)
		}
	}
	if !changed {
		return nil, false, errors.New("client not found")
	}
	return true, changed, nil
}

func sameXrayWorkerClientCredential(current, next map[string]any, protocol string) bool {
	if current == nil || next == nil {
		return false
	}
	field := "id"
	if protocol == "hysteria" {
		field = "auth"
	}
	currentValue, _ := current[field].(string)
	nextValue, _ := next[field].(string)
	return strings.TrimSpace(currentValue) != "" && subtle.ConstantTimeCompare([]byte(currentValue), []byte(nextValue)) == 1
}

func rawMessages(values []json.RawMessage) []json.RawMessage {
	return append([]json.RawMessage(nil), values...)
}
func inboundID(raw json.RawMessage) int {
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	id, _ := jsonInteger(value["id"])
	return id
}
func pathID(path, prefix string) (int, error) {
	value, err := strconv.Atoi(strings.TrimPrefix(path, prefix))
	if err != nil || value < 1 {
		return 0, errors.New("invalid id")
	}
	return value, nil
}
func readJSONObject(request *http.Request) (json.RawMessage, error) {
	defer request.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(request.Body, xrayWorkerMaxBody+1))
	if err != nil || len(raw) > xrayWorkerMaxBody {
		return nil, errors.New("invalid request")
	}
	if strings.HasPrefix(request.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		values, err := url.ParseQuery(string(raw))
		if err != nil {
			return nil, err
		}
		raw = []byte(values.Get("json"))
	}
	var object map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &object) != nil {
		return nil, errors.New("invalid request")
	}
	return raw, nil
}
