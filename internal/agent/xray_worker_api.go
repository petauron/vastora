package agent

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

func (s *Store) xrayWorkerHandler(apply xrayWorkerApply, observe xrayWorkerObserve) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(response, request.Body, xrayWorkerMaxBody)
		path := strings.TrimSuffix(request.URL.Path, "/")
		if !xrayWorkerEndpointAllowed(request.Method, path) {
			http.Error(response, "not found", http.StatusNotFound)
			return
		}
		s.xrayWorkerStateMu.Lock()
		defer s.xrayWorkerStateMu.Unlock()
		if err := s.requireLegacyLandingAuthority(request.Context()); err != nil {
			http.Error(response, "legacy runtime retired", http.StatusConflict)
			return
		}
		state, err := s.loadXrayWorkerState(request.Context())
		expectedAuthorization := "Bearer " + state.APIToken
		if err != nil || subtle.ConstantTimeCompare([]byte(request.Header.Get("Authorization")), []byte(expectedAuthorization)) != 1 {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		original := state
		// Mutations replace inbound entries and update accounting maps in place.
		// Keep the loaded snapshot separate so config changes cannot disappear
		// from the revision/apply comparison through shared slice backing arrays.
		state = cloneXrayWorkerState(state)
		forceApply := request.Method == http.MethodPost && path == "/panel/api/server/restartXrayService"
		var observationErr error
		if observe != nil {
			var observed xrayWorkerState
			observed, observationErr = observe(request.Context(), state)
			if observationErr == nil {
				state = observed
				if state.AppliedRevision < state.Revision && s.xrayWorkerAppliedReceiptMatches(state) {
					state.AppliedRevision = state.Revision
				}
			}
		}
		if observationErr == nil && state.AppliedRevision != state.Revision {
			observationErr = errors.New("agent: Xray worker revision requires explicit recovery")
		}
		object, mutation, err := xrayWorkerRequest(state, request)
		if observationErr != nil {
			err = observationErr
			mutation = nil
		}
		if err == nil {
			candidate := state
			if mutation != nil {
				candidate = *mutation
			}
			candidate = reconcileXrayWorkerAccounts(candidate, s.now().UnixMilli())
			changed, changeErr := xrayWorkerConfigChanged(original, candidate)
			if changeErr != nil {
				err = changeErr
			} else if mutation != nil || changed || !xrayWorkerStateEqual(original, candidate) {
				mutation = &candidate
			}
		}
		if err == nil && mutation != nil {
			if apply == nil {
				err = errors.New("worker runtime unavailable")
			} else {
				changed, changeErr := xrayWorkerConfigChanged(original, *mutation)
				if changeErr != nil {
					err = changeErr
				} else if !changed && !forceApply {
					err = s.saveXrayWorkerState(request.Context(), *mutation)
				} else {
					mutation.Revision = original.Revision + 1
					mutation.AppliedRevision = original.AppliedRevision
					if err = s.saveXrayWorkerState(request.Context(), *mutation); err == nil {
						err = apply(request.Context(), original, *mutation)
					}
					if err == nil {
						mutation.AppliedRevision = mutation.Revision
						err = s.saveXrayWorkerState(request.Context(), *mutation)
					}
				}
			}
		}
		response.Header().Set("Content-Type", "application/json")
		if err != nil {
			response.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(response).Encode(map[string]any{"success": false, "msg": "request rejected"})
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"success": true, "msg": "", "obj": object})
	})
}

func xrayWorkerStateEqual(left, right xrayWorkerState) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func cloneXrayWorkerState(state xrayWorkerState) xrayWorkerState {
	state.Inbounds = slices.Clone(state.Inbounds)
	for index := range state.Inbounds {
		state.Inbounds[index] = bytes.Clone(state.Inbounds[index])
	}
	state.HostGroups = slices.Clone(state.HostGroups)
	for index := range state.HostGroups {
		group := &state.HostGroups[index]
		group.InboundIDs = slices.Clone(group.InboundIDs)
		group.Hosts = slices.Clone(group.Hosts)
		group.Tags = slices.Clone(group.Tags)
	}
	state.XraySetting = bytes.Clone(state.XraySetting)
	state.RuntimeStats = maps.Clone(state.RuntimeStats)
	state.AccountStats = maps.Clone(state.AccountStats)
	state.ControllerStats = maps.Clone(state.ControllerStats)
	state.BlockedAccounts = maps.Clone(state.BlockedAccounts)
	return state
}

func xrayWorkerEndpointAllowed(method, path string) bool {
	if method == http.MethodGet {
		switch path {
		case "/panel/api/server/status", "/panel/api/inbounds/list", "/panel/api/hosts/list", "/panel/api/server/getWebCertFiles", "/panel/api/server/descendants", "/panel/api/server/clientIps":
			return true
		}
		return strings.HasPrefix(path, "/panel/api/inbounds/get/") || strings.HasPrefix(path, "/panel/api/hosts/byInbound/")
	}
	if method != http.MethodPost {
		return false
	}
	switch path {
	case "/panel/api/server/restartXrayService", "/panel/api/xray", "/panel/api/xray/update",
		"/panel/api/inbounds/add", "/panel/api/inbounds/resetAllTraffics", "/panel/api/inbounds/pushClientTraffics",
		"/panel/api/hosts/add",
		"/panel/api/clients/add", "/panel/api/clients/onlines", "/panel/api/clients/lastOnline", "/panel/api/clients/onlinesByGuid", "/panel/api/clients/activeInbounds",
		"/panel/api/clients/clientIpsByGuid", "/panel/api/server/clientIps":
		return true
	}
	for _, rule := range []struct{ prefix, suffix string }{
		{"/panel/api/inbounds/update/", ""},
		{"/panel/api/inbounds/del/", ""},
		{"/panel/api/inbounds/setEnable/", ""},
		{"/panel/api/inbounds/", "/resetTraffic"},
		{"/panel/api/inbounds/", "/subSortIndex"},
		{"/panel/api/clients/update/", ""},
		{"/panel/api/clients/del/", ""},
		{"/panel/api/clients/resetTraffic/", ""},
		{"/panel/api/clients/", "/detach"},
		{"/panel/api/hosts/update/", ""},
	} {
		remaining, ok := strings.CutPrefix(path, rule.prefix)
		if !ok || remaining == "" {
			continue
		}
		if rule.suffix == "" || strings.HasSuffix(remaining, rule.suffix) && strings.TrimSuffix(remaining, rule.suffix) != "" {
			return true
		}
	}
	return false
}

func xrayWorkerRequest(state xrayWorkerState, request *http.Request) (any, *xrayWorkerState, error) {
	path := strings.TrimSuffix(request.URL.Path, "/")
	if request.Method == http.MethodGet && path == "/panel/api/server/status" {
		if state.AppliedRevision != state.Revision {
			return nil, nil, errors.New("agent: Xray worker revision is not applied")
		}
		return map[string]any{
			"xray":            map[string]any{"state": "running", "version": xrayWorkerImageVersion(state.ImageReference), "errorMsg": ""},
			"panelVersion":    "vastora-agent",
			"panelGuid":       state.ApplicationID,
			"desiredRevision": state.Revision,
			"appliedRevision": state.AppliedRevision,
		}, nil, nil
	}
	if request.Method == http.MethodGet && path == "/panel/api/inbounds/list" {
		return rawMessages(state.Inbounds), nil, nil
	}
	if request.Method == http.MethodGet && path == "/panel/api/hosts/list" {
		return state.HostGroups, nil, nil
	}
	if request.Method == http.MethodGet && strings.HasPrefix(path, "/panel/api/hosts/byInbound/") {
		id, err := pathID(path, "/panel/api/hosts/byInbound/")
		if err != nil {
			return nil, nil, err
		}
		groups := make([]threeXUIHostGroup, 0, len(state.HostGroups))
		for _, group := range state.HostGroups {
			if slices.Contains(group.InboundIDs, id) {
				groups = append(groups, group)
			}
		}
		return groups, nil, nil
	}
	if request.Method == http.MethodPost && (path == "/panel/api/hosts/add" || strings.HasPrefix(path, "/panel/api/hosts/update/")) {
		var group threeXUIHostGroup
		if json.NewDecoder(request.Body).Decode(&group) != nil {
			return nil, nil, errors.New("invalid subscription host group")
		}
		inboundIDs := make(map[int]bool, len(state.Inbounds))
		for _, raw := range state.Inbounds {
			inboundIDs[inboundID(raw)] = true
		}
		if !validXrayWorkerHostGroup(group, inboundIDs) {
			return nil, nil, errors.New("invalid subscription host group")
		}
		index := slices.IndexFunc(state.HostGroups, func(current threeXUIHostGroup) bool { return current.GroupID == group.GroupID })
		if path == "/panel/api/hosts/add" {
			if index >= 0 {
				return nil, nil, errors.New("subscription host group already exists")
			}
			state.HostGroups = append(state.HostGroups, group)
		} else {
			groupID := strings.TrimPrefix(path, "/panel/api/hosts/update/")
			if groupID == "" || groupID != group.GroupID || index < 0 {
				return nil, nil, errors.New("subscription host group not found")
			}
			state.HostGroups[index] = group
		}
		return group, &state, nil
	}
	if request.Method == http.MethodPost && path == "/panel/api/server/restartXrayService" {
		return true, &state, nil
	}
	if request.Method == http.MethodPost && path == "/panel/api/xray" {
		nested, _ := json.Marshal(map[string]any{"xraySetting": json.RawMessage(state.XraySetting), "outboundTestUrl": ""})
		return string(nested), nil, nil
	}
	if request.Method == http.MethodPost && path == "/panel/api/xray/update" {
		// Routing is Vastora-owned landing state. The transitional controller
		// receives the worker token for inventory and account synchronization,
		// but must not be able to overwrite Agent-managed exits through a manual
		// panel edit. localThreeXUILandingRoutes reaches this private listener
		// from the same service address, so only that node-local caller may write
		// the routing document.
		if !xrayWorkerLocalRoutingMutation(state, request) {
			return nil, nil, errors.New("routing settings are managed by Vastora Agent")
		}
		if err := request.ParseForm(); err != nil {
			return nil, nil, err
		}
		raw := json.RawMessage(request.Form.Get("xraySetting"))
		var object map[string]any
		if json.Unmarshal(raw, &object) != nil {
			return nil, nil, errors.New("invalid settings")
		}
		state.XraySetting = append(json.RawMessage(nil), raw...)
		return true, &state, nil
	}
	if request.Method == http.MethodGet && strings.HasPrefix(path, "/panel/api/inbounds/get/") {
		id, err := pathID(path, "/panel/api/inbounds/get/")
		if err != nil {
			return nil, nil, err
		}
		for _, raw := range state.Inbounds {
			if inboundID(raw) == id {
				return raw, nil, nil
			}
		}
		return nil, nil, errors.New("not found")
	}
	if request.Method == http.MethodPost && path == "/panel/api/inbounds/add" {
		raw, err := readInboundPayload(request)
		if err != nil {
			return nil, nil, err
		}
		var inbound map[string]any
		_ = json.Unmarshal(raw, &inbound)
		inbound["id"] = state.NextInboundID
		state.NextInboundID++
		raw, _ = json.Marshal(inbound)
		state.Inbounds = append(state.Inbounds, raw)
		if state.validate() != nil {
			return nil, nil, errors.New("invalid inbound")
		}
		return raw, &state, nil
	}
	if request.Method == http.MethodPost && path == "/panel/api/inbounds/resetAllTraffics" {
		state.AccountStats = map[string]xrayWorkerTraffic{}
		state.ControllerStats = map[string]xrayWorkerTraffic{}
		for index := range state.Inbounds {
			var inbound map[string]any
			_ = json.Unmarshal(state.Inbounds[index], &inbound)
			inbound["up"], inbound["down"] = 0, 0
			if stats, ok := inbound["clientStats"].([]any); ok {
				for _, value := range stats {
					stat, _ := value.(map[string]any)
					stat["up"], stat["down"] = int64(0), int64(0)
				}
				inbound["clientStats"] = stats
			}
			state.Inbounds[index], _ = json.Marshal(inbound)
		}
		return true, &state, nil
	}
	if request.Method == http.MethodPost && strings.HasPrefix(path, "/panel/api/inbounds/") && strings.HasSuffix(path, "/subSortIndex") {
		idText := strings.TrimSuffix(strings.TrimPrefix(path, "/panel/api/inbounds/"), "/subSortIndex")
		id, err := strconv.Atoi(idText)
		if err != nil || request.ParseForm() != nil {
			return nil, nil, errors.New("invalid sort index")
		}
		index := slices.IndexFunc(state.Inbounds, func(raw json.RawMessage) bool { return inboundID(raw) == id })
		value, valueErr := strconv.Atoi(request.Form.Get("subSortIndex"))
		if index < 0 || valueErr != nil {
			return nil, nil, errors.New("invalid sort index")
		}
		var inbound map[string]any
		_ = json.Unmarshal(state.Inbounds[index], &inbound)
		inbound["subSortIndex"] = value
		state.Inbounds[index], _ = json.Marshal(inbound)
		return true, &state, nil
	}
	if request.Method == http.MethodPost && (strings.HasPrefix(path, "/panel/api/clients/") || path == "/panel/api/clients/add") {
		if path == "/panel/api/clients/onlines" || path == "/panel/api/clients/lastOnline" {
			return []any{}, nil, nil
		}
		if path == "/panel/api/clients/onlinesByGuid" || path == "/panel/api/clients/clientIpsByGuid" {
			return map[string]any{}, nil, nil
		}
		object, changed, err := mutateXrayWorkerClients(&state, request, path)
		if err != nil {
			return nil, nil, err
		}
		if changed {
			return object, &state, nil
		}
		return object, nil, nil
	}
	if request.Method == http.MethodGet && (path == "/panel/api/server/descendants" || path == "/panel/api/server/clientIps") {
		return []any{}, nil, nil
	}
	if request.Method == http.MethodGet && path == "/panel/api/server/getWebCertFiles" {
		return map[string]any{"webCertFile": "", "webKeyFile": ""}, nil, nil
	}
	if request.Method == http.MethodPost && (path == "/panel/api/clients/onlines" || path == "/panel/api/clients/lastOnline") {
		return []any{}, nil, nil
	}
	if request.Method == http.MethodPost && (path == "/panel/api/clients/onlinesByGuid" || path == "/panel/api/clients/activeInbounds" || path == "/panel/api/clients/clientIpsByGuid") {
		return map[string]any{}, nil, nil
	}
	if request.Method == http.MethodPost && path == "/panel/api/inbounds/pushClientTraffics" {
		return acceptXrayWorkerControllerStats(state, request)
	}
	if request.Method == http.MethodPost && path == "/panel/api/server/clientIps" {
		return true, nil, nil
	}
	for _, operation := range []struct{ prefix, kind string }{{"/panel/api/inbounds/update/", "update"}, {"/panel/api/inbounds/del/", "delete"}, {"/panel/api/inbounds/setEnable/", "enable"}, {"/panel/api/inbounds/", "reset"}} {
		if request.Method != http.MethodPost || !strings.HasPrefix(path, operation.prefix) {
			continue
		}
		remaining := strings.TrimPrefix(path, operation.prefix)
		if operation.kind == "reset" {
			remaining = strings.TrimSuffix(remaining, "/resetTraffic")
		}
		id, err := strconv.Atoi(remaining)
		if err != nil || id < 1 {
			return nil, nil, errors.New("invalid id")
		}
		index := slices.IndexFunc(state.Inbounds, func(raw json.RawMessage) bool { return inboundID(raw) == id })
		if index < 0 {
			return nil, nil, errors.New("not found")
		}
		if operation.kind == "delete" {
			state.Inbounds = append(state.Inbounds[:index], state.Inbounds[index+1:]...)
			state.HostGroups = slices.DeleteFunc(state.HostGroups, func(group threeXUIHostGroup) bool {
				return slices.Contains(group.InboundIDs, id)
			})
			return true, &state, nil
		}
		var inbound map[string]any
		_ = json.Unmarshal(state.Inbounds[index], &inbound)
		if operation.kind == "update" {
			raw, err := readInboundPayload(request)
			if err != nil {
				return nil, nil, err
			}
			var update map[string]any
			_ = json.Unmarshal(raw, &update)
			for key, value := range update {
				inbound[key] = value
			}
			inbound["id"] = id
		}
		if operation.kind == "enable" {
			raw, err := readJSONObject(request)
			if err != nil {
				return nil, nil, err
			}
			var values map[string]any
			_ = json.Unmarshal(raw, &values)
			enabled, ok := values["enable"].(bool)
			if !ok {
				return nil, nil, errors.New("invalid enabled state")
			}
			inbound["enable"] = enabled
		}
		if operation.kind == "reset" {
			inbound["up"], inbound["down"] = 0, 0
		}
		state.Inbounds[index], _ = json.Marshal(inbound)
		if state.validate() != nil {
			return nil, nil, errors.New("invalid inbound")
		}
		return state.Inbounds[index], &state, nil
	}
	return nil, nil, errors.New("unsupported endpoint")
}

func xrayWorkerLocalRoutingMutation(state xrayWorkerState, request *http.Request) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(request.RemoteAddr))
	if err != nil {
		return false
	}
	remote, local := net.ParseIP(host), net.ParseIP(state.Address)
	return remote != nil && local != nil && remote.Equal(local)
}

func readInboundPayload(request *http.Request) (json.RawMessage, error) {
	if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return readJSONObject(request)
	}
	if err := request.ParseForm(); err != nil {
		return nil, err
	}
	value := map[string]any{}
	for _, key := range []string{"remark", "listen", "protocol", "tag", "shareAddrStrategy", "shareAddr", "trafficReset"} {
		if request.Form.Has(key) {
			value[key] = request.Form.Get(key)
		}
	}
	for _, key := range []string{"port", "total", "expiryTime", "subSortIndex", "trafficResetDay"} {
		if raw := request.Form.Get(key); raw != "" {
			number, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return nil, errors.New("invalid inbound number")
			}
			value[key] = number
		}
	}
	for _, key := range []string{"enable", "disableFlow"} {
		if raw := request.Form.Get(key); raw != "" {
			enabled, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, errors.New("invalid inbound state")
			}
			value[key] = enabled
		}
	}
	for _, key := range []string{"settings", "streamSettings", "sniffing"} {
		if raw := request.Form.Get(key); raw != "" {
			var nested any
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.UseNumber()
			if decoder.Decode(&nested) != nil {
				return nil, errors.New("invalid inbound JSON")
			}
			value[key] = nested
		}
	}
	return json.Marshal(value)
}
