package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ServiceHandover accepts existing object identities, never a caller-selected
// endpoint, host command or access policy. The old public identity survives.
type ServiceHandover struct {
	TargetServiceID  string `json:"targetServiceId"`
	SourceSHA256     string `json:"sourceSha256"`
	TargetSHA256     string `json:"targetSha256"`
	BackupsConfirmed bool   `json:"backupsConfirmed"`
}

type ServiceHandoverPlan struct {
	SourceServiceID string `json:"sourceServiceId"`
	TargetServiceID string `json:"targetServiceId"`
	SourceSHA256    string `json:"sourceSha256"`
	TargetSHA256    string `json:"targetSha256"`
	Publications    int    `json:"publications"`
	State           string `json:"state"`
}

type serviceHandoverSnapshot struct {
	ID, ApplicationID, SiteID, Name, Protocol, ProxyProtocol, Endpoint, Source, UpdatedAt string
	NodeID, AppKey, ApplicationStatus, Adoption, ManifestSHA256                           string
	ContainerPort, HostPort                                                               int
	Management                                                                            bool
	ReceiptSHA256                                                                         string
	ResourceCount                                                                         int
	Publications                                                                          []json.RawMessage
}

// A retired pilot runtime must never be replayed by a generic Agent upgrade.
const obsoletePilotRuntimeReplay = `state='pending' AND attempt=0 AND reconciliation_required=0 AND operation='configure'
 AND NOT EXISTS(SELECT 1 FROM task_executions execution WHERE execution.task_id=deployments.id)
 AND EXISTS(SELECT 1 FROM task_events event WHERE event.task_id=deployments.id AND event.kind='application.apply' AND event.event='queued' AND event.message='application runtime generation '||deployments.runtime_generation)
 AND EXISTS(SELECT 1 FROM glivela_pilot_checkpoint pilot JOIN meridian_endpoints endpoint ON endpoint.id=pilot.endpoint_id WHERE endpoint.application_id=deployments.application_id)`

var errServiceHandover = errors.New("center: service handover requires current owned resources and reviewed backups")

func handoverIdentifier(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 128 && !strings.ContainsAny(value, "/\\\x00\r\n")
}

func readServiceHandover(ctx context.Context, tx *sql.Tx, id string) (serviceHandoverSnapshot, error) {
	var value serviceHandoverSnapshot
	var raw []byte
	if !handoverIdentifier(id) {
		return value, errServiceHandover
	}
	err := tx.QueryRowContext(ctx, `SELECT s.id,s.application_id,s.site_id,s.name,s.protocol,s.proxy_protocol,s.endpoint,s.source,s.updated_at,s.container_port,s.host_port,s.management,a.node_id,a.app_key,a.status,r.adoption_state,r.manifest_sha256,r.resources_json FROM services s JOIN applications a ON a.id=s.application_id JOIN application_resources r ON r.application_id=a.id WHERE s.id=? AND s.status='ready'`, id).Scan(&value.ID, &value.ApplicationID, &value.SiteID, &value.Name, &value.Protocol, &value.ProxyProtocol, &value.Endpoint, &value.Source, &value.UpdatedAt, &value.ContainerPort, &value.HostPort, &value.Management, &value.NodeID, &value.AppKey, &value.ApplicationStatus, &value.Adoption, &value.ManifestSHA256, &raw)
	if err != nil || value.Adoption != "ready" || len(raw) > 1<<20 {
		return value, errServiceHandover
	}
	manifest, manifestErr := hex.DecodeString(value.ManifestSHA256)
	if manifestErr != nil || len(manifest) != 32 || hex.EncodeToString(manifest) != value.ManifestSHA256 {
		return value, errServiceHandover
	}
	var receipt maintenanceReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || receipt.ApplicationID != value.ApplicationID || receipt.AppKey != value.AppKey || receipt.ManifestSHA256 != value.ManifestSHA256 || receipt.State != "ready" || receipt.Runtime != "docker" && receipt.Runtime != "systemd" {
		return value, errServiceHandover
	}
	value.ResourceCount = len(receipt.Resources)
	digest := sha256.Sum256(raw)
	value.ReceiptSHA256 = hex.EncodeToString(digest[:])
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM deployments WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1) AND NOT (`+obsoletePilotRuntimeReplay+`))+(SELECT COUNT(*) FROM application_commands WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1))+(SELECT COUNT(*) FROM application_maintenance WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1))+(SELECT COUNT(*) FROM application_adoptions WHERE application_id=? AND state IN ('pending','running'))`, value.ApplicationID, value.ApplicationID, value.ApplicationID, value.ApplicationID).Scan(&active); err != nil {
		return value, err
	}
	if active != 0 {
		return value, errServiceHandover
	}
	rows, err := tx.QueryContext(ctx, `SELECT json_object('id',id,'revision',desired_revision,'applied',applied_revision,'state',status,'kind',kind,'owner',ingress_owner,'entry',entry_node_id,'hostname',hostname,'sni',sni_hostname,'dnsProvider',dns_provider,'dnsRecord',dns_record_id,'accessApplication',access_application_id,'tls',tls_enabled,'updatedAt',updated_at) FROM publications WHERE service_id=? ORDER BY id`, id)
	if err != nil {
		return value, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return value, err
		}
		value.Publications = append(value.Publications, raw)
	}
	return value, rows.Err()
}

func serviceHandoverDigest(value serviceHandoverSnapshot) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func serviceHandoverPair(ctx context.Context, tx *sql.Tx, sourceID, targetID string) (serviceHandoverSnapshot, serviceHandoverSnapshot, error) {
	source, err := readServiceHandover(ctx, tx, sourceID)
	if err != nil {
		return source, serviceHandoverSnapshot{}, err
	}
	target, err := readServiceHandover(ctx, tx, targetID)
	if err != nil {
		return source, target, err
	}
	// The MVP does not introduce a new cross-node/site route or relax access.
	if source.ID == target.ID || source.ApplicationID == target.ApplicationID || source.SiteID != target.SiteID || source.NodeID != target.NodeID || source.Protocol != target.Protocol || source.ProxyProtocol != target.ProxyProtocol || source.Management != target.Management || target.Source != "catalog" || target.ApplicationStatus != "running" || target.ResourceCount == 0 || len(target.Publications) != 0 {
		return source, target, errServiceHandover
	}
	return source, target, nil
}

func (s *Store) PreviewServiceHandover(ctx context.Context, sourceID, targetID string) (ServiceHandoverPlan, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ServiceHandoverPlan{}, err
	}
	defer tx.Rollback()
	source, target, err := serviceHandoverPair(ctx, tx, sourceID, targetID)
	if err != nil {
		return ServiceHandoverPlan{}, err
	}
	return ServiceHandoverPlan{SourceServiceID: sourceID, TargetServiceID: targetID, SourceSHA256: serviceHandoverDigest(source), TargetSHA256: serviceHandoverDigest(target), Publications: len(source.Publications), State: "review-required"}, nil
}

type serviceHandoverAudit struct {
	ApplicationID, Name string
	Publications        int
	State               string
}

func (s *Store) HandoverService(ctx context.Context, sourceID string, input ServiceHandover) (ServiceHandoverPlan, error) {
	refused := ServiceHandoverPlan{}
	if !handoverIdentifier(sourceID) || !handoverIdentifier(input.TargetServiceID) || !input.BackupsConfirmed {
		return refused, errServiceHandover
	}
	for _, value := range []string{input.SourceSHA256, input.TargetSHA256} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != value {
			return refused, errServiceHandover
		}
	}
	raw, _ := json.Marshal(struct {
		Source string
		Input  ServiceHandover
	}{sourceID, input})
	digest := sha256.Sum256(raw)
	operation := "service-handover-" + hex.EncodeToString(digest[:20])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return refused, err
	}
	defer tx.Rollback()
	result := ServiceHandoverPlan{SourceServiceID: sourceID, TargetServiceID: input.TargetServiceID, SourceSHA256: input.SourceSHA256, TargetSHA256: input.TargetSHA256, State: "binding-updated-verification-pending"}
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT message FROM task_events WHERE task_id=? AND kind='service.handover' AND event='succeeded' ORDER BY created_at LIMIT 1`, operation).Scan(&previous)
	if err == nil {
		var audit serviceHandoverAudit
		var application, name string
		if json.Unmarshal(previous, &audit) != nil || audit.State != result.State {
			return refused, errServiceHandover
		}
		if tx.QueryRowContext(ctx, `SELECT application_id,name FROM services WHERE id=?`, sourceID).Scan(&application, &name) != nil || application != audit.ApplicationID || name != audit.Name {
			return refused, errServiceHandover
		}
		result.Publications = audit.Publications
		return result, nil // Lost-response retry does not rebind/requeue or claim health.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return refused, err
	}
	source, target, err := serviceHandoverPair(ctx, tx, sourceID, input.TargetServiceID)
	if err != nil {
		return refused, err
	}
	if serviceHandoverDigest(source) != input.SourceSHA256 || serviceHandoverDigest(target) != input.TargetSHA256 {
		return refused, errServiceHandover
	}
	stamp := s.now().UTC()
	// Retire only unclaimed automatic replays of this source's obsolete runtime.
	// Executed or ambiguous work still blocks handover; no task is acknowledged.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM deployments WHERE application_id=? AND (`+obsoletePilotRuntimeReplay+`)`, source.ApplicationID)
	if err != nil {
		return refused, err
	}
	var obsolete []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return refused, err
		}
		obsolete = append(obsolete, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return refused, err
	}
	rows.Close()
	for _, id := range obsolete {
		message := "Retired unclaimed automatic replay: runtime is owned by Glivela"
		if _, err := tx.ExecContext(ctx, `UPDATE deployments SET state='failed',error=?,updated_at=? WHERE id=?`, message, stamp.Format(time.RFC3339Nano), id); err != nil {
			return refused, err
		}
		if err := s.recordTaskEvent(ctx, tx, id, source.NodeID, "application.apply", applicationTaskRevision, "failed", message); err != nil {
			return refused, err
		}
	}
	// Retain the unpublished installation-created row as history. Its reserved
	// name cannot silently overwrite another row; a collision rolls back.
	if _, err = tx.ExecContext(ctx, `UPDATE services SET name=?,status='stopped',updated_at=? WHERE id=?`, "transferred-"+target.ID, stamp.Format(time.RFC3339Nano), target.ID); err != nil {
		return refused, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE services SET application_id=?,name=?,protocol=?,proxy_protocol=?,container_port=?,host_port=?,endpoint=?,source='catalog',app_protocol='',management=?,status='ready',last_error='',updated_at=? WHERE id=?`, target.ApplicationID, target.Name, target.Protocol, target.ProxyProtocol, target.ContainerPort, target.HostPort, target.Endpoint, target.Management, stamp.Format(time.RFC3339Nano), source.ID); err != nil {
		return refused, err
	}
	if err = s.reconcileServicePublications(ctx, tx, target.ApplicationID, source.ID, stamp); err != nil {
		return refused, err
	}
	audit, _ := json.Marshal(serviceHandoverAudit{ApplicationID: target.ApplicationID, Name: target.Name, Publications: len(source.Publications), State: result.State})
	// Only this new metadata operation succeeded. No old task or remote
	// service is acknowledged; publication/route verification stays pending.
	if err = s.recordTaskEvent(ctx, tx, operation, target.NodeID, "service.handover", 1, "succeeded", string(audit)); err != nil {
		return refused, err
	}
	if err = tx.Commit(); err != nil {
		return refused, err
	}
	result.Publications = len(source.Publications)
	return result, nil
}
