package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/distribution/reference"
	"github.com/google/uuid"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/secret"
)

const (
	xrayWorkerStateAAD       = "agent-xray-worker"
	xrayWorkerMaxBody        = 4 << 20
	xrayWorkerVersion        = "26.9.9"
	xrayWorkerImageReference = "ghcr.io/xtls/xray-core:26.9.9@sha256:45338c4df61fda061c47ce62aafda6c5d7d59cbdefc33f2e335d8b0c748b748a"
	xrayWorkerNonRootUID     = 65532
)

func xrayWorkerRuntimeUID() int {
	if os.Geteuid() == 0 {
		return xrayWorkerNonRootUID
	}
	return os.Geteuid()
}

// xrayWorkerState is the authoritative worker-side projection used during the
// controller migration. It intentionally models only the Xray data plane and
// the narrow API surface needed by Vastora; it is not a replacement panel.
type xrayWorkerState struct {
	ApplicationID      string                       `json:"applicationId"`
	ImageReference     string                       `json:"imageReference"`
	Address            string                       `json:"address"`
	PanelPort          int                          `json:"panelPort"`
	APIToken           string                       `json:"apiToken"`
	Revision           uint64                       `json:"revision"`
	AppliedRevision    uint64                       `json:"appliedRevision"`
	NextInboundID      int                          `json:"nextInboundId"`
	Inbounds           []json.RawMessage            `json:"inbounds"`
	XraySetting        json.RawMessage              `json:"xraySetting"`
	RuntimeStats       map[string]int64             `json:"runtimeStats,omitempty"`
	AccountStats       map[string]xrayWorkerTraffic `json:"accountStats,omitempty"`
	ControllerID       string                       `json:"controllerId,omitempty"`
	ControllerStats    map[string]xrayWorkerTraffic `json:"controllerStats,omitempty"`
	BlockedAccounts    map[string]bool              `json:"blockedAccounts,omitempty"`
	LegacyImportSHA256 string                       `json:"legacyImportSha256,omitempty"`
}

type xrayWorkerTraffic struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type xrayWorkerAppliedReceipt struct {
	Revision     uint64 `json:"revision"`
	ConfigSHA256 string `json:"configSha256"`
}

func (state xrayWorkerState) validate() error {
	if strings.TrimSpace(state.ApplicationID) == "" || !validXrayWorkerImageReference(state.ImageReference) || !networking.IsPrivateServiceAddress(state.Address) || state.PanelPort < 1024 || state.PanelPort > 65535 || state.PanelPort == threeXUIRealityPort || strings.TrimSpace(state.APIToken) == "" || len(state.APIToken) > 4096 || state.Revision == 0 || state.AppliedRevision > state.Revision || state.NextInboundID < 1 {
		return errors.New("agent: invalid Xray worker state")
	}
	if state.LegacyImportSHA256 != "" {
		decoded, err := hex.DecodeString(state.LegacyImportSHA256)
		if err != nil || len(decoded) != sha256.Size || state.LegacyImportSHA256 != strings.ToLower(state.LegacyImportSHA256) {
			return errors.New("agent: invalid Xray worker migration fingerprint")
		}
	}
	seenID, seenTag := map[int]bool{}, map[string]bool{}
	protocolCount := map[string]int{}
	for _, raw := range state.Inbounds {
		var inbound map[string]any
		if json.Unmarshal(raw, &inbound) != nil {
			return errors.New("agent: invalid Xray worker inbound")
		}
		id, ok := jsonInteger(inbound["id"])
		tag, _ := inbound["tag"].(string)
		protocol, _ := inbound["protocol"].(string)
		port, portOK := jsonInteger(inbound["port"])
		if !ok || id < 1 || seenID[id] || strings.TrimSpace(tag) == "" || tag == "api" || seenTag[tag] || !portOK || port < 1 || port > 65535 || (protocol != "vless" && protocol != "hysteria") {
			return errors.New("agent: unsupported Xray worker inbound")
		}
		stream, _ := inbound["streamSettings"].(map[string]any)
		network, _ := stream["network"].(string)
		security, _ := stream["security"].(string)
		if port != threeXUIRealityPort || protocol == "vless" && (security != "reality" || network != "tcp" && network != "raw" || !xrayWorkerAcceptsProxyProtocol(stream)) || protocol == "hysteria" && (security != "tls" || network != "hysteria") {
			return errors.New("agent: unsupported Xray worker transport")
		}
		if protocol == "hysteria" {
			if _, exists := stream["finalmask"]; exists {
				return errors.New("agent: Hysteria finalmask is unsupported because wildcard listeners cannot preserve the destination address on multi-homed hosts")
			}
		}
		if !validXrayWorkerClients(inbound, protocol) {
			return errors.New("agent: invalid Xray worker clients")
		}
		protocolCount[protocol]++
		if protocolCount[protocol] > 1 {
			return errors.New("agent: Xray worker port has conflicting inbounds")
		}
		seenID[id], seenTag[tag] = true, true
	}
	var settings map[string]any
	if json.Unmarshal(state.XraySetting, &settings) != nil {
		return errors.New("agent: invalid Xray worker settings")
	}
	for name, value := range state.RuntimeStats {
		if strings.TrimSpace(name) == "" || value < 0 {
			return errors.New("agent: invalid Xray worker runtime counter")
		}
	}
	for email, traffic := range state.AccountStats {
		if strings.TrimSpace(email) == "" || traffic.Up < 0 || traffic.Down < 0 {
			return errors.New("agent: invalid Xray worker account counter")
		}
	}
	for email, traffic := range state.ControllerStats {
		if strings.TrimSpace(email) == "" || traffic.Up < 0 || traffic.Down < 0 {
			return errors.New("agent: invalid Xray worker controller counter")
		}
	}
	for email, blocked := range state.BlockedAccounts {
		if strings.TrimSpace(email) == "" || !blocked {
			return errors.New("agent: invalid Xray worker enforcement state")
		}
	}
	return nil
}

func validXrayWorkerImageReference(value string) bool {
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil || named.Name() != "ghcr.io/xtls/xray-core" {
		return false
	}
	_, tagged := named.(reference.NamedTagged)
	digested, pinned := named.(reference.Digested)
	return tagged && pinned && digested.Digest().Validate() == nil && digested.Digest().Algorithm().String() == "sha256"
}

func xrayWorkerImageVersion(value string) string {
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return ""
	}
	tagged, ok := named.(reference.NamedTagged)
	if !ok {
		return ""
	}
	return tagged.Tag()
}

func validXrayWorkerClients(inbound map[string]any, protocol string) bool {
	settings, ok := inbound["settings"].(map[string]any)
	if !ok {
		return false
	}
	clients, ok := settings["clients"].([]any)
	if !ok {
		return false
	}
	seen := map[string]bool{}
	for _, value := range clients {
		client, ok := value.(map[string]any)
		email, _ := client["email"].(string)
		if !ok || strings.TrimSpace(email) == "" || seen[email] {
			return false
		}
		seen[email] = true
		if total, exists := client["totalGB"]; exists {
			value, valid := jsonInteger64(total)
			if !valid || value < 0 {
				return false
			}
		}
		if expiry, exists := client["expiryTime"]; exists {
			value, valid := jsonInteger64(expiry)
			if !valid || value < 0 {
				return false
			}
		}
		if protocol == "vless" {
			id, _ := client["id"].(string)
			if _, err := uuid.Parse(id); err != nil {
				return false
			}
		} else if auth, _ := client["auth"].(string); strings.TrimSpace(auth) == "" {
			return false
		}
	}
	return true
}

func xrayWorkerAcceptsProxyProtocol(stream map[string]any) bool {
	sockopt, _ := stream["sockopt"].(map[string]any)
	accepted, _ := sockopt["acceptProxyProtocol"].(bool)
	return accepted
}

func jsonInteger(value any) (int, bool) {
	switch number := value.(type) {
	case float64:
		integer := int(number)
		return integer, number == float64(integer)
	case json.Number:
		integer, err := strconv.Atoi(number.String())
		return integer, err == nil
	case int:
		return number, true
	default:
		return 0, false
	}
}

func (s *Store) saveXrayWorkerState(ctx context.Context, state xrayWorkerState) error {
	if err := state.validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(s.key, encoded, []byte(xrayWorkerStateAAD))
	if err != nil {
		return fmt.Errorf("agent: encrypt Xray worker state: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO xray_worker_state(id,sealed_state) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET sealed_state=excluded.sealed_state`, sealed); err != nil {
		return fmt.Errorf("agent: save Xray worker state: %w", err)
	}
	return nil
}

func (s *Store) loadXrayWorkerState(ctx context.Context) (xrayWorkerState, error) {
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM xray_worker_state WHERE id=1`).Scan(&sealed); errors.Is(err, sql.ErrNoRows) {
		return xrayWorkerState{}, errApplicationNotInstalled
	} else if err != nil {
		return xrayWorkerState{}, err
	}
	encoded, err := secret.Open(s.key, sealed, []byte(xrayWorkerStateAAD))
	if err != nil {
		return xrayWorkerState{}, errors.New("agent: Xray worker state does not match the local key")
	}
	var state xrayWorkerState
	if json.Unmarshal(encoded, &state) != nil || state.validate() != nil {
		return xrayWorkerState{}, errors.New("agent: persisted Xray worker state is invalid")
	}
	return state, nil
}
