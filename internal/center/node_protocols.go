package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/nodeprotocol"
	"github.com/petauron/vastora/internal/secret"
)

func (s *Server) handleNodeProtocols(w http.ResponseWriter, request *http.Request) {
	value, err := s.store.NodeProtocols(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) handleConfigureNodeProtocols(w http.ResponseWriter, request *http.Request) {
	var input nodeprotocol.Selection
	if err := decodeJSON(request, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	value, err := s.store.ConfigureNodeProtocols(request.Context(), request.PathValue("id"), input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, value)
}

func (s *Store) NodeProtocols(ctx context.Context, serviceID string) (nodeprotocol.View, error) {
	value := nodeprotocol.View{Selection: nodeprotocol.Selection{VLESS: true}, State: "succeeded"}
	var appKey string
	if err := s.db.QueryRowContext(ctx, `SELECT a.app_key FROM services s JOIN applications a ON a.id=s.application_id WHERE s.id=? AND s.status<>'stopped'`, serviceID).Scan(&appKey); err != nil {
		return value, err
	}
	if appKey == meridianAppKey {
		var status string
		if err := s.db.QueryRowContext(ctx, `SELECT endpoint.vless_enabled,endpoint.hy2_enabled,endpoint.status FROM meridian_endpoints endpoint WHERE endpoint.service_id=? AND endpoint.status<>'retired'`, serviceID).Scan(&value.VLESS, &value.HY2, &status); err != nil {
			return value, errors.New("center: subscription node is unavailable")
		}
		if status == "pending" || status == "applying" {
			value.State = "running"
		} else if status == "failed" {
			value.State = "failed"
		}
		return value, nil
	}
	if appKey != threeXUIAppKey {
		return value, errors.New("center: subscription node is unavailable")
	}
	err := s.db.QueryRowContext(ctx, `SELECT p.vless_enabled,p.hy2_enabled,p.command_id,COALESCE(c.state,'succeeded') FROM three_x_ui_node_protocols p LEFT JOIN application_commands c ON c.id=p.command_id WHERE p.service_id=?`, serviceID).Scan(&value.VLESS, &value.HY2, &value.CommandID, &value.State)
	if errors.Is(err, sql.ErrNoRows) {
		return value, nil
	}
	if err == nil && value.CommandID != "" {
		command, commandErr := s.ApplicationCommand(ctx, value.CommandID)
		if commandErr != nil {
			return value, commandErr
		}
		value.CommandID, value.State = command.ID, command.State
		if command.State != "succeeded" {
			var encoded []byte
			if err := s.db.QueryRowContext(ctx, `SELECT input_json FROM application_commands WHERE id=?`, command.ID).Scan(&encoded); err != nil {
				return value, err
			}
			var task nodeprotocol.Task
			if json.Unmarshal(encoded, &task) != nil {
				return value, errors.New("center: invalid node protocol task")
			}
			value.Selection = task.Selection
		}
	}
	return value, err
}

func (s *Store) ConfigureNodeProtocols(ctx context.Context, serviceID string, selection nodeprotocol.Selection) (ApplicationCommandView, error) {
	if err := selection.Validate(); err != nil {
		return ApplicationCommandView{}, err
	}
	var appKey string
	if err := s.db.QueryRowContext(ctx, `SELECT application.app_key FROM services service JOIN applications application ON application.id=service.application_id WHERE service.id=? AND service.status<>'stopped'`, serviceID).Scan(&appKey); err != nil {
		return ApplicationCommandView{}, errors.New("center: subscription node is unavailable")
	}
	if appKey == meridianAppKey {
		return s.configureMeridianNodeProtocols(ctx, serviceID, selection)
	}
	if appKey != threeXUIAppKey {
		return ApplicationCommandView{}, errors.New("center: subscription node is unavailable")
	}
	if err := s.ensureServicePublicationChangeAllowed(ctx, s.db, serviceID); err != nil {
		return ApplicationCommandView{}, err
	}
	// Issue a leaf certificate only for this node. Never copy the Center or Site
	// wildcard key to a public proxy, and never offer insecure TLS subscriptions.
	var hostname string
	if err := s.db.QueryRowContext(ctx, `SELECT p.hostname FROM publications p JOIN services svc ON svc.id=p.service_id JOIN applications a ON a.id=svc.application_id WHERE svc.id=? AND a.app_key=? AND p.kind='public_shared_443' AND p.status='ready' ORDER BY p.updated_at DESC LIMIT 1`, serviceID, threeXUIAppKey).Scan(&hostname); err != nil {
		return ApplicationCommandView{}, errors.New("center: configure the node public domain before changing protocols")
	}
	var certificate managedCertificate
	if err := s.validateNodeProtocolPreconditions(ctx, s.db, serviceID, hostname, selection); err != nil {
		return ApplicationCommandView{}, err
	}
	if selection.HY2 {
		var secretID, certHostname, notAfter string
		err := s.db.QueryRowContext(ctx, `SELECT COALESCE(certificate_secret_id,''),certificate_hostname,certificate_not_after FROM three_x_ui_node_protocols WHERE service_id=?`, serviceID).Scan(&secretID, &certHostname, &notAfter)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ApplicationCommandView{}, err
		}
		expiry, _ := time.Parse(time.RFC3339Nano, notAfter)
		if secretID != "" && certHostname == hostname && expiry.After(s.now().Add(privateCertificateRenewBefore)) {
			encoded, err := s.getSecret(ctx, secretID, "node-protocol-certificate:"+serviceID)
			if err != nil {
				return ApplicationCommandView{}, err
			}
			if json.Unmarshal(encoded, &certificate) != nil {
				return ApplicationCommandView{}, errors.New("center: node certificate is invalid")
			}
		} else {
			var err error
			certificate, err = s.obtainPrivateCertificate(ctx, hostname)
			if err != nil {
				return ApplicationCommandView{}, err
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	defer tx.Rollback()
	if err := s.validateNodeProtocolPreconditions(ctx, tx, serviceID, hostname, selection); err != nil {
		return ApplicationCommandView{}, err
	}
	var targetAgent, role, name, tag, displayName, applicationID string
	if err = tx.QueryRowContext(ctx, `SELECT a.id,a.node_id,a.role,svc.name,plan.inbound_tag,svc.display_name FROM services svc JOIN applications a ON a.id=svc.application_id JOIN three_x_ui_inbound_plans plan ON plan.service_id=svc.id WHERE svc.id=? AND svc.app_protocol='vless/tcp/reality' AND svc.status<>'stopped' AND a.app_key=? AND a.status='running'`, serviceID, threeXUIAppKey).Scan(&applicationID, &targetAgent, &role, &name, &tag, &displayName); err != nil {
		return ApplicationCommandView{}, errors.New("center: managed subscription node is unavailable")
	}
	controllerAgent, nodeID, err := threeXUIDataPlaneController(ctx, tx, applicationID, role)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	inboundID, err := strconv.Atoi(strings.TrimPrefix(name, "inbound-"))
	if err != nil || inboundID < 1 || tag == "" {
		return ApplicationCommandView{}, errors.New("center: subscription node identity is unavailable")
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE (agent_id IN (?,?) OR application_id=?) AND kind<>? AND (state IN ('pending','running') OR reconciliation_required=1)`, targetAgent, controllerAgent, applicationID, controllerCommandKind).Scan(&active); err != nil {
		return ApplicationCommandView{}, err
	}
	if active > 0 {
		return ApplicationCommandView{}, errors.New("center: another node operation is in progress")
	}
	token, err := randomToken(18)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	id := "application-command-" + token
	task := nodeprotocol.Task{Selection: selection, Phase: "prepare", RootCommandID: id, ServiceID: serviceID, ApplicationID: applicationID, TargetAgentID: targetAgent, ControllerAgentID: controllerAgent, InboundID: inboundID, InboundTag: tag, TargetNodeID: nodeID, Hostname: hostname, DisplayName: displayName}
	encoded, _ := json.Marshal(task)
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?)`, id, applicationID, targetAgent, targetAgent, nodeprotocol.CommandKind, encoded, now, now); err != nil {
		return ApplicationCommandView{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO three_x_ui_node_protocols(service_id,command_id) VALUES(?,?) ON CONFLICT(service_id) DO UPDATE SET command_id=excluded.command_id`, serviceID, id); err != nil {
		return ApplicationCommandView{}, err
	}
	if selection.HY2 {
		var previous sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT certificate_secret_id FROM three_x_ui_node_protocols WHERE service_id=?`, serviceID).Scan(&previous); err != nil {
			return ApplicationCommandView{}, err
		}
		encodedCert, _ := json.Marshal(certificate)
		secretID, err := s.putSecret(ctx, tx, encodedCert, "node-protocol-certificate:"+serviceID)
		if err != nil {
			return ApplicationCommandView{}, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE three_x_ui_node_protocols SET certificate_secret_id=?,certificate_hostname=?,certificate_not_after=? WHERE service_id=?`, secretID, hostname, certificate.NotAfter.UTC().Format(time.RFC3339Nano), serviceID); err != nil {
			return ApplicationCommandView{}, err
		}
		if previous.Valid {
			if _, err = tx.ExecContext(ctx, `DELETE FROM secrets WHERE id=?`, previous.String); err != nil {
				return ApplicationCommandView{}, err
			}
		}
	}
	if err = s.recordTaskEvent(ctx, tx, id, targetAgent, "application.command", 1, "queued", "Node protocol update queued"); err != nil {
		return ApplicationCommandView{}, err
	}
	if err = tx.Commit(); err != nil {
		return ApplicationCommandView{}, err
	}
	return s.ApplicationCommand(ctx, id)
}

func (s *Store) configureMeridianNodeProtocols(ctx context.Context, serviceID string, selection nodeprotocol.Selection) (ApplicationCommandView, error) {
	if !selection.VLESS {
		return ApplicationCommandView{}, errors.New("center: Meridian requires VLESS as the public TCP 443 anchor")
	}
	if err := s.ensureServicePublicationChangeAllowed(ctx, s.db, serviceID); err != nil {
		return ApplicationCommandView{}, err
	}
	var hostname string
	if err := s.db.QueryRowContext(ctx, `SELECT publication.hostname FROM publications publication
		JOIN meridian_endpoints endpoint ON endpoint.service_id=publication.service_id
		WHERE endpoint.service_id=? AND publication.kind='public_shared_443' AND publication.status='ready'
		ORDER BY publication.updated_at DESC LIMIT 1`, serviceID).Scan(&hostname); err != nil {
		return ApplicationCommandView{}, errors.New("center: configure the node public domain before changing protocols")
	}
	var certificate managedCertificate
	var err error
	if selection.HY2 {
		var endpointID, certificateHostname, notAfter string
		var certificateSecretID, privateKeySecretID sql.NullString
		if err := s.db.QueryRowContext(ctx, `SELECT id,hy2_server_name,hy2_certificate_secret_id,hy2_private_key_secret_id,hy2_certificate_not_after
			FROM meridian_endpoints WHERE service_id=? AND status<>'retired'`, serviceID).Scan(&endpointID, &certificateHostname, &certificateSecretID, &privateKeySecretID, &notAfter); err != nil {
			return ApplicationCommandView{}, errors.New("center: Meridian subscription node is unavailable")
		}
		expiresAt, _ := time.Parse(time.RFC3339Nano, notAfter)
		if certificateHostname == hostname && certificateSecretID.Valid && privateKeySecretID.Valid && expiresAt.After(s.now().Add(privateCertificateRenewBefore)) {
			certificatePEM, secretErr := s.getSecret(ctx, certificateSecretID.String, meridianHY2CertificateSecretContext(endpointID))
			if secretErr != nil {
				return ApplicationCommandView{}, errors.New("center: stored Meridian Hysteria certificate is unavailable")
			}
			privateKeyPEM, secretErr := s.getSecret(ctx, privateKeySecretID.String, meridianHY2PrivateKeySecretContext(endpointID))
			if secretErr != nil {
				return ApplicationCommandView{}, errors.New("center: stored Meridian Hysteria key is unavailable")
			}
			certificate = managedCertificate{CertificatePEM: string(certificatePEM), PrivateKeyPEM: string(privateKeyPEM), NotAfter: expiresAt}
		} else {
			certificate, err = s.obtainPrivateCertificate(ctx, hostname)
			if err != nil {
				return ApplicationCommandView{}, err
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	defer tx.Rollback()
	if err := ensureMeridianManagementWritable(ctx, tx); err != nil {
		return ApplicationCommandView{}, err
	}
	var endpointID, applicationID, hy2Tag string
	var oldCertificateID, oldPrivateKeyID sql.NullString
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT endpoint.id,endpoint.application_id,endpoint.hy2_inbound_tag,
		endpoint.hy2_certificate_secret_id,endpoint.hy2_private_key_secret_id,endpoint.status
		FROM meridian_endpoints endpoint JOIN applications application ON application.id=endpoint.application_id
		WHERE endpoint.service_id=? AND endpoint.status<>'retired' AND application.app_key=? AND application.status='running'`, serviceID, meridianAppKey).Scan(
		&endpointID, &applicationID, &hy2Tag, &oldCertificateID, &oldPrivateKeyID, &status,
	); err != nil {
		return ApplicationCommandView{}, errors.New("center: Meridian subscription node is unavailable")
	}
	if status == "pending" || status == "applying" {
		return ApplicationCommandView{}, errors.New("center: Meridian node already has an active runtime change")
	}
	if err := s.ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx, tx, endpointID); err != nil {
		return ApplicationCommandView{}, err
	}
	var certificateID, privateKeyID any
	var newCertificateID, newPrivateKeyID string
	if selection.HY2 {
		if hy2Tag == "" {
			token, tokenErr := randomToken(12)
			if tokenErr != nil {
				return ApplicationCommandView{}, tokenErr
			}
			hy2Tag = "meridian-hy2-" + token
		}
		newCertificateID, err = s.putSecret(ctx, tx, []byte(certificate.CertificatePEM), meridianHY2CertificateSecretContext(endpointID))
		if err != nil {
			return ApplicationCommandView{}, err
		}
		newPrivateKeyID, err = s.putSecret(ctx, tx, []byte(certificate.PrivateKeyPEM), meridianHY2PrivateKeySecretContext(endpointID))
		if err != nil {
			return ApplicationCommandView{}, err
		}
		certificateID, privateKeyID = newCertificateID, newPrivateKeyID
		rows, queryErr := tx.QueryContext(ctx, `SELECT id FROM meridian_credentials WHERE endpoint_id=? AND kind='native' AND hy2_auth_secret_id IS NULL ORDER BY id`, endpointID)
		if queryErr != nil {
			return ApplicationCommandView{}, queryErr
		}
		credentialIDs := []string{}
		for rows.Next() {
			var credentialID string
			if scanErr := rows.Scan(&credentialID); scanErr != nil {
				rows.Close()
				return ApplicationCommandView{}, scanErr
			}
			credentialIDs = append(credentialIDs, credentialID)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			rows.Close()
			return ApplicationCommandView{}, rowsErr
		}
		if closeErr := rows.Close(); closeErr != nil {
			return ApplicationCommandView{}, closeErr
		}
		for _, credentialID := range credentialIDs {
			auth, tokenErr := randomToken(32)
			if tokenErr != nil {
				return ApplicationCommandView{}, tokenErr
			}
			secretID, secretErr := s.putSecret(ctx, tx, []byte(auth), meridianCredentialHY2SecretContext(credentialID))
			if secretErr != nil {
				return ApplicationCommandView{}, secretErr
			}
			if _, updateErr := tx.ExecContext(ctx, `UPDATE meridian_credentials SET hy2_auth_secret_id=?,hy2_identity_sha256=?,updated_at=? WHERE id=? AND hy2_auth_secret_id IS NULL`, secretID, meridian.Identity(auth), s.now().UTC().Format(time.RFC3339Nano), credentialID); updateErr != nil {
				return ApplicationCommandView{}, updateErr
			}
		}
	} else {
		certificateID, privateKeyID = oldCertificateID, oldPrivateKeyID
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET vless_enabled=?,hy2_enabled=?,hy2_inbound_tag=?,hy2_server_name=CASE WHEN ? THEN ? ELSE hy2_server_name END,
		hy2_certificate_secret_id=?,hy2_private_key_secret_id=?,hy2_certificate_not_after=CASE WHEN ? THEN ? ELSE hy2_certificate_not_after END,
		desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error='',updated_at=?
		WHERE id=?`, boolInt(selection.VLESS), boolInt(selection.HY2), hy2Tag, selection.HY2, hostname, certificateID, privateKeyID, selection.HY2, certificate.NotAfter.UTC().Format(time.RFC3339Nano), now, endpointID)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ApplicationCommandView{}, errors.New("center: Meridian subscription node changed during protocol update")
	}
	commandID, err := s.queueMeridianRuntime(ctx, tx, endpointID, false)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	if selection.HY2 {
		for _, previous := range []sql.NullString{oldCertificateID, oldPrivateKeyID} {
			if previous.Valid && previous.String != newCertificateID && previous.String != newPrivateKeyID {
				if _, deleteErr := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id=?`, previous.String); deleteErr != nil {
					return ApplicationCommandView{}, deleteErr
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return ApplicationCommandView{}, err
	}
	return s.ApplicationCommand(ctx, commandID)
}

func (s *Store) hydrateNodeProtocolTask(ctx context.Context, tx *sql.Tx, task *nodeprotocol.Task) error {
	if task.Selection.Validate() != nil || task.RootCommandID == "" || task.ServiceID == "" || task.ApplicationID == "" || task.TargetAgentID == "" || task.ControllerAgentID == "" || task.InboundID < 1 || task.InboundTag == "" || (task.Phase != "prepare" && task.Phase != "apply" && task.Phase != "verify") {
		return errors.New("center: stored node protocol operation is invalid")
	}
	var applicationID, targetAgent, role, tag string
	if err := tx.QueryRowContext(ctx, `SELECT a.id,a.node_id,a.role,p.inbound_tag FROM services svc JOIN applications a ON a.id=svc.application_id JOIN three_x_ui_inbound_plans p ON p.service_id=svc.id WHERE svc.id=? AND svc.status<>'stopped' AND a.status='running'`, task.ServiceID).Scan(&applicationID, &targetAgent, &role, &tag); err != nil {
		return err
	}
	controllerAgent, nodeID, err := threeXUIDataPlaneController(ctx, tx, applicationID, role)
	if err != nil || applicationID != task.ApplicationID || targetAgent != task.TargetAgentID || controllerAgent != task.ControllerAgentID || nodeID != task.TargetNodeID || tag != task.InboundTag {
		return errors.New("center: node protocol target changed")
	}
	if !task.HY2 || task.Phase != "apply" {
		return nil
	}
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT sec.sealed FROM three_x_ui_node_protocols p JOIN secrets sec ON sec.id=p.certificate_secret_id WHERE p.service_id=? AND p.certificate_hostname=?`, task.ServiceID, task.Hostname).Scan(&sealed); err != nil {
		return err
	}
	encoded, err := secret.Open(s.key, sealed, []byte("node-protocol-certificate:"+task.ServiceID))
	if err != nil {
		return err
	}
	var cert managedCertificate
	if json.Unmarshal(encoded, &cert) != nil || !cert.NotAfter.After(s.now().Add(time.Hour)) {
		return errors.New("center: node certificate is unavailable or expired")
	}
	task.CertificatePEM, task.PrivateKeyPEM = cert.CertificatePEM, cert.PrivateKeyPEM
	return nil
}

func (s *Store) validateNodeProtocolPreconditions(ctx context.Context, q networkQueryer, serviceID, hostname string, selection nodeprotocol.Selection) error {
	var exists int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE service_id=? AND hostname=? AND kind='public_shared_443' AND status='ready'`, serviceID, hostname).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return errors.New("center: configure the node public domain before changing protocols")
	}
	if selection.VLESS {
		var ready int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM three_x_ui_reality_guards WHERE service_id=? AND status='ready'`, serviceID).Scan(&ready); err != nil {
			return err
		}
		if ready != 1 {
			return errors.New("center: resolve the node access warning before enabling VLESS")
		}
	}
	if selection.HY2 {
		var encoded []byte
		var tag, status string
		err := q.QueryRowContext(ctx, `SELECT l.desired_json,p.inbound_tag,l.status FROM services svc JOIN applications a ON a.id=svc.application_id JOIN three_x_ui_inbound_plans p ON p.service_id=svc.id JOIN landing_proxy_states l ON l.node_id=a.node_id WHERE svc.id=?`, serviceID).Scan(&encoded, &tag, &status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if status != "ready" && status != "stopped" {
				return errors.New("center: another node operation is in progress")
			}
			var desired landing.DesiredState
			if json.Unmarshal(encoded, &desired) != nil {
				return errors.New("center: landing settings are unavailable")
			}
			if desired.Proxy != nil {
				found := false
				for _, tagValue := range desired.Proxy.InboundTags {
					if tagValue == nodeprotocol.HY2Tag(tag) {
						found = true
					}
				}
				if !found {
					return errors.New("center: switch to the node's own exit before adding HY2, then select the landing server again")
				}
			}
		}
	}
	return nil
}

func (s *Store) completeNodeProtocolCommand(ctx context.Context, commit projectionCommit, tx *sql.Tx, id, agentID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var task nodeprotocol.Task
	var envelope struct {
		ProtocolCommand *nodeprotocol.Result `json:"protocolCommand"`
	}
	if json.Unmarshal(inputJSON, &task) != nil {
		return errors.New("center: invalid node protocol task")
	}
	if succeeded && (json.Unmarshal(rawResult, &envelope) != nil || envelope.ProtocolCommand == nil || envelope.ProtocolCommand.Phase != task.Phase || (task.Phase == "apply" || task.Phase == "verify") && task.HY2 && envelope.ProtocolCommand.HY2InboundID < 1 || task.Phase == "verify" && envelope.ProtocolCommand.HY2InboundID != task.HY2InboundID) {
		succeeded = false
		taskError = "Agent did not confirm the requested protocols"
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if succeeded && (task.Phase == "prepare" || task.Phase == "apply") {
		nextAgent := task.ControllerAgentID
		if task.Phase == "prepare" {
			task.Phase = "apply"
		} else {
			task.Phase = "verify"
			task.HY2InboundID = envelope.ProtocolCommand.HY2InboundID
			nextAgent = task.TargetAgentID
		}
		encoded, _ := json.Marshal(task)
		nextID := task.RootCommandID + "-" + task.Phase
		nextResult, _ := json.Marshal(map[string]string{"nextCommandId": nextID})
		if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state='succeeded',result_json=?,lease_expires_at='',updated_at=? WHERE id=?`, nextResult, now, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?)`, nextID, task.ApplicationID, nextAgent, task.TargetAgentID, nodeprotocol.CommandKind, encoded, now, now); err != nil {
			return err
		}
		if err := s.recordTaskEvent(ctx, tx, nextID, nextAgent, "application.command", 1, "queued", "Updating node protocols"); err != nil {
			return err
		}
		return commit(tx)
	}
	state := "failed"
	if succeeded {
		state = "succeeded"
		if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_node_protocols SET vless_enabled=?,hy2_enabled=?,hy2_inbound_id=?,certificate_applied_not_after=CASE WHEN ? THEN certificate_not_after ELSE '' END WHERE service_id=? AND command_id=?`, task.VLESS, task.HY2, envelope.ProtocolCommand.HY2InboundID, task.HY2, task.ServiceID, task.RootCommandID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state=?,error=?,result_json=?,lease_expires_at='',updated_at=? WHERE id=?`, state, taskError, []byte(`{}`), now, id); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, "application.command", 1, state, taskError); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Store) renewNodeProtocolCertificates(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT p.service_id,p.vless_enabled FROM three_x_ui_node_protocols p JOIN services svc ON svc.id=p.service_id WHERE p.hy2_enabled=1 AND svc.status<>'stopped' AND p.certificate_applied_not_after<?`, s.now().Add(privateCertificateRenewBefore).UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	type renewal struct {
		id    string
		vless bool
	}
	var pending []renewal
	for rows.Next() {
		var value renewal
		if err = rows.Scan(&value.id, &value.vless); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, value := range pending {
		if _, err := s.ConfigureNodeProtocols(ctx, value.id, nodeprotocol.Selection{VLESS: value.vless, HY2: true}); err != nil {
			failures = append(failures, fmt.Errorf("renew node certificate: %w", err))
		}
	}
	meridianRows, err := s.db.QueryContext(ctx, `SELECT service_id,vless_enabled FROM meridian_endpoints WHERE hy2_enabled=1 AND status='ready' AND hy2_certificate_not_after<? ORDER BY service_id`, s.now().Add(privateCertificateRenewBefore).UTC().Format(time.RFC3339Nano))
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	pending = pending[:0]
	for meridianRows.Next() {
		var value renewal
		if err := meridianRows.Scan(&value.id, &value.vless); err != nil {
			meridianRows.Close()
			return errors.Join(append(failures, err)...)
		}
		pending = append(pending, value)
	}
	if err := meridianRows.Err(); err != nil {
		meridianRows.Close()
		return errors.Join(append(failures, err)...)
	}
	if err := meridianRows.Close(); err != nil {
		return errors.Join(append(failures, err)...)
	}
	for _, value := range pending {
		if _, err := s.ConfigureNodeProtocols(ctx, value.id, nodeprotocol.Selection{VLESS: value.vless, HY2: true}); err != nil {
			failures = append(failures, fmt.Errorf("renew Meridian node certificate: %w", err))
		}
	}
	return errors.Join(failures...)
}
