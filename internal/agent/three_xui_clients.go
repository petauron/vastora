package agent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const threeXUIClientPageSize = 200

type threeXUIClientPage struct {
	Items []struct {
		Email      string `json:"email"`
		SubID      string `json:"subId"`
		Enable     bool   `json:"enable"`
		TotalGB    int64  `json:"totalGB"`
		ExpiryTime int64  `json:"expiryTime"`
		LimitIP    int    `json:"limitIp"`
		InboundIDs []int  `json:"inboundIds"`
		Traffic    struct {
			Up   int64 `json:"up"`
			Down int64 `json:"down"`
		} `json:"traffic"`
	} `json:"items"`
	Total int `json:"total"`
}

type threeXUIClientDetail struct {
	Client     map[string]json.RawMessage `json:"client"`
	InboundIDs []int                      `json:"inboundIds"`
}

func applyThreeXUIClientCommand(ctx context.Context, store *Store, command ThreeXUIClientCommandTask) (ThreeXUIClientCommandResult, error) {
	baseURL, token, err := threeXUIClientAPIConnection(ctx, store)
	if err != nil {
		return ThreeXUIClientCommandResult{}, err
	}
	result := ThreeXUIClientCommandResult{Inbounds: append([]ThreeXUIClientInbound(nil), command.Inbounds...)}
	switch command.Action {
	case "list":
	case "create":
		if !clientInboundsAvailable(command.Inbounds, command.InboundIDs) {
			return result, errors.New("agent: selected 3x-ui nodes are unavailable")
		}
		clientID, err := randomUUID()
		if err != nil {
			return result, err
		}
		subID, err := randomClientToken()
		if err != nil {
			return result, err
		}
		payload := map[string]any{
			"client": map[string]any{
				"email": command.NewEmail, "subId": subID, "id": clientID, "flow": "xtls-rprx-vision",
				"totalGB": command.TotalBytes, "expiryTime": command.ExpiryTime, "limitIp": command.LimitIP,
				"tgId": 0, "enable": command.Enabled,
			},
			"inboundIds": command.InboundIDs,
		}
		if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/add", token, "application/json", payload); err != nil {
			return result, fmt.Errorf("agent: create 3x-ui client: %w", err)
		}
	case "update", "set_enabled":
		detail, err := getThreeXUIClient(ctx, baseURL, token, command.Email)
		if err != nil {
			return result, err
		}
		if command.Action == "update" {
			if !clientInboundsAvailable(command.Inbounds, command.InboundIDs) {
				return result, errors.New("agent: selected 3x-ui nodes are unavailable")
			}
			setClientJSONField(detail.Client, "email", command.NewEmail)
			setClientJSONField(detail.Client, "totalGB", command.TotalBytes)
			setClientJSONField(detail.Client, "expiryTime", command.ExpiryTime)
			setClientJSONField(detail.Client, "limitIp", command.LimitIP)
		} else {
			setClientJSONField(detail.Client, "enable", command.Enabled)
		}
		if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/update/"+url.PathEscape(command.Email), token, "application/json", detail.Client); err != nil {
			return result, fmt.Errorf("agent: update 3x-ui client: %w", err)
		}
		if command.Action == "update" {
			if err := syncThreeXUIClientInbounds(ctx, baseURL, token, command.NewEmail, detail.InboundIDs, command.InboundIDs); err != nil {
				return result, err
			}
		}
	case "delete":
		if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/del/"+url.PathEscape(command.Email), token, "application/json", map[string]any{}); err != nil {
			return result, fmt.Errorf("agent: delete 3x-ui client: %w", err)
		}
	case "reset_traffic":
		if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/resetTraffic/"+url.PathEscape(command.Email), token, "application/json", map[string]any{}); err != nil {
			return result, fmt.Errorf("agent: reset 3x-ui client traffic: %w", err)
		}
	case "reveal_link":
		secret, err := revealThreeXUIClientLink(ctx, baseURL, token, command)
		if err != nil {
			return result, err
		}
		result.Secret, result.SecretKind = secret, "client_link"
	case "reveal_subscription":
		secret, err := revealThreeXUIClientSubscription(ctx, baseURL, token, command)
		if err != nil {
			return result, err
		}
		result.Secret, result.SecretKind = secret, "subscription"
	default:
		return result, errors.New("agent: unsupported 3x-ui client operation")
	}
	clients, err := listThreeXUIClients(ctx, baseURL, token)
	if err != nil {
		if command.Action == "list" {
			return result, err
		}
		return result, nil
	}
	result.Clients = clients
	result.ClientsObserved = true
	return result, nil
}

func threeXUIClientAPIConnection(ctx context.Context, store *Store) (string, string, error) {
	installation, err := store.AppliedInstallation(ctx, threeXUIKey)
	if err != nil {
		return "", "", errors.New("agent: official 3x-ui is not installed")
	}
	config, err := decodeThreeXUIConfig(installation.Config)
	if err != nil {
		return "", "", err
	}
	var secrets map[string]string
	if json.Unmarshal(installation.Secrets, &secrets) != nil || strings.TrimSpace(secrets["api_token"]) == "" {
		return "", "", errors.New("agent: 3x-ui API token is unavailable")
	}
	address := installation.ServiceAddress
	if net.ParseIP(address) == nil {
		return "", "", errors.New("agent: 3x-ui private service address is invalid")
	}
	return "http://" + net.JoinHostPort(address, strconv.Itoa(config.PanelPort)), strings.TrimSpace(secrets["api_token"]), nil
}

func listThreeXUIClients(ctx context.Context, baseURL, token string) ([]ThreeXUIClientView, error) {
	clients := []ThreeXUIClientView{}
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("%s/panel/api/clients/list/paged?page=%d&pageSize=%d&sort=email&order=ascend", baseURL, page, threeXUIClientPageSize)
		payload, err := threeXUIAPI(ctx, http.MethodGet, endpoint, token, "", nil)
		if err != nil {
			return nil, fmt.Errorf("agent: list 3x-ui clients: %w", err)
		}
		var response threeXUIClientPage
		if json.Unmarshal(payload, &response) != nil || response.Total < 0 {
			return nil, errors.New("agent: 3x-ui returned invalid client data")
		}
		for _, client := range response.Items {
			if strings.TrimSpace(client.Email) == "" {
				return nil, errors.New("agent: 3x-ui returned an unnamed client")
			}
			inboundIDs := append([]int(nil), client.InboundIDs...)
			sort.Ints(inboundIDs)
			clients = append(clients, ThreeXUIClientView{
				Email: client.Email, Enabled: client.Enable, TotalBytes: client.TotalGB,
				UsedBytes: client.Traffic.Up + client.Traffic.Down, ExpiryTime: client.ExpiryTime,
				LimitIP: client.LimitIP, InboundIDs: inboundIDs, HasSubscription: strings.TrimSpace(client.SubID) != "",
			})
		}
		if len(clients) >= response.Total || len(response.Items) < threeXUIClientPageSize {
			break
		}
		if page >= 50 {
			return nil, errors.New("agent: 3x-ui client list exceeds the supported size")
		}
	}
	return clients, nil
}

func getThreeXUIClient(ctx context.Context, baseURL, token, email string) (threeXUIClientDetail, error) {
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/clients/get/"+url.PathEscape(email), token, "", nil)
	if err != nil {
		return threeXUIClientDetail{}, fmt.Errorf("agent: get 3x-ui client: %w", err)
	}
	var detail threeXUIClientDetail
	if json.Unmarshal(payload, &detail) != nil || len(detail.Client) == 0 {
		return detail, errors.New("agent: 3x-ui returned invalid client details")
	}
	return detail, nil
}

func setClientJSONField(client map[string]json.RawMessage, key string, value any) {
	encoded, _ := json.Marshal(value)
	client[key] = encoded
}

func clientJSONText(client map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(client[key], &value)
	return strings.TrimSpace(value)
}

func clientInboundsAvailable(inbounds []ThreeXUIClientInbound, ids []int) bool {
	if len(ids) == 0 {
		return false
	}
	available := make(map[int]bool, len(inbounds))
	for _, inbound := range inbounds {
		available[inbound.ID] = true
	}
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id < 1 || !available[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func syncThreeXUIClientInbounds(ctx context.Context, baseURL, token, email string, current, desired []int) error {
	currentSet := make(map[int]bool, len(current))
	desiredSet := make(map[int]bool, len(desired))
	for _, inboundID := range current {
		currentSet[inboundID] = true
	}
	for _, inboundID := range desired {
		desiredSet[inboundID] = true
	}
	attach, detach := []int{}, []int{}
	for _, inboundID := range desired {
		if !currentSet[inboundID] {
			attach = append(attach, inboundID)
		}
	}
	for _, inboundID := range current {
		if !desiredSet[inboundID] {
			detach = append(detach, inboundID)
		}
	}
	if len(attach) != 0 {
		if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/"+url.PathEscape(email)+"/attach", token, "application/json", map[string]any{"inboundIds": attach}); err != nil {
			return fmt.Errorf("agent: attach 3x-ui client to selected nodes: %w", err)
		}
	}
	if len(detach) != 0 {
		if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/"+url.PathEscape(email)+"/detach", token, "application/json", map[string]any{"inboundIds": detach}); err != nil {
			return fmt.Errorf("agent: detach 3x-ui client from unselected nodes: %w", err)
		}
	}
	return nil
}

func clientInbound(inbounds []ThreeXUIClientInbound, ids []int, requested int) (ThreeXUIClientInbound, bool) {
	for _, inbound := range inbounds {
		if inbound.ID != requested && requested != 0 {
			continue
		}
		for _, id := range ids {
			if inbound.ID == id {
				return inbound, true
			}
		}
	}
	return ThreeXUIClientInbound{}, false
}

func revealThreeXUIClientLink(ctx context.Context, baseURL, token string, command ThreeXUIClientCommandTask) (string, error) {
	detail, err := getThreeXUIClient(ctx, baseURL, token, command.Email)
	if err != nil {
		return "", err
	}
	inboundRef, ok := clientInbound(command.Inbounds, detail.InboundIDs, command.InboundID)
	if !ok || strings.TrimSpace(inboundRef.ConnectHostname) == "" {
		return "", errors.New("agent: this client has no ready public REALITY entry")
	}
	inbound, err := ensureThreeXUIRealityClientVersion(ctx, baseURL, token, inboundRef.ID)
	if err != nil {
		return "", err
	}
	return realityClientLinkFromInbound(inbound, inboundRef.ConnectHostname, command.Email)
}

func realityClientLinkFromInbound(inbound threeXUIRealityInbound, connectHostname, email string) (string, error) {
	var settings struct {
		Clients []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Flow  string `json:"flow"`
		} `json:"clients"`
	}
	var stream struct {
		Security string `json:"security"`
		Reality  struct {
			ServerNames []string `json:"serverNames"`
			ShortIDs    []string `json:"shortIds"`
			Settings    struct {
				PublicKey string `json:"publicKey"`
			} `json:"settings"`
		} `json:"realitySettings"`
	}
	if json.Unmarshal(inbound.Settings, &settings) != nil || json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Security != "reality" || len(stream.Reality.ServerNames) == 0 || len(stream.Reality.ShortIDs) == 0 || stream.Reality.Settings.PublicKey == "" {
		return "", errors.New("agent: selected REALITY inbound is incomplete")
	}
	for _, client := range settings.Clients {
		if client.Email == email && client.ID != "" {
			return realityShareURI(client.ID, connectHostname, email, stream.Reality.ServerNames[0], stream.Reality.Settings.PublicKey, stream.Reality.ShortIDs[0]), nil
		}
	}
	return "", errors.New("agent: client is not attached to the selected REALITY inbound")
}

func revealThreeXUIClientSubscription(ctx context.Context, baseURL, token string, command ThreeXUIClientCommandTask) (string, error) {
	base, err := url.Parse(strings.TrimSpace(command.SubscriptionBaseURI))
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.Port() != "" || base.Path != threeXUIRawSubscriptionPath || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("agent: no ready public subscription entry is available")
	}
	for _, inbound := range command.Inbounds {
		if strings.TrimSpace(inbound.ConnectHostname) == "" || strings.TrimSpace(inbound.SNIHostname) == "" {
			continue
		}
		if _, err := ensureThreeXUIRealityClientVersion(ctx, baseURL, token, inbound.ID); err != nil {
			return "", err
		}
		if err := syncThreeXUIRealityHost(ctx, baseURL, token, inbound.ID, inbound.ConnectHostname, inbound.SNIHostname); err != nil {
			return "", err
		}
	}
	_, err = configureThreeXUIPublicSubscription(ctx, baseURL, token, base.Hostname(), base.String())
	if err != nil {
		return "", err
	}
	detail, err := getThreeXUIClient(ctx, baseURL, token, command.Email)
	if err != nil {
		return "", err
	}
	subID := clientJSONText(detail.Client, "subId")
	if subID == "" {
		subID, err = randomClientToken()
		if err != nil {
			return "", err
		}
		setClientJSONField(detail.Client, "subId", subID)
		if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/update/"+url.PathEscape(command.Email), token, "application/json", detail.Client); err != nil {
			return "", fmt.Errorf("agent: enable 3x-ui client subscription: %w", err)
		}
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + "/" + subID
	return base.String(), nil
}

func randomClientToken() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("agent: generate 3x-ui subscription id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
