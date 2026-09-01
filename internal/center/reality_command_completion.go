package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

func (s *Store) completeRealityCreateCommand(ctx context.Context, tx *sql.Tx, taskID, agentID, applicationID, gatewayID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
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
		}
		if succeeded {
			for _, excluded := range input.ExcludedSNI {
				if result.ServerName == excluded {
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
				err = nil
			}
			var siteID string
			if err == nil {
				err = tx.QueryRowContext(ctx, `SELECT site_id FROM applications WHERE id = ?`, applicationID).Scan(&siteID)
			}
			var duplicateDisplayName int
			if err == nil {
				err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE site_id = ? AND id <> ? AND app_protocol = 'vless/tcp/reality' AND status <> 'stopped' AND display_name = ? COLLATE NOCASE`, siteID, serviceID, result.DisplayName).Scan(&duplicateDisplayName)
			}
			if err == nil && duplicateDisplayName != 0 {
				err = errors.New("this Site already has a REALITY node with that display name")
			}
			if err == nil && serviceID == "" {
				serviceID, err = randomToken(18)
				if err == nil {
					_, err = tx.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, display_name, region_code, protocol, container_port, host_port, endpoint, source, app_protocol, management, observed_listen, status, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, 'tcp', ?, ?, ?, 'observed', 'vless/tcp/reality', 0, ?, 'ready', ?, ?)`, serviceID, applicationID, siteID, serviceName, result.DisplayName, input.RegionCode, result.Port, result.Port, net.JoinHostPort(result.Listen, fmt.Sprint(result.Port)), result.Listen, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
				}
			} else if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE services SET display_name = ?, region_code = ?, protocol = 'tcp', container_port = ?, host_port = ?, endpoint = ?, source = 'observed', app_protocol = 'vless/tcp/reality', observed_listen = ?, status = 'ready', last_error = '', updated_at = ? WHERE id = ?`, result.DisplayName, input.RegionCode, result.Port, result.Port, net.JoinHostPort(result.Listen, fmt.Sprint(result.Port)), result.Listen, now.Format(time.RFC3339Nano), serviceID)
			}
			if err != nil {
				succeeded = false
				taskError = "center: save REALITY service: " + err.Error()
			}
			if succeeded {
				_, guardErr := tx.ExecContext(ctx, `INSERT INTO three_x_ui_reality_guards(
					service_id, target_host, target_ip, server_name, node_asn, target_asn, cdn_provider,
					companion_inbound_id, companion_tag, companion_port, revision, status,
					verified_at, last_error, created_at, updated_at
				) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 'ready', ?, '', ?, ?)
				ON CONFLICT(service_id) DO UPDATE SET target_host = excluded.target_host,
					target_ip = excluded.target_ip, server_name = excluded.server_name,
					node_asn = excluded.node_asn, target_asn = excluded.target_asn,
					cdn_provider = excluded.cdn_provider,
					companion_inbound_id = excluded.companion_inbound_id,
					companion_tag = excluded.companion_tag, companion_port = excluded.companion_port,
					revision = three_x_ui_reality_guards.revision + 1, status = 'ready',
					verified_at = excluded.verified_at, last_error = '', updated_at = excluded.updated_at`,
					serviceID, result.TargetHost, result.TargetIP, result.ServerName, result.NodeASN, result.TargetASN, result.CDNProvider,
					result.CompanionInboundID, result.CompanionTag, result.CompanionPort,
					now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
				if guardErr != nil {
					succeeded = false
					taskError = "center: save REALITY fallback guard: " + guardErr.Error()
				}
			}
			if succeeded {
				nextResetAt, planErr := nextThreeXUIInboundResetAt(ctx, tx, serviceID, now, input.InboundResetDay)
				if planErr == nil {
					planErr = upsertThreeXUIInboundPlan(ctx, tx, serviceID, result.InboundTag, input.InboundTotalBytes, input.InboundResetDay, nextResetAt, 1, now)
				}
				if planErr != nil {
					succeeded = false
					taskError = "center: save REALITY inbound traffic plan: " + planErr.Error()
				}
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
			var secretID any
			if result.ClientCreated {
				value, err := s.putSecret(ctx, tx, []byte(result.ShareURI), "application-command:"+taskID)
				if err != nil {
					return err
				}
				secretID = value
			}
			result.ShareURI = ""
			result.InboundTag = ""
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
	} else {
		// The Agent-side REALITY mutation, service projection, traffic plan, and
		// optional one-time secret are now durable. Publication is a separate
		// lifecycle: mark the command terminal in this same transaction so a
		// Center crash can never replay the mutation and later quarantine a ready
		// service with an unreachable secret.
		if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'succeeded', lease_expires_at = '', error = '', updated_at = ? WHERE id = ? AND state = 'running'`, now.Format(time.RFC3339Nano), taskID); err != nil {
			return err
		}
		if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, "succeeded", "3x-ui REALITY node created; access entry queued"); err != nil {
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
	err := s.ensureRealityPublication(ctx, serviceID, gatewayID, input, result.ServerName)
	warning := ""
	if err != nil {
		warning = "center: create REALITY access entry: " + err.Error()
	}
	// This is a best-effort projection update after the terminal transaction.
	// A failed write is recovered from the durable succeeded result at startup;
	// it must not make the Agent replay an already committed mutation.
	_, _ = s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE application_commands SET error = ?, updated_at = ? WHERE id = ? AND state = 'succeeded'`, warning, s.now().UTC().Format(time.RFC3339Nano), taskID)
	return nil
}
