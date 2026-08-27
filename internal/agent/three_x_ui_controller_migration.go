package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const threeXUIControllerBackupMaxSize = 64 << 20

func (c Client) applyThreeXUIControllerCommand(ctx context.Context, store *Store, command ThreeXUIControllerCommandTask) (ThreeXUIControllerCommandResult, error) {
	if command.ApplicationID == "" || (command.Action != "backup" && command.Action != "promote" && command.Action != "demote") {
		return ThreeXUIControllerCommandResult{}, errors.New("agent: invalid 3x-ui controller operation")
	}
	baseURL, currentToken, err := threeXUIClientAPIConnection(ctx, store)
	if err != nil {
		return ThreeXUIControllerCommandResult{}, err
	}
	switch command.Action {
	case "backup":
		content, err := downloadThreeXUIDatabase(ctx, baseURL, currentToken)
		if err != nil {
			return ThreeXUIControllerCommandResult{}, err
		}
		if err := c.uploadThreeXUIBackup(ctx, store, command.ApplicationID, command.BackupRevision, content); err != nil {
			return ThreeXUIControllerCommandResult{}, err
		}
		digest := sha256.Sum256(content)
		return ThreeXUIControllerCommandResult{Action: "backup", BackupRevision: command.BackupRevision, BackupSHA256: hex.EncodeToString(digest[:]), BackupSize: int64(len(content))}, nil
	case "promote":
		return c.promoteThreeXUIController(ctx, store, baseURL, currentToken, command)
	default:
		if err := demoteThreeXUIController(ctx, store, baseURL, command.SourceAPIToken); err != nil {
			return ThreeXUIControllerCommandResult{}, err
		}
		return ThreeXUIControllerCommandResult{Action: "demote"}, nil
	}
}

func downloadThreeXUIDatabase(ctx context.Context, baseURL, token string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/panel/api/server/getDb", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 90 * time.Second}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("agent: download 3x-ui restore point: %w", err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, threeXUIControllerBackupMaxSize+1))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || len(content) < 16 || len(content) > threeXUIControllerBackupMaxSize || string(content[:16]) != "SQLite format 3\x00" {
		return nil, errors.New("agent: 3x-ui returned an invalid SQLite restore point")
	}
	return content, nil
}

func (c Client) uploadThreeXUIBackup(ctx context.Context, store *Store, applicationID string, revision int64, content []byte) error {
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	endpoint := connection.CenterURL + "/api/v1/agents/" + url.PathEscape(connection.AgentID) + "/three-x-ui-backups/" + url.PathEscape(applicationID) + "/" + strconv.FormatInt(revision, 10)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(content))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+connection.Credential)
	request.Header.Set("Content-Type", "application/octet-stream")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("agent: upload 3x-ui restore point: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		content, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("agent: Center rejected 3x-ui restore point: %s", centerFailureMessage(response.Status, content))
	}
	return nil
}

func (c Client) downloadThreeXUIMigrationBackup(ctx context.Context, store *Store, migrationID string) ([]byte, error) {
	connection, err := store.Connection(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := connection.CenterURL + "/api/v1/agents/" + url.PathEscape(connection.AgentID) + "/three-x-ui-migrations/" + url.PathEscape(migrationID) + "/backup"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+connection.Credential)
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("agent: download 3x-ui migration restore point: %w", err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, threeXUIControllerBackupMaxSize+1))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("agent: Center rejected 3x-ui migration restore: %s", centerFailureMessage(response.Status, content))
	}
	if len(content) < 16 || len(content) > threeXUIControllerBackupMaxSize || string(content[:16]) != "SQLite format 3\x00" {
		return nil, errors.New("agent: Center returned an invalid 3x-ui migration restore point")
	}
	return content, nil
}

func centerFailureMessage(status string, content []byte) string {
	var failure struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(content, &failure) == nil && strings.TrimSpace(failure.Error) != "" {
		return failure.Error
	}
	return status
}

func (c Client) promoteThreeXUIController(ctx context.Context, store *Store, baseURL, currentToken string, command ThreeXUIControllerCommandTask) (ThreeXUIControllerCommandResult, error) {
	if command.MigrationID == "" || command.SourceApplicationID == "" || command.BackupRevision < 1 || command.SourceRemoteNodeID < 1 || command.SourceAPIToken == "" || command.SourceAddress == "" || command.SourcePanelPort < 1 {
		return ThreeXUIControllerCommandResult{}, errors.New("agent: incomplete 3x-ui controller migration")
	}
	original, err := downloadThreeXUIDatabase(ctx, baseURL, currentToken)
	if err != nil {
		return ThreeXUIControllerCommandResult{}, fmt.Errorf("agent: create target rollback point: %w", err)
	}
	installation, err := store.AppliedInstallation(ctx, threeXUIKey)
	if err != nil {
		return ThreeXUIControllerCommandResult{}, err
	}
	config, err := decodeThreeXUIConfig(installation.Config)
	if err != nil {
		return ThreeXUIControllerCommandResult{}, err
	}
	panelGUID, err := threeXUISettingFromDatabase(original, "panelGuid")
	if err != nil {
		return ThreeXUIControllerCommandResult{}, fmt.Errorf("agent: preserve target 3x-ui identity: %w", err)
	}
	if panelGUID == "" {
		panelGUID, err = randomUUID()
		if err != nil {
			return ThreeXUIControllerCommandResult{}, err
		}
	}
	restore, err := c.downloadThreeXUIMigrationBackup(ctx, store, command.MigrationID)
	if err != nil {
		return ThreeXUIControllerCommandResult{}, err
	}
	transformed, err := transformThreeXUIControllerDatabase(restore, command, threeXUIControllerTargetSettings{
		Address:   installation.ServiceAddress,
		PanelPort: config.PanelPort,
		PanelGUID: panelGUID,
	})
	if err != nil {
		return ThreeXUIControllerCommandResult{}, err
	}
	if err := importThreeXUIDatabase(ctx, baseURL, currentToken, transformed); err != nil {
		return ThreeXUIControllerCommandResult{}, fmt.Errorf("agent: restore 3x-ui controller database: %w", err)
	}
	rollback := func(operationErr error) (ThreeXUIControllerCommandResult, error) {
		if rollbackErr := rollbackThreeXUIControllerDatabase(baseURL, command.SourceAPIToken, currentToken, original); rollbackErr != nil {
			return ThreeXUIControllerCommandResult{}, errors.Join(operationErr, fmt.Errorf("agent: rollback promoted 3x-ui controller: %w", rollbackErr))
		}
		return ThreeXUIControllerCommandResult{}, operationErr
	}
	if err := waitForThreeXUIAPI(ctx, baseURL, command.SourceAPIToken); err != nil {
		return rollback(fmt.Errorf("agent: validate restored 3x-ui controller: %w", err))
	}
	parsedBase, parseErr := url.Parse(baseURL)
	if parseErr != nil || parsedBase.Hostname() == "" || mustPort(baseURL) < 1 {
		return rollback(errors.New("agent: promoted 3x-ui endpoint is invalid"))
	}
	if err := configureThreeXUISubscriptionRole(ctx, parsedBase.Hostname(), mustPort(baseURL), command.SourceAPIToken, "master"); err != nil {
		return rollback(fmt.Errorf("agent: enable subscription on promoted 3x-ui controller: %w", err))
	}
	var secrets map[string]string
	if json.Unmarshal(installation.Secrets, &secrets) != nil {
		return rollback(errors.New("agent: stored 3x-ui secrets are invalid"))
	}
	secrets["api_token"] = command.SourceAPIToken
	installation.Secrets, _ = json.Marshal(secrets)
	if _, err := store.RecordApplied(ctx, installation); err != nil {
		return rollback(fmt.Errorf("agent: save promoted 3x-ui controller token: %w", err))
	}
	return ThreeXUIControllerCommandResult{Action: "promote", BackupRevision: command.BackupRevision, SourceRemoteNodeID: command.SourceRemoteNodeID}, nil
}

func rollbackThreeXUIControllerDatabase(baseURL, restoredToken, originalToken string, content []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	seen := map[string]bool{}
	var failures []error
	for _, token := range []string{restoredToken, originalToken} {
		token = strings.TrimSpace(token)
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		if err := importThreeXUIDatabase(ctx, baseURL, token, content); err == nil {
			return nil
		} else {
			failures = append(failures, err)
		}
	}
	if len(failures) == 0 {
		return errors.New("no 3x-ui API token was available for rollback")
	}
	return errors.Join(failures...)
}

type threeXUIControllerTargetSettings struct {
	Address   string
	PanelPort int
	PanelGUID string
}

func mustPort(baseURL string) int {
	parsed, _ := url.Parse(baseURL)
	port, _ := strconv.Atoi(parsed.Port())
	return port
}

func transformThreeXUIControllerDatabase(content []byte, command ThreeXUIControllerCommandTask, target threeXUIControllerTargetSettings) ([]byte, error) {
	if ip := net.ParseIP(target.Address); ip == nil || ip.To4() == nil || target.PanelPort < 1024 || target.PanelPort > 65535 || strings.TrimSpace(target.PanelGUID) == "" {
		return nil, errors.New("agent: invalid target 3x-ui controller settings")
	}
	file, err := os.CreateTemp("", "vastora-3x-ui-migration-*.db")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(content); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var nodeCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = ?`, command.SourceRemoteNodeID).Scan(&nodeCount); err != nil || nodeCount != 1 {
		return nil, errors.New("agent: target VLESS node is missing from the controller restore point")
	}
	sentinel := -command.SourceRemoteNodeID
	if _, err := tx.Exec(`UPDATE inbounds SET node_id = ? WHERE node_id = ?`, sentinel, command.SourceRemoteNodeID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE inbounds SET node_id = ? WHERE node_id IS NULL`, command.SourceRemoteNodeID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE inbounds SET node_id = NULL WHERE node_id = ?`, sentinel); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE nodes SET name = ?, remark = ?, scheme = 'http', address = ?, port = ?, base_path = '/', api_token = ?, enable = 1, allow_private_address = 1, tls_verify_mode = 'verify', pinned_cert_sha256 = '', inbound_sync_mode = 'all', inbound_tags = '[]', outbound_tag = '', guid = '', status = 'unknown', last_heartbeat = 0, latency_ms = 0, xray_version = '', panel_version = '', cpu_pct = 0, mem_pct = 0, uptime_secs = 0, net_up = 0, net_down = 0, last_error = '', xray_state = '', xray_error = '', config_dirty = 1, config_dirty_at = 0, inbounds_adopted_at = 0 WHERE id = ?`,
		threeXUINodeAPIName(command.SourceApplicationID), strings.TrimSpace(command.SourceName), command.SourceAddress, command.SourcePanelPort, command.SourceAPIToken, command.SourceRemoteNodeID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM node_client_traffics WHERE node_id = ?`, command.SourceRemoteNodeID); err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such table") {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM node_client_ips WHERE node_id = ?`, command.SourceRemoteNodeID); err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such table") {
		return nil, err
	}
	settings := map[string]string{
		"webListen": target.Address,
		"webPort":   strconv.Itoa(target.PanelPort),
		"subListen": target.Address,
		"subPort":   "2096",
		"panelGuid": target.PanelGUID,
	}
	for key, value := range settings {
		if err := upsertThreeXUISetting(tx, key, value); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func upsertThreeXUISetting(tx *sql.Tx, key, value string) error {
	result, err := tx.Exec(`UPDATE settings SET value = ? WHERE key = ?`, value, key)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		_, err = tx.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)`, key, value)
	}
	return err
}

func threeXUISettingFromDatabase(content []byte, key string) (string, error) {
	file, err := os.CreateTemp("", "vastora-3x-ui-setting-*.db")
	if err != nil {
		return "", err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.Write(content); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var value string
	err = db.QueryRow(`SELECT value FROM settings WHERE key = ? ORDER BY id LIMIT 1`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return strings.TrimSpace(value), err
}

func importThreeXUIDatabase(ctx context.Context, baseURL, token string, content []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("db", "vastora-controller.db")
	if err != nil {
		return err
	}
	if _, err := part.Write(content); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/panel/api/server/importDB", &body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := (&http.Client{Timeout: 2 * time.Minute}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var envelope struct {
		Success bool   `json:"success"`
		Message string `json:"msg"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope) != nil || response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
		return fmt.Errorf("3x-ui rejected database import: %s", strings.TrimSpace(envelope.Message))
	}
	return nil
}

func waitForThreeXUIAPI(ctx context.Context, baseURL, token string) error {
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/server/status", token, "", nil); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("3x-ui API did not return after database restore")
		case <-ticker.C:
		}
	}
}

func demoteThreeXUIController(ctx context.Context, store *Store, baseURL, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("agent: previous 3x-ui controller token is unavailable")
	}
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/list", token, "", nil)
	if err != nil {
		return err
	}
	var inbounds []struct {
		ID     int  `json:"id"`
		NodeID *int `json:"nodeId"`
	}
	if json.Unmarshal(payload, &inbounds) != nil {
		return errors.New("agent: 3x-ui returned invalid inbound data during demotion")
	}
	for _, inbound := range inbounds {
		if inbound.NodeID != nil {
			if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/del/"+strconv.Itoa(inbound.ID), token, "application/json", map[string]any{}); err != nil {
				return err
			}
		}
	}
	nodes, err := listThreeXUINodes(ctx, baseURL, token)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/nodes/del/"+strconv.Itoa(node.ID), token, "application/json", map[string]any{}); err != nil {
			return err
		}
	}
	installation, err := store.AppliedInstallation(ctx, threeXUIKey)
	if err != nil {
		return err
	}
	config, err := decodeThreeXUIConfig(installation.Config)
	if err != nil {
		return err
	}
	return configureThreeXUISubscriptionRole(ctx, installation.ServiceAddress, config.PanelPort, token, "worker")
}
