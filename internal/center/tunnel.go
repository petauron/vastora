package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

const cloudflaredImage = "docker.io/cloudflare/cloudflared:2026.7.2@sha256:4f6655284ab3d252b7f28fedb19fe6c8fc82ee5b1295c20ac74d475e5398a52d"

func (s *Store) queueTunnelState(ctx context.Context, tx *sql.Tx, agentID string, now time.Time) error {
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision FROM cloudflare_tunnels WHERE agent_id = ?`, agentID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: configure Cloudflare before creating a Tunnel publication")
	} else if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.hostname, s.protocol, s.endpoint
		FROM publications p JOIN services s ON s.id = p.service_id
		WHERE p.gateway_node_id = ? AND p.kind = 'cloudflare_tunnel' AND p.status <> 'stopped' AND s.status <> 'stopped'
		ORDER BY p.hostname, p.id`, agentID)
	if err != nil {
		return err
	}
	ingress := []TunnelTaskIngress{}
	for rows.Next() {
		var hostname, protocol, endpoint string
		if err := rows.Scan(&hostname, &protocol, &endpoint); err != nil {
			rows.Close()
			return err
		}
		if protocol != "http" && protocol != "https" {
			rows.Close()
			return errors.New("center: Cloudflare Tunnel only supports Web services")
		}
		host, portValue, err := net.SplitHostPort(endpoint)
		if err != nil || net.ParseIP(host) == nil {
			rows.Close()
			return errors.New("center: stored Tunnel upstream is invalid")
		}
		port, _ := strconv.Atoi(portValue)
		serviceURL := (&url.URL{Scheme: protocol, Host: net.JoinHostPort(host, strconv.Itoa(port))}).String()
		ingress = append(ingress, TunnelTaskIngress{Hostname: hostname, Service: serviceURL})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	sort.Slice(ingress, func(i, j int) bool { return ingress[i].Hostname < ingress[j].Hostname })
	status := "running"
	if len(ingress) == 0 {
		status = "stopped"
	}
	revision := current + 1
	state := TunnelTaskState{Revision: revision, Status: status, Image: cloudflaredImage, Ingress: ingress}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cloudflare_tunnels SET desired_revision = ?, desired_json = ?, status = 'pending', attempt = 0, lease_expires_at = '', last_error = '', updated_at = ? WHERE agent_id = ?`, revision, payload, now.Format(time.RFC3339Nano), agentID); err != nil {
		return fmt.Errorf("center: queue Tunnel state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET desired_revision = ?, status = 'pending', last_error = '', updated_at = ? WHERE gateway_node_id = ? AND kind = 'cloudflare_tunnel' AND status <> 'stopped'`, revision, now.Format(time.RFC3339Nano), agentID); err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, tunnelTaskID(agentID, revision), agentID, "tunnel.state.apply", revision, "queued", "Cloudflare Tunnel desired state queued")
}

func (s *Store) claimTunnelTask(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var revision, attempt int64
	var desiredJSON []byte
	var tokenSecretID string
	err := tx.QueryRowContext(ctx, `SELECT desired_revision, desired_json, token_secret_id, attempt FROM cloudflare_tunnels
		WHERE agent_id = ? AND desired_revision > applied_revision AND status IN ('pending', 'failed')`, agentID).Scan(&revision, &desiredJSON, &tokenSecretID, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state TunnelTaskState
	if json.Unmarshal(desiredJSON, &state) != nil || state.Revision != revision || (state.Status != "running" && state.Status != "stopped") || strings.TrimSpace(state.Image) == "" {
		return nil, errors.New("center: invalid stored Tunnel desired state")
	}
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id = ?`, tokenSecretID).Scan(&sealed); err != nil {
		return nil, fmt.Errorf("center: read Tunnel token: %w", err)
	}
	token, err := secret.Open(s.key, sealed, []byte("cloudflare-tunnel:"+agentID))
	if err != nil {
		return nil, fmt.Errorf("center: decrypt Tunnel token: %w", err)
	}
	state.Token = string(token)
	now := s.now().UTC()
	claimed, err := tx.ExecContext(ctx, `UPDATE cloudflare_tunnels SET status = 'applying', attempt = attempt + 1, lease_expires_at = ?, updated_at = ?
		WHERE agent_id = ? AND desired_revision = ? AND attempt = ? AND status IN ('pending', 'failed')`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), agentID, revision, attempt)
	if err != nil {
		return nil, err
	}
	if changed, _ := claimed.RowsAffected(); changed != 1 {
		return nil, errors.New("center: Tunnel desired state changed while claiming")
	}
	task := &AgentTask{Kind: "tunnel.state.apply", ID: tunnelTaskID(agentID, revision), Attempt: attempt + 1, Revision: revision, TunnelState: &state}
	if err := s.recordTaskEvent(ctx, tx, task.ID, agentID, task.Kind, revision, "claimed", fmt.Sprintf("attempt %d", task.Attempt)); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) completeTunnelState(ctx context.Context, agentID string, revision, expectedAttempt int64, succeeded bool, taskError string) error {
	taskError = strings.TrimSpace(taskError)
	if len(taskError) > 1024 {
		taskError = taskError[:1024]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var desired, applied, attempt int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, attempt FROM cloudflare_tunnels WHERE agent_id = ?`, agentID).Scan(&desired, &applied, &status, &attempt); err != nil {
		return errors.New("center: Tunnel desired state not found")
	}
	if revision <= applied {
		return nil
	}
	if revision != desired || status != "applying" || expectedAttempt != attempt {
		return errors.New("center: stale Tunnel result")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	nextStatus, event := "ready", "succeeded"
	if !succeeded {
		nextStatus, event = "failed", "failed"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cloudflare_tunnels SET applied_revision = CASE WHEN ? THEN ? ELSE applied_revision END, status = ?, lease_expires_at = '', last_error = ?, updated_at = ? WHERE agent_id = ?`, succeeded, revision, nextStatus, taskError, now, agentID); err != nil {
		return err
	}
	publicationStatus := "ready"
	verificationTargets := []publicationVerificationTarget{}
	if succeeded {
		publicationStatus = "applying"
	}
	if !succeeded {
		publicationStatus = "failed"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET applied_revision = CASE WHEN ? THEN desired_revision ELSE applied_revision END, status = ?, last_error = ?, updated_at = ? WHERE gateway_node_id = ? AND kind = 'cloudflare_tunnel' AND desired_revision <= ? AND status <> 'stopped'`, succeeded, publicationStatus, taskError, now, agentID, revision); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, tunnelTaskID(agentID, revision), agentID, "tunnel.state.apply", revision, event, taskError); err != nil {
		return err
	}
	if succeeded {
		rows, err := tx.QueryContext(ctx, `SELECT id, desired_revision FROM publications
			WHERE gateway_node_id = ? AND kind = 'cloudflare_tunnel' AND desired_revision <= ? AND status <> 'stopped'`, agentID, revision)
		if err != nil {
			return err
		}
		for rows.Next() {
			var target publicationVerificationTarget
			if err := rows.Scan(&target.id, &target.revision); err != nil {
				rows.Close()
				return err
			}
			verificationTargets = append(verificationTargets, target)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, target := range verificationTargets {
		s.schedulePublicationVerification(target.id, target.revision)
	}
	return nil
}

func tunnelTaskID(agentID string, revision int64) string {
	return fmt.Sprintf("tunnel-%s-r%d", agentID, revision)
}

func tunnelTaskRevision(taskID string) (int64, bool) {
	marker := strings.LastIndex(taskID, "-r")
	if !strings.HasPrefix(taskID, "tunnel-") || marker <= len("tunnel-") {
		return 0, false
	}
	revision, err := strconv.ParseInt(taskID[marker+2:], 10, 64)
	return revision, err == nil && revision > 0
}
