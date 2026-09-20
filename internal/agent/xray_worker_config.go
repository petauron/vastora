package agent

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

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
		// HAProxy owns public TCP/443 and reaches REALITY only through the
		// shared Docker bridge. HY2 uses an explicit host UDP/443 publication.
		if protocol == "vless" || protocol == "hysteria" {
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

func (s *Store) reconcileXrayWorkerAppliedReceipt(state xrayWorkerState) error {
	if s.xrayWorkerAppliedReceiptMatches(state) {
		return nil
	}
	desired, err := renderXrayWorkerConfig(state)
	if err != nil {
		return err
	}
	active, err := os.ReadFile(filepath.Join(s.dataDir, "xray-worker", "config.json"))
	if err != nil || len(active) > xrayWorkerMaxBody || subtle.ConstantTimeCompare(desired, active) != 1 {
		return errors.New("agent: recreated Xray worker configuration requires explicit reconciliation")
	}
	return s.recordXrayWorkerApplied(state)
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
