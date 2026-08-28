package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const threeXUIRealityMinClientVersion = "1.8.2"

type threeXUIRealityInbound struct {
	ID              int             `json:"id"`
	Tag             string          `json:"tag"`
	Remark          string          `json:"remark"`
	Protocol        string          `json:"protocol"`
	Listen          string          `json:"listen"`
	Port            int             `json:"port"`
	Enable          bool            `json:"enable"`
	Up              int64           `json:"up"`
	Down            int64           `json:"down"`
	Total           int64           `json:"total"`
	TrafficReset    string          `json:"trafficReset"`
	TrafficResetDay int             `json:"trafficResetDay"`
	Settings        json.RawMessage `json:"settings"`
	StreamSettings  json.RawMessage `json:"streamSettings"`
	NodeID          *int            `json:"nodeId,omitempty"`
}

type threeXUIHostGroup struct {
	GroupID           string   `json:"groupId"`
	InboundIDs        []int    `json:"inboundIds"`
	Hosts             []string `json:"hosts"`
	Remark            string   `json:"remark"`
	ServerDescription string   `json:"serverDescription"`
	IsDisabled        bool     `json:"isDisabled"`
	IsHidden          bool     `json:"isHidden"`
	Tags              []string `json:"tags"`
	Port              int      `json:"port"`
	Security          string   `json:"security"`
	SNI               string   `json:"sni"`
	Fingerprint       string   `json:"fingerprint"`
	MihomoIPVersion   string   `json:"mihomoIpVersion"`
}

type realityScanResult struct {
	Target      string   `json:"target"`
	Host        string   `json:"host"`
	Feasible    bool     `json:"feasible"`
	ServerNames []string `json:"serverNames"`
}

type uncertainRealityMutationError struct {
	cause error
}

func (e *uncertainRealityMutationError) Error() string { return e.cause.Error() }
func (e *uncertainRealityMutationError) Unwrap() error { return e.cause }

func uncertainRealityMutation(cause error) error {
	if cause == nil {
		return nil
	}
	return &uncertainRealityMutationError{cause: cause}
}

func realityMutationOutcomeUncertain(err error) bool {
	var uncertain *uncertainRealityMutationError
	return errors.As(err, &uncertain)
}

func deferUncertainRealityTask(_ int64, cause error) error {
	if cause == nil {
		return nil
	}
	return deferTaskUntilReconciled(cause)
}

func deferOrRollbackKnownRealityTask(ctx context.Context, attempt int64, cause error, baseURL, token string, inboundID int, inboundTag string, nodeID int, clientEmail string, clientCreated bool) error {
	if cause == nil {
		return nil
	}
	if attempt < maxDeferredTaskAttempts {
		return deferTaskUntilReconciled(cause)
	}
	if rollbackErr := rollbackThreeXUIRealityCreation(ctx, baseURL, token, inboundID, inboundTag, nodeID, clientEmail, clientCreated); rollbackErr != nil {
		return deferTaskUntilReconciled(errors.Join(cause, rollbackErr))
	}
	return cause
}

func applyRealityCommand(ctx context.Context, store *Store, commandID string, attempt int64, command RealityCommandTask) (RealityCommandResult, error) {
	return applyRealityCommandWithRecovery(ctx, store, commandID, attempt, command, false)
}

func applyRealityCommandWithRecovery(ctx context.Context, store *Store, commandID string, attempt int64, command RealityCommandTask, recreatedRecoveredHalfState bool) (RealityCommandResult, error) {
	baseURL, masterToken, err := threeXUIClientAPIConnection(ctx, store)
	if err != nil {
		if attempt > 1 {
			return RealityCommandResult{}, deferUncertainRealityTask(attempt, err)
		}
		return RealityCommandResult{}, err
	}
	if command.Action == "rename" {
		result, renameErr := renameThreeXUIRealityInbound(ctx, baseURL, masterToken, command)
		if renameErr != nil {
			if realityMutationOutcomeUncertain(renameErr) {
				// Both mutations can commit before a response is lost. Preserve this
				// command ID only when their read-back is also inconclusive; explicit
				// precondition failures (for example a deleted inbound) are terminal.
				return RealityCommandResult{}, deferUncertainRealityTask(attempt, renameErr)
			}
			return RealityCommandResult{}, renameErr
		}
		return result, nil
	}
	if command.Action != "create" || !validRealityDisplayName(command.DisplayName) || (command.CreateInitialClient && !validRealityDisplayName(command.ClientName)) || command.InboundTag != threeXUIRealityTag(commandID) || command.InboundTotalBytes < 0 || command.InboundResetDays < 0 || command.InboundResetDays > maxThreeXUIResetDays || command.ClientTotalBytes < 0 || command.ClientResetDays < 0 || command.ClientResetDays > maxThreeXUIResetDays || command.ClientExpiryTime < 0 {
		return RealityCommandResult{}, errors.New("agent: REALITY creation parameters are invalid")
	}
	listen := strings.TrimSpace(command.TargetAddress)
	if ip := net.ParseIP(listen); ip == nil || ip.To4() == nil {
		return RealityCommandResult{}, errors.New("agent: target VLESS node private service address is invalid")
	}
	scanURL, scanToken := baseURL, masterToken
	if command.TargetNodeID > 0 {
		if command.TargetPanelPort < 1024 || command.TargetPanelPort > 65535 || strings.TrimSpace(command.TargetAPIToken) == "" {
			return RealityCommandResult{}, errors.New("agent: target VLESS node API connection is unavailable")
		}
		scanURL = "http://" + net.JoinHostPort(listen, strconv.Itoa(command.TargetPanelPort))
		scanToken = strings.TrimSpace(command.TargetAPIToken)
	}
	clientEmail := threeXUIClientEmail(command.ClientName, commandID)
	inboundTag := command.InboundTag
	if existing, ok, err := findRealityInbound(ctx, baseURL, masterToken, inboundTag, command.TargetNodeID); err != nil {
		// The same command may be replaying after an Agent crash that occurred
		// immediately after the remote add committed. A failed deterministic tag
		// probe therefore has an unknown external outcome and cannot be terminal.
		if attempt <= 1 {
			return RealityCommandResult{}, err
		}
		return RealityCommandResult{}, deferUncertainRealityTask(attempt, err)
	} else if ok {
		existing, err = updateThreeXUIRealityInbound(ctx, baseURL, masterToken, existing.ID, command.TargetNodeID, command.DisplayName, &command.InboundTotalBytes)
		if err != nil {
			return RealityCommandResult{}, deferOrRollbackKnownRealityTask(ctx, attempt, err, baseURL, masterToken, existing.ID, inboundTag, command.TargetNodeID, clientEmail, command.CreateInitialClient)
		}
		existing, err = ensureThreeXUIRealityMinimumClientVersion(ctx, baseURL, masterToken, existing.ID)
		if err != nil {
			return RealityCommandResult{}, deferOrRollbackKnownRealityTask(ctx, attempt, err, baseURL, masterToken, existing.ID, inboundTag, command.TargetNodeID, clientEmail, command.CreateInitialClient)
		}
		result, err := realityResultFromInbound(existing, existing.Tag, command.ConnectHostname, command.DisplayName, command.ClientName, clientEmail)
		if err != nil {
			rollbackErr := rollbackThreeXUIRealityCreation(ctx, baseURL, masterToken, existing.ID, inboundTag, command.TargetNodeID, clientEmail, command.CreateInitialClient)
			if rollbackErr != nil {
				return RealityCommandResult{}, deferUncertainRealityTask(attempt, errors.Join(err, rollbackErr))
			}
			return RealityCommandResult{}, err
		}
		if command.CreateInitialClient && command.ClientResetDays > 0 && command.ClientExpiryTime <= store.now().UTC().UnixMilli() {
			cause := errors.New("agent: REALITY creation parameters are invalid")
			if rollbackErr := rollbackThreeXUIRealityCreation(ctx, baseURL, masterToken, result.InboundID, inboundTag, command.TargetNodeID, clientEmail, true); rollbackErr != nil {
				return RealityCommandResult{}, deferUncertainRealityTask(attempt, errors.Join(cause, rollbackErr))
			}
			return RealityCommandResult{}, cause
		}
		if command.CreateInitialClient && !result.ClientCreated {
			// A prior compensation can delete the global client while failing to
			// delete the inbound. Reusing that half-state would make Center reject
			// the result and strand the active inbound, so remove it and recreate
			// the deterministic pair atomically.
			if rollbackErr := rollbackThreeXUIRealityCreation(ctx, baseURL, masterToken, result.InboundID, inboundTag, command.TargetNodeID, clientEmail, true); rollbackErr != nil {
				return RealityCommandResult{}, deferUncertainRealityTask(attempt, rollbackErr)
			}
		} else {
			if err := syncThreeXUIRealityHost(ctx, baseURL, masterToken, result.InboundID, result.ConnectHostname, result.SNIHostname); err != nil {
				return RealityCommandResult{}, deferOrRollbackKnownRealityTask(ctx, attempt, err, baseURL, masterToken, result.InboundID, inboundTag, command.TargetNodeID, clientEmail, command.CreateInitialClient)
			}
			if err := attachAllThreeXUIClientsToInbound(ctx, baseURL, masterToken, result.InboundID); err != nil {
				return RealityCommandResult{}, deferOrRollbackKnownRealityTask(ctx, attempt, err, baseURL, masterToken, result.InboundID, inboundTag, command.TargetNodeID, clientEmail, command.CreateInitialClient)
			}
			if command.CreateInitialClient && result.ClientCreated {
				if err := attachThreeXUIClientToAllManagedRealityInbounds(ctx, baseURL, masterToken, clientEmail); err != nil {
					return RealityCommandResult{}, deferOrRollbackKnownRealityTask(ctx, attempt, err, baseURL, masterToken, result.InboundID, inboundTag, command.TargetNodeID, clientEmail, true)
				}
			}
			return result, nil
		}
	}
	if command.CreateInitialClient && command.ClientResetDays > 0 && command.ClientExpiryTime <= store.now().UTC().UnixMilli() {
		return RealityCommandResult{}, errors.New("agent: REALITY creation parameters are invalid")
	}
	// The client name is derived from the command ID. If the deterministic
	// inbound no longer exists, a same-name client can only be residue from an
	// interrupted compensation and must not be reused with new credentials.
	if command.CreateInitialClient {
		if err := deleteThreeXUIClientIfExists(ctx, baseURL, masterToken, clientEmail); err != nil {
			return RealityCommandResult{}, deferUncertainRealityTask(attempt, fmt.Errorf("agent: clean incomplete REALITY client: %w", err))
		}
	}

	target, sni, err := selectRealityTarget(ctx, scanURL, scanToken, command)
	if err != nil {
		return RealityCommandResult{}, err
	}
	keys, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/server/getNewX25519Cert", masterToken, "", nil)
	if err != nil {
		return RealityCommandResult{}, fmt.Errorf("agent: generate REALITY keys: %w", err)
	}
	var keyPair struct {
		PrivateKey string `json:"privateKey"`
		PublicKey  string `json:"publicKey"`
	}
	if json.Unmarshal(keys, &keyPair) != nil || keyPair.PrivateKey == "" || keyPair.PublicKey == "" {
		return RealityCommandResult{}, errors.New("agent: 3x-ui returned invalid REALITY keys")
	}
	clientCreated := command.CreateInitialClient
	clientID := ""
	clients := []map[string]any{}
	if clientCreated {
		clientID, err = randomUUID()
		if err != nil {
			return RealityCommandResult{}, err
		}
		clients = append(clients, map[string]any{
			"id": clientID, "email": clientEmail, "flow": "xtls-rprx-vision", "limitIp": 0,
			"totalGB": command.ClientTotalBytes, "expiryTime": command.ClientExpiryTime, "reset": command.ClientResetDays, "enable": true,
		})
	}
	shortBytes := make([]byte, 8)
	if _, err := rand.Read(shortBytes); err != nil {
		return RealityCommandResult{}, fmt.Errorf("agent: generate REALITY short id: %w", err)
	}
	shortID := hex.EncodeToString(shortBytes)
	port, err := availableRealityPort(ctx, baseURL, masterToken, command.TargetNodeID, listen)
	if err != nil {
		return RealityCommandResult{}, err
	}
	payload := map[string]any{
		"enable": true, "tag": inboundTag, "remark": command.DisplayName, "listen": "0.0.0.0", "port": port, "protocol": "vless", "expiryTime": 0,
		"total": command.InboundTotalBytes, "trafficReset": "never", "trafficResetDay": 1,
		"settings":       map[string]any{"clients": clients, "decryption": "none", "encryption": "none", "fallbacks": []any{}},
		"streamSettings": threeXUIRealityStreamSettings(target, sni, keyPair.PrivateKey, keyPair.PublicKey, shortID),
		"sniffing":       map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "metadataOnly": false, "routeOnly": false},
	}
	if command.TargetNodeID > 0 {
		payload["nodeId"] = command.TargetNodeID
	}
	added, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/add", masterToken, "application/json", payload)
	var inbound threeXUIRealityInbound
	addResponseValid := err == nil && json.Unmarshal(added, &inbound) == nil && inbound.ID > 0
	recoveredAfterAdd := false
	if !addResponseValid {
		recovered, found, recoveryErr := findRealityInbound(ctx, baseURL, masterToken, inboundTag, command.TargetNodeID)
		if recoveryErr != nil {
			if err != nil {
				return RealityCommandResult{}, deferUncertainRealityTask(attempt, errors.Join(fmt.Errorf("agent: create 3x-ui REALITY inbound: %w", err), recoveryErr))
			}
			return RealityCommandResult{}, deferUncertainRealityTask(attempt, errors.Join(errors.New("agent: 3x-ui returned an invalid REALITY inbound"), recoveryErr))
		}
		if !found {
			if err != nil {
				return RealityCommandResult{}, fmt.Errorf("agent: create 3x-ui REALITY inbound: %w", err)
			}
			return RealityCommandResult{}, errors.New("agent: 3x-ui returned an invalid REALITY inbound")
		}
		inbound = recovered
		recoveredAfterAdd = true
	}
	if strings.TrimSpace(inbound.Tag) == "" {
		inbound.Tag = command.InboundTag
	}
	result := RealityCommandResult{}
	if recoveredAfterAdd {
		result, err = realityResultFromInbound(inbound, inbound.Tag, command.ConnectHostname, command.DisplayName, command.ClientName, clientEmail)
		if err != nil {
			rollbackErr := rollbackThreeXUIRealityCreation(ctx, baseURL, masterToken, inbound.ID, inboundTag, command.TargetNodeID, clientEmail, clientCreated)
			if rollbackErr != nil {
				return RealityCommandResult{}, deferUncertainRealityTask(attempt, errors.Join(err, rollbackErr))
			}
			return RealityCommandResult{}, err
		}
		if command.CreateInitialClient && !result.ClientCreated {
			rollbackErr := rollbackThreeXUIRealityCreation(ctx, baseURL, masterToken, result.InboundID, inboundTag, command.TargetNodeID, clientEmail, true)
			if rollbackErr != nil {
				return RealityCommandResult{}, deferUncertainRealityTask(attempt, rollbackErr)
			}
			if recreatedRecoveredHalfState {
				return RealityCommandResult{}, deferUncertainRealityTask(attempt, errors.New("agent: recovered REALITY inbound did not contain its initial client after recreation"))
			}
			// The add may have committed only the inbound before its response was
			// lost. After verified compensation, recreate the deterministic pair in
			// the same task so Center never receives a success missing its client.
			return applyRealityCommandWithRecovery(ctx, store, commandID, attempt, command, true)
		}
	} else {
		result = RealityCommandResult{Action: "create", InboundID: inbound.ID, DisplayName: command.DisplayName, ClientName: command.ClientName, Listen: listen, Port: port, Target: target, SNIHostname: sni, ConnectHostname: command.ConnectHostname, InboundTag: inbound.Tag, ClientCreated: clientCreated, InboundTotalBytes: command.InboundTotalBytes}
		if clientCreated {
			result.ShareURI = realityShareURI(clientID, command.ConnectHostname, command.DisplayName, sni, keyPair.PublicKey, shortID)
		}
	}
	if err := completeThreeXUIRealityCreation(ctx, baseURL, masterToken, result, clientEmail); err != nil {
		rollbackErr := rollbackThreeXUIRealityCreation(ctx, baseURL, masterToken, result.InboundID, inboundTag, command.TargetNodeID, clientEmail, clientCreated)
		if rollbackErr != nil {
			return RealityCommandResult{}, deferUncertainRealityTask(attempt, errors.Join(err, rollbackErr))
		}
		return RealityCommandResult{}, err
	}
	return result, nil
}

func completeThreeXUIRealityCreation(ctx context.Context, baseURL, token string, result RealityCommandResult, clientEmail string) error {
	if err := syncThreeXUIRealityHost(ctx, baseURL, token, result.InboundID, result.ConnectHostname, result.SNIHostname); err != nil {
		return err
	}
	if err := attachAllThreeXUIClientsToInbound(ctx, baseURL, token, result.InboundID); err != nil {
		return err
	}
	if result.ClientCreated {
		if err := attachThreeXUIClientToAllManagedRealityInbounds(ctx, baseURL, token, clientEmail); err != nil {
			return err
		}
	}
	return nil
}

func rollbackThreeXUIRealityCreation(ctx context.Context, baseURL, token string, inboundID int, inboundTag string, nodeID int, clientEmail string, clientCreated bool) error {
	failures := []error{}
	if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/del/"+strconv.Itoa(inboundID), token, "application/json", map[string]any{}); err != nil {
		_, found, verifyErr := findRealityInbound(ctx, baseURL, token, inboundTag, nodeID)
		if verifyErr != nil {
			failures = append(failures, errors.Join(fmt.Errorf("agent: remove incomplete REALITY inbound: %w", err), fmt.Errorf("agent: verify incomplete REALITY inbound cleanup: %w", verifyErr)))
		} else if found {
			failures = append(failures, fmt.Errorf("agent: remove incomplete REALITY inbound: %w", err))
		}
	}
	if clientCreated {
		if err := deleteThreeXUIClientIfExists(ctx, baseURL, token, clientEmail); err != nil {
			failures = append(failures, fmt.Errorf("agent: remove incomplete REALITY client: %w", err))
		}
	}
	return errors.Join(failures...)
}

func renameThreeXUIRealityInbound(ctx context.Context, baseURL, token string, command RealityCommandTask) (RealityCommandResult, error) {
	if command.InboundID < 1 || !validRealityDisplayName(command.DisplayName) {
		return RealityCommandResult{}, errors.New("agent: REALITY rename parameters are invalid")
	}
	if (command.ConnectHostname != "" || command.SNIHostname != "") && (!validThreeXUIShareHostname(strings.ToLower(strings.TrimSuffix(strings.TrimSpace(command.ConnectHostname), "."))) || !validThreeXUIShareHostname(strings.ToLower(strings.TrimSuffix(strings.TrimSpace(command.SNIHostname), ".")))) {
		return RealityCommandResult{}, errors.New("agent: REALITY rename subscription endpoint is invalid")
	}
	inbound, err := updateThreeXUIRealityInbound(ctx, baseURL, token, command.InboundID, command.TargetNodeID, command.DisplayName, nil)
	if err != nil {
		return RealityCommandResult{}, err
	}
	if command.ConnectHostname != "" || command.SNIHostname != "" {
		if err := syncThreeXUIRealityHost(ctx, baseURL, token, command.InboundID, command.ConnectHostname, command.SNIHostname); err != nil {
			// The inbound rename is already committed at this point. Even an
			// explicit host-update rejection cannot make the overall two-stage
			// command terminal: replay the same ID until both resources converge.
			return RealityCommandResult{}, uncertainRealityMutation(err)
		}
	}
	return RealityCommandResult{Action: "rename", InboundID: inbound.ID, DisplayName: inbound.Remark}, nil
}

func updateThreeXUIRealityInbound(ctx context.Context, baseURL, token string, inboundID, nodeID int, displayName string, totalBytes *int64) (threeXUIRealityInbound, error) {
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/get/"+strconv.Itoa(inboundID), token, "", nil)
	if err != nil {
		return threeXUIRealityInbound{}, fmt.Errorf("agent: get 3x-ui REALITY inbound for rename: %w", err)
	}
	var inbound threeXUIRealityInbound
	var update map[string]any
	if json.Unmarshal(payload, &inbound) != nil || json.Unmarshal(payload, &update) != nil || inbound.ID != inboundID || inbound.Protocol != "vless" || !threeXUIInboundMatchesNode(inbound, nodeID) {
		return threeXUIRealityInbound{}, errors.New("agent: selected REALITY inbound is unavailable on this node")
	}
	var stream struct {
		Security string `json:"security"`
	}
	if json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Security != "reality" {
		return threeXUIRealityInbound{}, errors.New("agent: selected inbound is not VLESS REALITY")
	}
	changed := inbound.Remark != displayName || inbound.TrafficReset != "never" || inbound.TrafficResetDay != 1
	if totalBytes != nil && inbound.Total != *totalBytes {
		changed = true
	}
	if !changed {
		return inbound, nil
	}
	update["remark"] = displayName
	update["trafficReset"] = "never"
	update["trafficResetDay"] = 1
	if totalBytes != nil {
		update["total"] = *totalBytes
	}
	delete(update, "id")
	delete(update, "clientStats")
	delete(update, "fallbackParent")
	if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/update/"+strconv.Itoa(inboundID), token, "application/json", update); err != nil {
		cause := fmt.Errorf("agent: rename 3x-ui REALITY inbound: %w", err)
		// A failed mutation response is ambiguous until the same resource is read
		// back. A confirmed old value is a normal terminal failure; an unavailable
		// read-back retains the command for same-ID reconciliation.
		observedPayload, observeErr := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/get/"+strconv.Itoa(inboundID), token, "", nil)
		if observeErr != nil {
			return threeXUIRealityInbound{}, uncertainRealityMutation(errors.Join(cause, fmt.Errorf("agent: verify 3x-ui REALITY rename: %w", observeErr)))
		}
		var observed threeXUIRealityInbound
		if json.Unmarshal(observedPayload, &observed) != nil || observed.ID != inboundID {
			return threeXUIRealityInbound{}, uncertainRealityMutation(errors.Join(cause, errors.New("agent: 3x-ui returned invalid REALITY rename verification")))
		}
		matches := observed.Remark == displayName && observed.TrafficReset == "never" && observed.TrafficResetDay == 1
		if totalBytes != nil {
			matches = matches && observed.Total == *totalBytes
		}
		if matches {
			return observed, nil
		}
		return threeXUIRealityInbound{}, cause
	}
	inbound.Remark = displayName
	inbound.TrafficReset = "never"
	inbound.TrafficResetDay = 1
	if totalBytes != nil {
		inbound.Total = *totalBytes
	}
	return inbound, nil
}

func threeXUIRealityTag(commandID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(commandID)))
	return "vastora-" + hex.EncodeToString(digest[:12])
}

func validRealityDisplayName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > 64 || strings.ContainsAny(value, "/\\") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func threeXUIInboundMatchesNode(inbound threeXUIRealityInbound, nodeID int) bool {
	return (nodeID == 0 && inbound.NodeID == nil) || (nodeID > 0 && inbound.NodeID != nil && *inbound.NodeID == nodeID)
}

func attachAllThreeXUIClientsToInbound(ctx context.Context, baseURL, token string, inboundID int) error {
	clients, err := listThreeXUIClients(ctx, baseURL, token)
	if err != nil {
		return fmt.Errorf("agent: list clients for automatic node synchronization: %w", err)
	}
	emails := make([]string, 0, len(clients))
	for _, client := range clients {
		if !containsInt(client.InboundIDs, inboundID) {
			emails = append(emails, client.Email)
		}
	}
	if len(emails) == 0 {
		return nil
	}
	payload, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/clients/bulkAttach", token, "application/json", map[string]any{"emails": emails, "inboundIds": []int{inboundID}})
	if err != nil {
		return fmt.Errorf("agent: attach existing clients to the new REALITY node: %w", err)
	}
	var result struct {
		Errors []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(payload, &result) != nil || len(result.Errors) != 0 {
		return errors.New("agent: 3x-ui could not attach every existing client to the new REALITY node")
	}
	return nil
}

func attachThreeXUIClientToAllManagedRealityInbounds(ctx context.Context, baseURL, token, email string) error {
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/list", token, "", nil)
	if err != nil {
		return fmt.Errorf("agent: list managed REALITY nodes for initial client: %w", err)
	}
	var inbounds []threeXUIRealityInbound
	if json.Unmarshal(payload, &inbounds) != nil {
		return errors.New("agent: 3x-ui returned invalid inbounds while synchronizing the initial client")
	}
	detail, err := getThreeXUIClient(ctx, baseURL, token, email)
	if err != nil {
		return fmt.Errorf("agent: read initial 3x-ui client associations: %w", err)
	}
	desired := append([]int(nil), detail.InboundIDs...)
	for _, inbound := range inbounds {
		if inbound.ID < 1 || inbound.Protocol != "vless" || !managedThreeXUIRealityTag(inbound.Tag) || containsInt(desired, inbound.ID) {
			continue
		}
		var stream struct {
			Security string `json:"security"`
		}
		if json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Security != "reality" {
			continue
		}
		desired = append(desired, inbound.ID)
	}
	if len(desired) == 0 {
		return errors.New("agent: no managed REALITY node is available for the initial client")
	}
	return syncThreeXUIClientInbounds(ctx, baseURL, token, email, detail.InboundIDs, desired)
}

func managedThreeXUIRealityTag(tag string) bool {
	tag = strings.TrimSpace(tag)
	if strings.HasPrefix(tag, "vastora-") {
		return true
	}
	if !strings.HasPrefix(tag, "n") {
		return false
	}
	separator := strings.IndexByte(tag, '-')
	if separator < 2 || !strings.HasPrefix(tag[separator+1:], "vastora-") {
		return false
	}
	_, err := strconv.Atoi(tag[1:separator])
	return err == nil
}

func containsInt(values []int, expected int) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func threeXUIRealityStreamSettings(target, sni, privateKey, publicKey, shortID string) map[string]any {
	return map[string]any{
		"network":     "tcp",
		"tcpSettings": map[string]any{"header": map[string]string{"type": "none"}},
		"security":    "reality",
		"realitySettings": map[string]any{
			"show": false, "xver": 0, "target": target, "serverNames": []string{sni},
			"privateKey": privateKey, "minClientVer": threeXUIRealityMinClientVersion,
			"maxClientVer": "", "maxTimediff": 0, "shortIds": []string{shortID},
			"settings": map[string]any{"publicKey": publicKey, "fingerprint": "chrome", "serverName": "", "spiderX": "/"},
		},
	}
}

func ensureThreeXUIRealityMinimumClientVersion(ctx context.Context, baseURL, token string, inboundID int) (threeXUIRealityInbound, error) {
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/get/"+strconv.Itoa(inboundID), token, "", nil)
	if err != nil {
		return threeXUIRealityInbound{}, fmt.Errorf("agent: get 3x-ui REALITY inbound: %w", err)
	}
	var inbound threeXUIRealityInbound
	var update map[string]any
	if json.Unmarshal(payload, &inbound) != nil || json.Unmarshal(payload, &update) != nil || inbound.ID != inboundID {
		return threeXUIRealityInbound{}, errors.New("agent: 3x-ui returned invalid REALITY inbound data")
	}
	streamSettings, ok := update["streamSettings"].(map[string]any)
	if !ok || streamSettings["security"] != "reality" || update["protocol"] != "vless" {
		return threeXUIRealityInbound{}, errors.New("agent: selected inbound is not VLESS REALITY")
	}
	realitySettings, ok := streamSettings["realitySettings"].(map[string]any)
	if !ok {
		return threeXUIRealityInbound{}, errors.New("agent: selected REALITY inbound is incomplete")
	}
	if minClientVersion, _ := realitySettings["minClientVer"].(string); strings.TrimSpace(minClientVersion) == threeXUIRealityMinClientVersion {
		return inbound, nil
	}
	realitySettings["minClientVer"] = threeXUIRealityMinClientVersion
	delete(update, "id")
	delete(update, "clientStats")
	delete(update, "fallbackParent")
	if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/update/"+strconv.Itoa(inboundID), token, "application/json", update); err != nil {
		return threeXUIRealityInbound{}, fmt.Errorf("agent: set 3x-ui REALITY minimum client version: %w", err)
	}
	encodedStreamSettings, err := json.Marshal(streamSettings)
	if err != nil {
		return threeXUIRealityInbound{}, err
	}
	inbound.StreamSettings = encodedStreamSettings
	return inbound, nil
}

func syncThreeXUIRealityHost(ctx context.Context, baseURL, token string, inboundID int, connectHostname, sniHostname string) error {
	connectHostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(connectHostname), "."))
	sniHostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(sniHostname), "."))
	if inboundID < 1 || !validThreeXUIShareHostname(connectHostname) || !validThreeXUIShareHostname(sniHostname) {
		return errors.New("agent: invalid public REALITY subscription endpoint")
	}
	groupID := "vastora-public-" + strconv.Itoa(inboundID)
	desired := threeXUIHostGroup{
		GroupID: groupID, InboundIDs: []int{inboundID}, Hosts: []string{connectHostname},
		Remark: threeXUISubscriptionRemarkTemplate, ServerDescription: "Managed by Vastora",
		Tags: []string{"vastora"}, Port: 443, Security: "same", SNI: sniHostname,
		Fingerprint: "chrome", MihomoIPVersion: "dual",
	}
	groups, err := threeXUIRealityHostGroups(ctx, baseURL, token, inboundID)
	if err != nil {
		return err
	}
	endpoint := baseURL + "/panel/api/hosts/add"
	for _, group := range groups {
		if group.GroupID != groupID {
			continue
		}
		if threeXUIRealityHostMatches(group, desired) {
			return nil
		}
		endpoint = baseURL + "/panel/api/hosts/update/" + url.PathEscape(groupID)
		break
	}
	if _, err := threeXUIAPI(ctx, http.MethodPost, endpoint, token, "application/json", desired); err != nil {
		cause := fmt.Errorf("agent: synchronize 3x-ui REALITY subscription host: %w", err)
		observed, observeErr := threeXUIRealityHostGroups(ctx, baseURL, token, inboundID)
		if observeErr != nil {
			return uncertainRealityMutation(errors.Join(cause, fmt.Errorf("agent: verify 3x-ui REALITY subscription host: %w", observeErr)))
		}
		for _, group := range observed {
			if group.GroupID == groupID && threeXUIRealityHostMatches(group, desired) {
				return nil
			}
		}
		return cause
	}
	return nil
}

func threeXUIRealityHostGroups(ctx context.Context, baseURL, token string, inboundID int) ([]threeXUIHostGroup, error) {
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/hosts/byInbound/"+strconv.Itoa(inboundID), token, "", nil)
	if err != nil {
		return nil, fmt.Errorf("agent: list 3x-ui REALITY subscription hosts: %w", err)
	}
	var groups []threeXUIHostGroup
	if json.Unmarshal(payload, &groups) != nil {
		return nil, errors.New("agent: 3x-ui returned invalid subscription host data")
	}
	return groups, nil
}

func validThreeXUIShareHostname(value string) bool {
	if value == "" || len(value) > 253 || strings.ContainsAny(value, "/\\?#@:") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func threeXUIRealityHostMatches(actual, desired threeXUIHostGroup) bool {
	return actual.GroupID == desired.GroupID && len(actual.InboundIDs) == 1 && actual.InboundIDs[0] == desired.InboundIDs[0] &&
		len(actual.Hosts) == 1 && strings.EqualFold(strings.TrimSuffix(actual.Hosts[0], "."), desired.Hosts[0]) &&
		actual.Remark == desired.Remark && actual.ServerDescription == desired.ServerDescription && !actual.IsDisabled && !actual.IsHidden &&
		actual.Port == desired.Port && actual.Security == desired.Security && strings.EqualFold(strings.TrimSuffix(actual.SNI, "."), desired.SNI) &&
		actual.Fingerprint == desired.Fingerprint && actual.MihomoIPVersion == desired.MihomoIPVersion && len(actual.Tags) == 1 && actual.Tags[0] == "vastora"
}

func threeXUIClientEmail(name, commandID string) string {
	var value strings.Builder
	separator := false
	for _, character := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(character) || unicode.IsNumber(character) || character == '_' || character == '.' || character == '-' {
			if separator && value.Len() != 0 {
				value.WriteByte('-')
			}
			value.WriteRune(character)
			separator = false
		} else {
			separator = true
		}
	}
	base := strings.Trim(value.String(), "-._")
	if base == "" {
		base = "client"
	}
	if characters := []rune(base); len(characters) > 48 {
		base = strings.TrimRight(string(characters[:48]), "-._")
	}
	suffix := strings.TrimPrefix(commandID, "application-command-")
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	return base + "-" + suffix
}

func selectRealityTarget(ctx context.Context, baseURL, token string, command RealityCommandTask) (string, string, error) {
	excluded := map[string]bool{}
	for _, hostname := range command.ExcludedSNI {
		excluded[strings.ToLower(strings.TrimSpace(hostname))] = true
	}
	if command.Target != "" {
		form := url.Values{"target": {command.Target}, "sni": {command.SNIHostname}, "xver": {"0"}}
		result, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/server/scanRealityTarget", token, "application/x-www-form-urlencoded", form)
		if err != nil {
			return "", "", fmt.Errorf("agent: scan custom REALITY target: %w", err)
		}
		var scan realityScanResult
		if json.Unmarshal(result, &scan) != nil || !scan.Feasible || excluded[command.SNIHostname] || !realityScanAllowsSNI(scan, command.SNIHostname) {
			return "", "", errors.New("agent: custom REALITY target is not feasible, its certificate does not cover the SNI, or the SNI is already in use")
		}
		return scan.Target, command.SNIHostname, nil
	}
	result, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/server/scanRealityTargets", token, "application/x-www-form-urlencoded", url.Values{"targets": {""}})
	if err != nil {
		return "", "", fmt.Errorf("agent: scan REALITY targets: %w", err)
	}
	var scans []realityScanResult
	if json.Unmarshal(result, &scans) != nil {
		return "", "", errors.New("agent: 3x-ui returned invalid REALITY target candidates")
	}
	for _, scan := range scans {
		if !scan.Feasible {
			continue
		}
		candidates := append([]string(nil), scan.ServerNames...)
		candidates = append(candidates, scan.Host)
		for _, candidate := range candidates {
			candidate = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(candidate), "."))
			if candidate != "" && !strings.HasPrefix(candidate, "*.") && !excluded[candidate] {
				return scan.Target, candidate, nil
			}
		}
	}
	return "", "", errors.New("agent: no feasible unused REALITY target was found from this node")
}

func realityScanAllowsSNI(scan realityScanResult, hostname string) bool {
	hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	for _, candidate := range append(append([]string(nil), scan.ServerNames...), scan.Host) {
		candidate = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(candidate), "."))
		if candidate == hostname {
			return true
		}
		if strings.HasPrefix(candidate, "*.") {
			suffix := strings.TrimPrefix(candidate, "*")
			prefix := strings.TrimSuffix(hostname, suffix)
			if prefix != hostname && prefix != "" && !strings.Contains(prefix, ".") {
				return true
			}
		}
	}
	return false
}

func findRealityInbound(ctx context.Context, baseURL, token, inboundTag string, nodeID int) (threeXUIRealityInbound, bool, error) {
	result, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/list", token, "", nil)
	if err != nil {
		return threeXUIRealityInbound{}, false, fmt.Errorf("agent: list 3x-ui inbounds: %w", err)
	}
	var inbounds []threeXUIRealityInbound
	if json.Unmarshal(result, &inbounds) != nil {
		return threeXUIRealityInbound{}, false, errors.New("agent: 3x-ui returned invalid inbound data")
	}
	for _, inbound := range inbounds {
		if normalizedThreeXUIInboundTag(inbound.Tag, nodeID) == normalizedThreeXUIInboundTag(inboundTag, nodeID) && threeXUIInboundMatchesNode(inbound, nodeID) {
			return inbound, true, nil
		}
	}
	return threeXUIRealityInbound{}, false, nil
}

func realityResultFromInbound(inbound threeXUIRealityInbound, inboundTag, connectHostname, displayName, clientName, clientEmail string) (RealityCommandResult, error) {
	var settings struct {
		Clients []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"clients"`
	}
	var stream struct {
		Security string `json:"security"`
		Reality  struct {
			Target      string   `json:"target"`
			ServerNames []string `json:"serverNames"`
			ShortIDs    []string `json:"shortIds"`
			Settings    struct {
				PublicKey string `json:"publicKey"`
			} `json:"settings"`
		} `json:"realitySettings"`
	}
	if json.Unmarshal(inbound.Settings, &settings) != nil || json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Security != "reality" || len(stream.Reality.ServerNames) == 0 || len(stream.Reality.ShortIDs) == 0 || stream.Reality.Settings.PublicKey == "" {
		return RealityCommandResult{}, errors.New("agent: existing Vastora REALITY inbound is incomplete")
	}
	clientID := ""
	for _, client := range settings.Clients {
		if client.Email == clientEmail {
			clientID = client.ID
			break
		}
	}
	result := RealityCommandResult{Action: "create", InboundID: inbound.ID, DisplayName: displayName, ClientName: clientName, Listen: inbound.Listen, Port: inbound.Port, Target: stream.Reality.Target, SNIHostname: stream.Reality.ServerNames[0], ConnectHostname: connectHostname, InboundTag: inboundTag, ClientCreated: clientID != "", InboundTotalBytes: inbound.Total}
	if clientID != "" {
		result.ShareURI = realityShareURI(clientID, connectHostname, displayName, stream.Reality.ServerNames[0], stream.Reality.Settings.PublicKey, stream.Reality.ShortIDs[0])
	}
	return result, nil
}

func threeXUIAPI(ctx context.Context, method, endpoint, token, contentType string, payload any) (json.RawMessage, error) {
	var body io.Reader
	if payload != nil {
		switch value := payload.(type) {
		case url.Values:
			body = strings.NewReader(value.Encode())
		default:
			encoded, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			body = bytes.NewReader(encoded)
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := (&http.Client{Timeout: 45 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var envelope struct {
		Success bool            `json:"success"`
		Message string          `json:"msg"`
		Object  json.RawMessage `json:"obj"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&envelope) != nil || response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
		return nil, fmt.Errorf("3x-ui rejected the request: %s", strings.TrimSpace(envelope.Message))
	}
	return envelope.Object, nil
}

func availableTCPPort(address string) (int, error) {
	for port := threeXUIRealityPortFirst; port <= threeXUIRealityPortLast; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort(address, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		_ = listener.Close()
		return port, nil
	}
	return 0, errors.New("agent: no mapped private REALITY port is available")
}

func availableRealityPort(ctx context.Context, baseURL, token string, nodeID int, address string) (int, error) {
	if nodeID == 0 {
		return availableTCPPort(address)
	}
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/list", token, "", nil)
	if err != nil {
		return 0, fmt.Errorf("agent: inspect remote REALITY ports: %w", err)
	}
	var inbounds []struct {
		Port   int  `json:"port"`
		NodeID *int `json:"nodeId,omitempty"`
	}
	if json.Unmarshal(payload, &inbounds) != nil {
		return 0, errors.New("agent: 3x-ui returned invalid remote inbound data")
	}
	used := map[int]bool{}
	for _, inbound := range inbounds {
		if inbound.NodeID != nil && *inbound.NodeID == nodeID {
			used[inbound.Port] = true
		}
	}
	for port := threeXUIRealityPortFirst; port <= threeXUIRealityPortLast; port++ {
		if !used[port] {
			return port, nil
		}
	}
	return 0, errors.New("agent: could not allocate an unused remote REALITY port")
}

func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("agent: generate REALITY client id: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func realityShareURI(clientID, hostname, name, sni, publicKey, shortID string) string {
	query := url.Values{"encryption": {"none"}, "flow": {"xtls-rprx-vision"}, "security": {"reality"}, "sni": {sni}, "fp": {"chrome"}, "pbk": {publicKey}, "sid": {shortID}, "spx": {"/"}, "type": {"tcp"}, "headerType": {"none"}}
	return "vless://" + clientID + "@" + net.JoinHostPort(hostname, "443") + "?" + query.Encode() + "#" + url.PathEscape(name)
}
