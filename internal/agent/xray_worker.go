package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

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

func renderXrayWorkerConfig(state xrayWorkerState) ([]byte, error) {
	if err := state.validate(); err != nil {
		return nil, err
	}
	var config map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(state.XraySetting)))
	decoder.UseNumber()
	if decoder.Decode(&config) != nil {
		return nil, errors.New("agent: invalid Xray worker settings")
	}
	runtimeInbounds := make([]any, 0, len(state.Inbounds)+1)
	runtimeInbounds = append(runtimeInbounds, map[string]any{"listen": "127.0.0.1", "port": 10085, "protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"}, "tag": "api"})
	for _, raw := range state.Inbounds {
		var source map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if decoder.Decode(&source) != nil {
			return nil, errors.New("agent: invalid Xray worker inbound")
		}
		enabled, _ := source["enable"].(bool)
		if !enabled {
			continue
		}
		protocol, _ := source["protocol"].(string)
		normalizeXrayWorkerClients(source, protocol, state.BlockedAccounts)
		for _, field := range []string{"id", "enable", "remark", "up", "down", "total", "expiryTime", "trafficReset", "trafficResetDay", "clientStats", "nodeId", "fallbackParent"} {
			delete(source, field)
		}
		// HAProxy owns public TCP/443. REALITY receives PROXY v2 on the
		// private service address; HY2 remains host-network UDP/443.
		if protocol == "vless" {
			source["listen"] = state.Address
		} else if protocol == "hysteria" {
			source["listen"] = "0.0.0.0"
		}
		runtimeInbounds = append(runtimeInbounds, source)
	}
	config["inbounds"] = runtimeInbounds
	config["api"] = map[string]any{"tag": "api", "services": []string{"HandlerService", "LoggerService", "StatsService"}}
	if outbounds, ok := config["outbounds"].([]any); ok {
		config["outbounds"] = slices.DeleteFunc(outbounds, func(value any) bool {
			object, _ := value.(map[string]any)
			return object["tag"] == "api"
		})
	}
	routing, _ := config["routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{"domainStrategy": "IPIfNonMatch"}
	}
	rules, _ := routing["rules"].([]any)
	rules = slices.DeleteFunc(rules, func(value any) bool {
		object, _ := value.(map[string]any)
		if object["outboundTag"] == "api" {
			return true
		}
		if object["inboundTag"] == "api" {
			return true
		}
		inbounds, _ := object["inboundTag"].([]any)
		return slices.Contains(inbounds, any("api"))
	})
	rules = append([]any{map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}}, rules...)
	routing["rules"] = rules
	config["routing"] = routing
	if config["log"] == nil {
		config["log"] = map[string]any{"loglevel": "warning"}
	}
	config["stats"] = map[string]any{}
	policy, _ := config["policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
	}
	levels, _ := policy["levels"].(map[string]any)
	if levels == nil {
		levels = map[string]any{}
	}
	level, _ := levels["0"].(map[string]any)
	if level == nil {
		level = map[string]any{}
	}
	level["statsUserUplink"], level["statsUserDownlink"] = true, true
	levels["0"] = level
	policy["levels"] = levels
	system, _ := policy["system"].(map[string]any)
	if system == nil {
		system = map[string]any{}
	}
	system["statsInboundUplink"], system["statsInboundDownlink"] = true, true
	policy["system"] = system
	config["policy"] = policy
	return json.Marshal(config)
}

func normalizeXrayWorkerClients(inbound map[string]any, protocol string, blockedAccounts map[string]bool) {
	settings, _ := inbound["settings"].(map[string]any)
	clients, _ := settings["clients"].([]any)
	if settings == nil || clients == nil {
		return
	}
	normalized := make([]any, 0, len(clients))
	for _, raw := range clients {
		client, _ := raw.(map[string]any)
		if client == nil {
			continue
		}
		email, _ := client["email"].(string)
		if enabled, exists := client["enable"].(bool); exists && !enabled || blockedAccounts[email] {
			continue
		}
		clean := map[string]any{}
		allowed := []string{"email"}
		if protocol == "vless" {
			allowed = append(allowed, "id", "flow", "level")
		} else {
			allowed = append(allowed, "auth")
		}
		for _, key := range allowed {
			if value, exists := client[key]; exists {
				clean[key] = value
			}
		}
		normalized = append(normalized, clean)
	}
	settings["clients"] = normalized
	inbound["settings"] = settings
}

func (s *Store) writeXrayWorkerConfig(state xrayWorkerState) (string, error) {
	staged, path, err := s.stageXrayWorkerConfig(state)
	if err != nil {
		return "", err
	}
	defer os.Remove(staged)
	if err := commitXrayWorkerConfig(staged, path); err != nil {
		return "", err
	}
	return path, nil
}

func commitXrayWorkerConfig(staged, path string) error {
	if filepath.Dir(staged) != filepath.Dir(path) {
		return errors.New("agent: Xray candidate must share the active configuration directory")
	}
	if err := os.Rename(staged, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return uncertainTaskOutcome(err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return uncertainTaskOutcome(err)
	}
	return nil
}

func (s *Store) recordXrayWorkerApplied(state xrayWorkerState) error {
	encoded, err := renderXrayWorkerConfig(state)
	if err != nil {
		return err
	}
	receipt, err := json.Marshal(xrayWorkerAppliedReceipt{Revision: state.Revision, ConfigSHA256: fmt.Sprintf("%x", sha256.Sum256(encoded))})
	if err != nil {
		return err
	}
	directory := filepath.Join(s.dataDir, "xray-worker")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "applied-*.json")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(receipt)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return commitXrayWorkerConfig(temporaryName, filepath.Join(directory, "applied.json"))
}

func (s *Store) xrayWorkerAppliedReceiptMatches(state xrayWorkerState) bool {
	encoded, err := os.ReadFile(filepath.Join(s.dataDir, "xray-worker", "applied.json"))
	if err != nil || len(encoded) > 4096 {
		return false
	}
	var receipt xrayWorkerAppliedReceipt
	if json.Unmarshal(encoded, &receipt) != nil || receipt.Revision != state.Revision {
		return false
	}
	config, err := renderXrayWorkerConfig(state)
	if err != nil || subtle.ConstantTimeCompare([]byte(receipt.ConfigSHA256), []byte(fmt.Sprintf("%x", sha256.Sum256(config)))) != 1 {
		return false
	}
	active, err := os.ReadFile(filepath.Join(s.dataDir, "xray-worker", "config.json"))
	return err == nil && len(active) <= xrayWorkerMaxBody && subtle.ConstantTimeCompare([]byte(receipt.ConfigSHA256), []byte(fmt.Sprintf("%x", sha256.Sum256(active)))) == 1
}

// stageXrayWorkerConfig writes a complete candidate beside the active file.
// The caller must validate it with Xray before atomically renaming it.
func (s *Store) stageXrayWorkerConfig(state xrayWorkerState) (string, string, error) {
	encoded, err := renderXrayWorkerConfig(state)
	if err != nil {
		return "", "", err
	}
	directory := filepath.Join(s.dataDir, "xray-worker")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", "", err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", "", err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(directory, xrayWorkerRuntimeUID(), -1); err != nil {
			return "", "", err
		}
	}
	temporary, err := os.CreateTemp(directory, "candidate-*.json")
	if err != nil {
		return "", "", err
	}
	temporaryName := temporary.Name()
	if os.Geteuid() == 0 {
		err = temporary.Chown(xrayWorkerRuntimeUID(), -1)
	}
	if err == nil {
		err = temporary.Chmod(0o600)
	}
	if err == nil {
		_, err = temporary.Write(encoded)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	path := filepath.Join(directory, "config.json")
	if err != nil {
		_ = os.Remove(temporaryName)
		return "", "", err
	}
	return temporaryName, path, nil
}

func xrayWorkerConfigChanged(previous, next xrayWorkerState) (bool, error) {
	before, err := renderXrayWorkerConfig(previous)
	if err != nil {
		return false, err
	}
	after, err := renderXrayWorkerConfig(next)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(before, after), nil
}

// xrayWorkerMigrationFingerprint records the complete imported business
// configuration while excluding monotonic traffic observations. It is used
// only to prove that a restored legacy writer has not changed after a failed
// cutover; it never becomes a second configuration source.
func xrayWorkerMigrationFingerprint(inbounds []json.RawMessage, xraySetting json.RawMessage) (string, error) {
	normalizedInbounds := make([]any, 0, len(inbounds))
	for _, raw := range inbounds {
		var inbound map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if decoder.Decode(&inbound) != nil || inbound == nil {
			return "", errors.New("agent: invalid legacy worker migration inbound")
		}
		delete(inbound, "up")
		delete(inbound, "down")
		delete(inbound, "clientStats")
		if settings, ok := inbound["settings"].(map[string]any); ok {
			if clients, ok := settings["clients"].([]any); ok {
				for _, rawClient := range clients {
					if client, ok := rawClient.(map[string]any); ok {
						delete(client, "up")
						delete(client, "down")
					}
				}
			}
		}
		normalizedInbounds = append(normalizedInbounds, inbound)
	}
	var settings any
	decoder := json.NewDecoder(strings.NewReader(string(xraySetting)))
	decoder.UseNumber()
	if decoder.Decode(&settings) != nil || settings == nil {
		return "", errors.New("agent: invalid legacy worker migration routing")
	}
	encoded, err := json.Marshal(struct {
		Inbounds    []any `json:"inbounds"`
		XraySetting any   `json:"xraySetting"`
	}{Inbounds: normalizedInbounds, XraySetting: settings})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

type xrayWorkerApply func(context.Context, xrayWorkerState, xrayWorkerState) error
type xrayWorkerObserve func(context.Context, xrayWorkerState) (xrayWorkerState, error)

func (s *Store) startXrayWorkerAPI(state xrayWorkerState, apply xrayWorkerApply, observe xrayWorkerObserve) error {
	s.xrayWorkerMu.Lock()
	defer s.xrayWorkerMu.Unlock()
	target := net.JoinHostPort(state.Address, strconv.Itoa(state.PanelPort))
	if s.xrayWorkerServer != nil {
		if s.xrayWorkerListener != nil && s.xrayWorkerListener.Addr().String() == target {
			if s.xrayWorkerDone != nil {
				select {
				case <-s.xrayWorkerDone:
					reconcileContext, cancel := context.WithCancel(context.Background())
					done := make(chan struct{})
					s.xrayWorkerCancel, s.xrayWorkerDone = cancel, done
					go s.runXrayWorkerReconciler(reconcileContext, apply, observe, done)
				default:
				}
			}
			return nil
		}
		if s.xrayWorkerCancel != nil {
			s.xrayWorkerCancel()
			s.xrayWorkerCancel = nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.xrayWorkerServer.Shutdown(ctx)
		if s.xrayWorkerDone != nil {
			select {
			case <-s.xrayWorkerDone:
			case <-ctx.Done():
				err = errors.Join(err, ctx.Err())
			}
			s.xrayWorkerDone = nil
		}
		cancel()
		if err != nil {
			return err
		}
		s.xrayWorkerServer, s.xrayWorkerListener = nil, nil
	}
	listener, err := net.Listen("tcp", target)
	if err != nil {
		return fmt.Errorf("agent: bind Xray worker management API: %w", err)
	}
	server := &http.Server{Handler: s.xrayWorkerHandler(apply, observe), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	s.xrayWorkerListener, s.xrayWorkerServer = listener, server
	go func() { _ = server.Serve(listener) }()
	reconcileContext, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.xrayWorkerCancel, s.xrayWorkerDone = cancel, done
	go s.runXrayWorkerReconciler(reconcileContext, apply, observe, done)
	return nil
}

func (s *Store) stopXrayWorkerAPI(ctx context.Context, deleteData bool) error {
	s.xrayWorkerMu.Lock()
	defer s.xrayWorkerMu.Unlock()
	var stopErr error
	if s.xrayWorkerCancel != nil {
		s.xrayWorkerCancel()
		s.xrayWorkerCancel = nil
	}
	if s.xrayWorkerServer != nil {
		stopErr = s.xrayWorkerServer.Shutdown(ctx)
		s.xrayWorkerServer, s.xrayWorkerListener = nil, nil
	}
	if s.xrayWorkerDone != nil {
		select {
		case <-s.xrayWorkerDone:
		case <-ctx.Done():
			stopErr = errors.Join(stopErr, ctx.Err())
		}
		s.xrayWorkerDone = nil
	}
	if deleteData && stopErr == nil {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM xray_worker_state WHERE id=1`); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(s.dataDir, "xray-worker")); err != nil {
			return err
		}
	}
	return stopErr
}

func (s *Store) runXrayWorkerReconciler(ctx context.Context, apply xrayWorkerApply, observe xrayWorkerObserve, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.reconcileXrayWorkerRuntime(ctx, apply, observe) != nil {
				// Fail closed on the first control-plane error. The persisted
				// desired/applied revisions remain available to explicit recovery.
				return
			}
		}
	}
}

func (s *Store) reconcileXrayWorkerRuntime(ctx context.Context, apply xrayWorkerApply, observe xrayWorkerObserve) error {
	s.xrayWorkerStateMu.Lock()
	defer s.xrayWorkerStateMu.Unlock()
	previous, err := s.loadXrayWorkerState(ctx)
	if err != nil {
		return err
	}
	if previous.AppliedRevision != previous.Revision {
		return errors.New("Xray worker revision requires explicit recovery")
	}
	candidate := previous
	if observe != nil {
		candidate, err = observe(ctx, candidate)
		if err != nil {
			return err
		}
	}
	candidate = reconcileXrayWorkerAccounts(candidate, s.now().UnixMilli())
	changed, err := xrayWorkerConfigChanged(previous, candidate)
	if err != nil {
		return err
	}
	if !changed {
		if xrayWorkerStateEqual(previous, candidate) {
			return nil
		}
		return s.saveXrayWorkerState(ctx, candidate)
	}
	if apply == nil {
		return errors.New("worker runtime unavailable")
	}
	candidate.Revision = previous.Revision + 1
	candidate.AppliedRevision = previous.AppliedRevision
	if err := s.saveXrayWorkerState(ctx, candidate); err != nil {
		return err
	}
	if err := apply(ctx, previous, candidate); err != nil {
		return err
	}
	candidate.AppliedRevision = candidate.Revision
	return s.saveXrayWorkerState(ctx, candidate)
}

func (s *Store) retireXrayWorkerState(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM xray_worker_state WHERE id=1`); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.dataDir, "xray-worker"))
}

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
		state, err := s.loadXrayWorkerState(request.Context())
		expectedAuthorization := "Bearer " + state.APIToken
		if err != nil || subtle.ConstantTimeCompare([]byte(request.Header.Get("Authorization")), []byte(expectedAuthorization)) != 1 {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		original := state
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
			observationErr = errors.New("Xray worker revision requires explicit recovery")
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

func xrayWorkerEndpointAllowed(method, path string) bool {
	if method == http.MethodGet {
		switch path {
		case "/panel/api/server/status", "/panel/api/inbounds/list", "/panel/api/hosts/list", "/panel/api/server/getWebCertFiles", "/panel/api/server/descendants", "/panel/api/server/clientIps":
			return true
		}
		return strings.HasPrefix(path, "/panel/api/inbounds/get/")
	}
	if method != http.MethodPost {
		return false
	}
	switch path {
	case "/panel/api/server/restartXrayService", "/panel/api/xray", "/panel/api/xray/update",
		"/panel/api/inbounds/add", "/panel/api/inbounds/resetAllTraffics", "/panel/api/inbounds/pushClientTraffics",
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
			return nil, nil, errors.New("Xray worker revision is not applied")
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
	if request.Method == http.MethodGet && (path == "/panel/api/hosts/list" || path == "/panel/api/server/descendants" || path == "/panel/api/server/clientIps") {
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
		return nil, nil, errors.New("Xray worker traffic controller changed")
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
