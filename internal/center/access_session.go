package center

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const cloudflareAccessSettingsSchema = `CREATE TABLE cloudflare_access_settings (
	id INTEGER PRIMARY KEY CHECK(id = 1),
	session_duration TEXT NOT NULL DEFAULT '24h' CHECK(session_duration IN ('15m','30m','1h','6h','12h','24h','48h','72h','168h','720h')),
	sync_json TEXT NOT NULL DEFAULT '{"status":"not_synced","total":0,"updated":0}' CHECK(json_valid(sync_json))
)`

// A deliberately bounded subset of Cloudflare's application-session durations.
// This setting never changes organization, policy or Cloudflare One Client sessions.
func validAccessSessionDuration(value string) bool {
	switch value {
	case "15m", "30m", "1h", "6h", "12h", "24h", "48h", "72h", "168h", "720h":
		return true
	}
	return false
}

func validateAccessSessionInput(input CenterRemoteAccessInput) error {
	duration := strings.TrimSpace(input.AccessSessionDuration)
	if duration != "" && (!validAccessSessionDuration(duration) || !input.Enabled || strings.TrimSpace(input.ProtectionMode) == "native") {
		return errors.New("center: select a supported session duration in Cloudflare Access mode")
	}
	return nil
}

type AccessSessionSync struct {
	Status              string   `json:"status"`
	Total               int      `json:"total"`
	Updated             int      `json:"updated"`
	FailedHosts         []string `json:"failedHosts,omitempty"`
	PolicyOverrideHosts []string `json:"policyOverrideHosts,omitempty"`
}

func (s *Store) accessSessionSettings(ctx context.Context) (string, AccessSessionSync, error) {
	var duration, encoded string
	var sync AccessSessionSync
	if err := s.db.QueryRowContext(ctx, `SELECT session_duration, sync_json FROM cloudflare_access_settings WHERE id = 1`).Scan(&duration, &encoded); err != nil {
		return "", sync, err
	}
	if !validAccessSessionDuration(duration) {
		return "", sync, errors.New("center: invalid stored Access session duration")
	}
	err := json.Unmarshal([]byte(encoded), &sync)
	return duration, sync, err
}

func (s *Store) saveAccessSessionSettings(ctx context.Context, duration string, sync AccessSessionSync) error {
	if !validAccessSessionDuration(duration) {
		return errors.New("center: select a supported Access session duration")
	}
	encoded, err := json.Marshal(sync)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE cloudflare_access_settings SET session_duration = ?, sync_json = ? WHERE id = 1`, duration, string(encoded))
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return errors.New("center: Access session settings are missing")
	}
	return nil
}

// The caller holds remoteAccessMu, also used by service Access creation. Persist
// desired state first; interrupted/partial sync remains visible and retryable.
// newlyCreatedID already received this duration and has no policy overrides.
func (s *Store) syncAccessSessions(ctx context.Context, client cloudflareClient, duration, newlyCreatedID string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT access_application_id, hostname FROM center_remote_access
		WHERE protection_mode = 'access' AND access_application_id <> ''
		UNION SELECT access_application_id, hostname FROM publications WHERE access_application_id <> '' AND status <> 'stopped'
		ORDER BY 1`)
	if err != nil {
		return err
	}
	type target struct{ id, hostname string }
	var targets []target
	for rows.Next() {
		var app target
		if err := rows.Scan(&app.id, &app.hostname); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, app)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	result := AccessSessionSync{Status: "pending", Total: len(targets)}
	if err := s.saveAccessSessionSettings(ctx, duration, result); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for _, app := range targets {
		var overridden bool
		var updateErr error
		if app.id != newlyCreatedID {
			attempt, done := context.WithTimeout(ctx, 8*time.Second)
			overridden, updateErr = client.updateAccessSession(attempt, app.id, app.hostname, duration)
			done()
		}
		if overridden {
			result.PolicyOverrideHosts = append(result.PolicyOverrideHosts, app.hostname)
		}
		if updateErr != nil {
			result.FailedHosts = append(result.FailedHosts, app.hostname)
			slog.WarnContext(ctx, "Access session synchronization failed", "hostname", app.hostname, "error", updateErr)
		} else {
			result.Updated++
		}
	}
	result.Status = "synced"
	if len(result.FailedHosts) != 0 {
		result.Status = "partial"
		if result.Updated == 0 {
			result.Status = "failed"
		}
	}
	// Retain the diagnostic outcome even when the browser disconnects or the
	// remote request times out. Never change the working entry's own status.
	saveCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	return s.saveAccessSessionSettings(saveCtx, duration, result)
}

func (client cloudflareClient) updateAccessSession(ctx context.Context, id, hostname, duration string) (bool, error) {
	if !validAccessSessionDuration(duration) {
		return false, errors.New("center: select a supported Access session duration")
	}
	path := "/accounts/" + url.PathEscape(client.accountID) + "/access/apps/" + url.PathEscape(id)
	var app map[string]json.RawMessage
	if err := client.do(ctx, http.MethodGet, path, nil, &app); err != nil {
		return false, err
	}
	readString := func(key string) string {
		var value string
		_ = json.Unmarshal(app[key], &value)
		return value
	}
	if readString("id") != id || readString("type") != "self_hosted" || readString("domain") != hostname {
		return false, errors.New("center: managed Access application identity does not match")
	}
	// Read all attached policies, including reusable ones. Never rewrite their
	// durations: an explicit override must remain visible to the administrator.
	overridden := false
	for page := 1; ; page++ {
		var policies []struct {
			SessionDuration string `json:"session_duration"`
		}
		if err := client.do(ctx, http.MethodGet, fmt.Sprintf("%s/policies?per_page=100&page=%d", path, page), nil, &policies); err != nil {
			return overridden, err
		}
		for _, policy := range policies {
			if strings.TrimSpace(policy.SessionDuration) != "" {
				overridden = true
			}
		}
		if len(policies) < 100 {
			break
		}
	}
	if readString("session_duration") == duration {
		return overridden, nil
	}
	// Round-trip the application configuration, excluding response-only fields.
	// Policy attachments are preserved by ID, not recreated or edited inline.
	var policies []struct {
		ID         string `json:"id"`
		AccountID  string `json:"account_id,omitempty"`
		Precedence int    `json:"precedence"`
	}
	if err := json.Unmarshal(app["policies"], &policies); err != nil || len(policies) == 0 {
		return overridden, errors.New("center: cannot preserve Access policy attachments")
	}
	for _, policy := range policies {
		if policy.ID == "" {
			return overridden, errors.New("center: Access policy attachment has no ID")
		}
	}
	app["policies"], _ = json.Marshal(policies)
	for _, field := range []string{"id", "aud", "created_at", "updated_at"} {
		delete(app, field)
	}
	app["session_duration"], _ = json.Marshal(duration)
	var updated struct {
		ID              string `json:"id"`
		SessionDuration string `json:"session_duration"`
	}
	if err := client.do(ctx, http.MethodPut, path, app, &updated); err != nil {
		return overridden, err
	}
	if updated.ID != id || updated.SessionDuration != duration {
		return overridden, errors.New("center: Cloudflare did not confirm the Access session duration")
	}
	return overridden, nil
}
