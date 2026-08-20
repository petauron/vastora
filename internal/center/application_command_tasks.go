package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

func (s *Store) claimApplicationCommand(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var id string
	var inputJSON []byte
	var attempt int64
	err := tx.QueryRowContext(ctx, `SELECT id, input_json, attempt FROM application_commands WHERE agent_id = ? AND state = 'pending' ORDER BY created_at, rowid LIMIT 1`, agentID).Scan(&id, &inputJSON, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("center: read pending application operation: %w", err)
	}
	var command RealityCommandTask
	if json.Unmarshal(inputJSON, &command) != nil || command.Name == "" || command.ConnectHostname == "" {
		return nil, errors.New("center: stored application operation is invalid")
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'running', attempt = attempt + 1, lease_expires_at = ?, error = '', updated_at = ? WHERE id = ? AND state = 'pending' AND attempt = ?`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, attempt)
	if err != nil {
		return nil, fmt.Errorf("center: claim application operation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, errors.New("center: application operation changed while claiming")
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, "application.command", 1, "claimed", fmt.Sprintf("attempt %d", attempt+1)); err != nil {
		return nil, err
	}
	return &AgentTask{Kind: "application.command", ID: id, Attempt: attempt + 1, Revision: 1, ApplicationCommand: &command}, nil
}

func (s *Store) completeApplicationCommand(ctx context.Context, agentID, taskID string, expectedAttempt int64, succeeded bool, taskError string, rawResult json.RawMessage) error {
	taskError = strings.TrimSpace(taskError)
	if len(taskError) > 1024 {
		taskError = taskError[:1024]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var applicationID, gatewayID, kind, currentState string
	var inputJSON []byte
	var attempt int64
	if err := tx.QueryRowContext(ctx, `SELECT application_id, gateway_node_id, kind, input_json, state, attempt FROM application_commands WHERE id = ? AND agent_id = ?`, taskID, agentID).Scan(&applicationID, &gatewayID, &kind, &inputJSON, &currentState, &attempt); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: application operation not found")
	} else if err != nil {
		return err
	}
	if currentState == "succeeded" || currentState == "failed" {
		return nil
	}
	if kind != realityCommandKind || currentState != "running" || expectedAttempt <= 0 || expectedAttempt != attempt {
		return errors.New("center: stale application operation result")
	}
	now := s.now().UTC()
	var serviceID string
	var input RealityCommandTask
	var envelope ApplicationTaskResult
	if json.Unmarshal(inputJSON, &input) != nil {
		return errors.New("center: stored application operation is invalid")
	}
	if succeeded {
		if len(rawResult) == 0 || json.Unmarshal(rawResult, &envelope) != nil || envelope.ApplicationCommand == nil {
			succeeded = false
			taskError = "center: Agent returned an invalid REALITY result"
		}
	}
	if succeeded {
		result := *envelope.ApplicationCommand
		if err := validateRealityCommandResult(input, result); err != nil {
			succeeded = false
			taskError = err.Error()
		} else {
			var serviceAddress string
			if err := tx.QueryRowContext(ctx, `SELECT service_address FROM agent_network_profiles WHERE agent_id = ?`, agentID).Scan(&serviceAddress); err != nil || result.Listen != serviceAddress {
				succeeded = false
				taskError = "center: REALITY inbound is not bound to the confirmed private service address"
			}
		}
		if succeeded {
			for _, excluded := range input.ExcludedSNI {
				if result.SNIHostname == excluded {
					succeeded = false
					taskError = "center: selected REALITY SNI is already used on this gateway"
					break
				}
			}
		}
		if succeeded {
			result := *envelope.ApplicationCommand
			serviceName := fmt.Sprintf("inbound-%d", result.InboundID)
			err := tx.QueryRowContext(ctx, `SELECT id FROM services WHERE application_id = ? AND name = ?`, applicationID, serviceName).Scan(&serviceID)
			if errors.Is(err, sql.ErrNoRows) {
				serviceID, err = randomToken(18)
				if err == nil {
					var siteID string
					err = tx.QueryRowContext(ctx, `SELECT site_id FROM applications WHERE id = ?`, applicationID).Scan(&siteID)
					if err == nil {
						_, err = tx.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, management, observed_listen, status, created_at, updated_at) VALUES(?, ?, ?, ?, 'tcp', ?, ?, ?, 'observed', 'vless/tcp/reality', 0, ?, 'ready', ?, ?)`, serviceID, applicationID, siteID, serviceName, result.Port, result.Port, net.JoinHostPort(result.Listen, fmt.Sprint(result.Port)), result.Listen, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
					}
				}
			} else if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE services SET protocol = 'tcp', container_port = ?, host_port = ?, endpoint = ?, source = 'observed', app_protocol = 'vless/tcp/reality', observed_listen = ?, status = 'ready', last_error = '', updated_at = ? WHERE id = ?`, result.Port, result.Port, net.JoinHostPort(result.Listen, fmt.Sprint(result.Port)), result.Listen, now.Format(time.RFC3339Nano), serviceID)
			}
			if err != nil {
				succeeded = false
				taskError = "center: save REALITY service: " + err.Error()
			}
		}
		if succeeded {
			result := *envelope.ApplicationCommand
			var previousSecret sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT result_secret_id FROM application_commands WHERE id = ?`, taskID).Scan(&previousSecret); err != nil {
				return err
			}
			if previousSecret.Valid {
				if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, previousSecret.String); err != nil {
					return err
				}
			}
			secretID, err := s.putSecret(ctx, tx, []byte(result.ShareURI), "application-command:"+taskID)
			if err != nil {
				return err
			}
			result.ShareURI = ""
			publicResult, _ := json.Marshal(result)
			if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET result_json = ?, result_secret_id = ?, error = '', updated_at = ? WHERE id = ?`, publicResult, secretID, now.Format(time.RFC3339Nano), taskID); err != nil {
				return err
			}
		}
	}
	if !succeeded {
		if taskError == "" {
			taskError = "application operation failed"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'failed', lease_expires_at = '', error = ?, updated_at = ? WHERE id = ?`, taskError, now.Format(time.RFC3339Nano), taskID); err != nil {
			return err
		}
	}
	if !succeeded {
		if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, "failed", taskError); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !succeeded {
		return nil
	}
	result := *envelope.ApplicationCommand
	err = s.ensureRealityPublication(ctx, serviceID, gatewayID, input, result.SNIHostname)
	if err == nil {
		finalizeTx, beginErr := s.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		defer finalizeTx.Rollback()
		if _, updateErr := finalizeTx.ExecContext(ctx, `UPDATE application_commands SET state = 'succeeded', lease_expires_at = '', error = '', updated_at = ? WHERE id = ? AND state = 'running'`, s.now().UTC().Format(time.RFC3339Nano), taskID); updateErr != nil {
			return updateErr
		}
		if eventErr := s.recordTaskEvent(ctx, finalizeTx, taskID, agentID, "application.command", 1, "succeeded", "3x-ui REALITY and shared 443 access created"); eventErr != nil {
			return eventErr
		}
		return finalizeTx.Commit()
	}
	failure := "center: create REALITY access entry: " + err.Error()
	cleanupTx, beginErr := s.db.BeginTx(ctx, nil)
	if beginErr != nil {
		return errors.Join(err, beginErr)
	}
	defer cleanupTx.Rollback()
	var secretID sql.NullString
	_ = cleanupTx.QueryRowContext(ctx, `SELECT result_secret_id FROM application_commands WHERE id = ?`, taskID).Scan(&secretID)
	if _, updateErr := cleanupTx.ExecContext(ctx, `UPDATE application_commands SET state = 'failed', result_secret_id = NULL, error = ?, updated_at = ? WHERE id = ?`, failure, s.now().UTC().Format(time.RFC3339Nano), taskID); updateErr != nil {
		return errors.Join(err, updateErr)
	}
	if secretID.Valid {
		_, _ = cleanupTx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, secretID.String)
	}
	if eventErr := s.recordTaskEvent(ctx, cleanupTx, taskID, agentID, "application.command", 1, "failed", failure); eventErr != nil {
		return errors.Join(err, eventErr)
	}
	if commitErr := cleanupTx.Commit(); commitErr != nil {
		return errors.Join(err, commitErr)
	}
	return nil
}

func (s *Store) ensureRealityPublication(ctx context.Context, serviceID, gatewayID string, input RealityCommandTask, sniHostname string) error {
	var existingID, existingGateway, existingSNI, existingDNS string
	err := s.db.QueryRowContext(ctx, `SELECT id, COALESCE(gateway_node_id, ''), sni_hostname, dns_provider FROM publications WHERE service_id = ? AND kind = 'public_shared_443' AND hostname = ? AND status <> 'stopped'`, serviceID, input.ConnectHostname).Scan(&existingID, &existingGateway, &existingSNI, &existingDNS)
	if err == nil {
		if existingGateway != gatewayID || existingSNI != sniHostname || existingDNS != input.DNSProvider {
			return errors.New("center: existing REALITY access entry does not match the requested gateway, SNI, and DNS")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.CreatePublication(ctx, PublicationInput{ServiceID: serviceID, Kind: publicationShared443, GatewayNodeID: gatewayID, Hostname: input.ConnectHostname, SNIHostname: sniHostname, DNSProvider: input.DNSProvider})
	return err
}
