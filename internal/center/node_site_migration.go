package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/nodesite"
)

// The previous key remains sealed with its existing application-bound AAD.
// This recovery record is never included in a public API view or task event.
type previousNodeSiteBinding struct {
	Hostname, Directory, Chain, Fingerprint, NotAfter string
	Port                                              int
	Key                                               []byte
}

func (s *Store) validateWebsiteHostnameConfigure(ctx context.Context, request DeploymentRequest, manifest catalog.AppManifest) (bool, error) {
	if request.AppKey != "vastora-official/daylight" && request.AppKey != "vastora-official/daylight-https" {
		return false, nil
	}
	if manifest.Runtime == nil || (manifest.Runtime.Kind != "native" && manifest.Runtime.Kind != "systemd") || !slices.Contains(manifest.Runtime.RequiredCapabilities, "shared-caddy") {
		return false, nil
	}
	var values map[string]json.RawMessage
	var target string
	if json.Unmarshal(request.Config, &values) != nil || len(values) != 1 || json.Unmarshal(values["hostname"], &target) != nil {
		return false, nil
	}
	var from, host, state string
	var revision, applied int64
	err := s.db.QueryRowContext(ctx, `SELECT n.migration_from,n.hostname,n.state,n.revision,n.applied_revision FROM node_sites n JOIN applications a ON a.id=n.application_id WHERE a.node_id=? AND a.app_key=? AND a.status='running'`, request.AgentID, request.AppKey).Scan(&from, &host, &state, &revision, &applied)
	if err != nil || from == "" || (target != host && target != from) || state != "ready" || revision != applied {
		return true, errors.New("center: prepare and verify the website hostname migration first")
	}
	return true, nil
}

func installedWebsiteHostname(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var hostname string
	err := tx.QueryRowContext(ctx, `SELECT json_extract(d.config_json,'$.hostname') FROM deployments d JOIN applications a ON a.id=d.application_id WHERE a.id=? AND a.app_key IN ('vastora-official/daylight','vastora-official/daylight-https') AND a.status='running' AND d.state='succeeded' AND d.operation IN ('install','upgrade','configure') ORDER BY d.updated_at DESC,d.rowid DESC LIMIT 1`, id).Scan(&hostname)
	return hostname, err
}

func (s *Store) prepareNodeSiteMigration(ctx context.Context, id string, in nodeSiteInput) (NodeSiteView, error) {
	invalid := errors.New("center: website migration requires the exact installed hostname and a ready managed certificate")
	if !nodesite.ValidHostname(in.Hostname) || !nodesite.ValidHostname(in.PreviousHostname) || in.Hostname == in.PreviousHostname || in.Port < 1024 || in.Port > 65535 {
		return NodeSiteView{}, invalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NodeSiteView{}, err
	}
	defer tx.Rollback()
	installed, err := installedWebsiteHostname(ctx, tx, id)
	if err != nil || installed != in.PreviousHostname {
		return NodeSiteView{}, invalid
	}
	var previous previousNodeSiteBinding
	var agent, state, from string
	var revision, applied int64
	err = tx.QueryRowContext(ctx, `SELECT agent_id,hostname,port,ca_directory,certificate_pem,sealed_key,fingerprint,not_after,state,revision,applied_revision,migration_from FROM node_sites WHERE application_id=?`, id).Scan(&agent, &previous.Hostname, &previous.Port, &previous.Directory, &previous.Chain, &previous.Key, &previous.Fingerprint, &previous.NotAfter, &state, &revision, &applied, &from)
	if err != nil {
		return NodeSiteView{}, invalid
	}
	if from != "" {
		if from != in.PreviousHostname || previous.Hostname != in.Hostname || previous.Port != in.Port {
			return NodeSiteView{}, invalid
		}
		if err = tx.Commit(); err != nil {
			return NodeSiteView{}, err
		}
		return s.NodeSite(ctx, id)
	}
	if state != "ready" || revision != applied || previous.Hostname != in.PreviousHostname || previous.Chain == "" || len(previous.Key) == 0 {
		return NodeSiteView{}, invalid
	}
	if err = nodeSiteIdle(ctx, tx, id, agent); err != nil {
		return NodeSiteView{}, err
	}
	directory := in.CADirectory
	if directory == "" {
		directory = previous.Directory
	}
	if _, err = nodeSiteDirectory(directory); err != nil {
		return NodeSiteView{}, err
	}
	recovery, err := json.Marshal(previous)
	if err != nil {
		return NodeSiteView{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE node_sites SET hostname=?,port=?,ca_directory=?,revision=revision+1,state='issuing',migration_from=?,migration_previous=?,sealed_key=X'',certificate_pem='',fingerprint='',not_after='',error='',updated_at=? WHERE application_id=? AND migration_from='' AND revision=?`, in.Hostname, in.Port, directory, in.PreviousHostname, recovery, s.now().UTC().Format(time.RFC3339Nano), id, revision)
	if err != nil {
		return NodeSiteView{}, err
	}
	if err = tx.Commit(); err != nil {
		return NodeSiteView{}, err
	}
	return s.NodeSite(ctx, id)
}

func (s *Store) validateNodeSiteMigrationAction(ctx context.Context, tx *sql.Tx, id, action string) error {
	var from, host, agent, state string
	if err := tx.QueryRowContext(ctx, `SELECT migration_from,hostname,agent_id,state FROM node_sites WHERE application_id=?`, id).Scan(&from, &host, &agent, &state); err != nil {
		return err
	}
	completion := action == "accept-migration" || action == "cancel-migration"
	if from == "" {
		if completion {
			return errors.New("center: no website migration is pending")
		}
		return nil
	}
	if action == "inspect" {
		return nil
	}
	if !completion {
		return errors.New("center: use the explicit website migration action")
	}
	installed, err := installedWebsiteHostname(ctx, tx, id)
	if err != nil {
		return err
	}
	if action == "accept-migration" && (installed != host || state != "ready") || action == "cancel-migration" && installed != from {
		return errors.New("center: verify the installed website binding before completing migration")
	}
	return nodeSiteIdle(ctx, tx, id, agent)
}

func (s *Store) finishNodeSiteMigration(ctx context.Context, tx *sql.Tx, id, action, fingerprint string) (string, error) {
	if action == "accept-migration" {
		var expected string
		if err := tx.QueryRowContext(ctx, `SELECT fingerprint FROM node_sites WHERE application_id=?`, id).Scan(&expected); err != nil {
			return "", err
		}
		if fingerprint == "" || fingerprint != expected {
			return "", errors.New("center: accepted website certificate differs from the prepared binding")
		}
		_, err := tx.ExecContext(ctx, `UPDATE node_sites SET migration_from='',migration_previous=X'' WHERE application_id=? AND migration_from<>''`, id)
		return "ready", err
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT migration_previous FROM node_sites WHERE application_id=? AND migration_from<>''`, id).Scan(&raw); err != nil {
		return "", err
	}
	var old previousNodeSiteBinding
	if json.Unmarshal(raw, &old) != nil || !nodesite.ValidHostname(old.Hostname) || len(old.Key) == 0 || old.Chain == "" || fingerprint == "" || old.Fingerprint != fingerprint {
		return "", errors.New("center: original website certificate recovery is unavailable")
	}
	_, err := tx.ExecContext(ctx, `UPDATE node_sites SET hostname=?,port=?,ca_directory=?,certificate_pem=?,sealed_key=?,fingerprint=?,not_after=?,applied_revision=revision,state='ready',migration_from='',migration_previous=X'' WHERE application_id=?`, old.Hostname, old.Port, old.Directory, old.Chain, old.Key, old.Fingerprint, old.NotAfter, id)
	return "ready", err
}
