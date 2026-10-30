package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/petauron/vastora/internal/secret"

	"github.com/petauron/vastora/internal/nodesite"
)

const nodeSiteSchema = `CREATE TABLE node_sites (
 application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 hostname TEXT NOT NULL UNIQUE,
 port INTEGER NOT NULL CHECK(port BETWEEN 1024 AND 65535),
 revision INTEGER NOT NULL CHECK(revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL CHECK(state IN ('pending','issuing','ready','action_required','rolled_back')),
 sealed_key BLOB NOT NULL DEFAULT X'',
 certificate_pem TEXT NOT NULL DEFAULT '',
 fingerprint TEXT NOT NULL DEFAULT '',
 not_after TEXT NOT NULL DEFAULT '',
 ca_directory TEXT NOT NULL,
 error TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);
CREATE TABLE node_site_tasks (
 id TEXT PRIMARY KEY,
 application_id TEXT NOT NULL REFERENCES node_sites(application_id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL,
 input_json TEXT NOT NULL CHECK(json_valid(input_json)),
 state TEXT NOT NULL CHECK(state IN ('pending','running','succeeded','failed')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT '{}',
 updated_at TEXT NOT NULL
);
CREATE TABLE node_site_certmagic_storage (
 key TEXT PRIMARY KEY,
 sealed BLOB NOT NULL,
 modified_at TEXT NOT NULL,
 size INTEGER NOT NULL
);`

type NodeSiteView struct {
	ApplicationID   string `json:"applicationId"`
	Hostname        string `json:"hostname"`
	Port            int    `json:"port"`
	Revision        int64  `json:"revision"`
	AppliedRevision int64  `json:"appliedRevision"`
	State           string `json:"state"`
	Fingerprint     string `json:"fingerprint,omitempty"`
	NotAfter        string `json:"notAfter,omitempty"`
	Error           string `json:"error,omitempty"`
	CADirectory     string `json:"caDirectory,omitempty"`
}

type nodeSiteInput struct {
	Hostname    string `json:"hostname"`
	Port        int    `json:"port"`
	CADirectory string `json:"caDirectory"`
}

func (s *Store) NodeSite(ctx context.Context, id string) (NodeSiteView, error) {
	v := NodeSiteView{ApplicationID: id, State: "unconfigured"}
	err := s.db.QueryRowContext(ctx, `SELECT n.hostname,n.port,n.revision,n.applied_revision,CASE WHEN a.status='running' THEN n.state ELSE 'action_required' END,n.fingerprint,n.not_after,CASE WHEN a.status='running' THEN n.error ELSE 'Daylight application is not running' END,n.ca_directory FROM node_sites n JOIN applications a ON a.id=n.application_id WHERE n.application_id=?`, id).Scan(&v.Hostname, &v.Port, &v.Revision, &v.AppliedRevision, &v.State, &v.Fingerprint, &v.NotAfter, &v.Error, &v.CADirectory)
	if errors.Is(err, sql.ErrNoRows) {
		// Read the already installed identity without issuing anything on a GET.
		err = s.db.QueryRowContext(ctx, `SELECT COALESCE(json_extract(d.config_json,'$.hostname'),'') FROM applications a JOIN deployments d ON d.application_id=a.id WHERE a.id=? AND a.app_key IN ('vastora-official/daylight','vastora-official/daylight-https') AND d.state='succeeded' AND d.operation<>'uninstall' ORDER BY d.updated_at DESC LIMIT 1`, id).Scan(&v.Hostname)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
	}
	return v, err
}

func (s *Server) handleNodeSite(w http.ResponseWriter, r *http.Request) {
	v, err := s.store.NodeSite(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 400, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) handleConfigureNodeSite(w http.ResponseWriter, r *http.Request) {
	var input nodeSiteInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	v, err := s.store.configureNodeSite(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	writeJSON(w, 202, v)
}
func (s *Server) handleNodeSiteAction(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, err)
		return
	}
	if input.Action != "inspect" && input.Action != "rollback" && input.Action != "cancel" {
		writeError(w, 400, errors.New("center: invalid node site action"))
		return
	}
	id := r.PathValue("id")
	tx, err := s.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	defer tx.Rollback()
	if input.Action == "cancel" {
		var blocked bool
		err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM node_site_tasks WHERE application_id=? AND state='running') OR EXISTS(SELECT 1 FROM task_executions WHERE agent_id=(SELECT agent_id FROM node_sites WHERE application_id=?) AND disposition='' AND state IN ('offered','running','helper_running','unknown','failed'))`, id, id).Scan(&blocked)
		if err == nil && blocked {
			err = errors.New("center: review the active execution before cancelling")
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `UPDATE node_site_tasks SET state='failed',error='Cancelled before delivery' WHERE application_id=? AND state='pending'`, id)
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `UPDATE node_sites SET state='action_required',error='Cancelled by administrator' WHERE application_id=?`, id)
		}
	} else {
		err = s.store.queueNodeSitePhase(r.Context(), tx, id, input.Action)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, 400, err)
		return
	}
	v, err := s.store.NodeSite(r.Context(), id)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	writeJSON(w, 202, v)
}

func (s *Store) configureNodeSite(ctx context.Context, id string, in nodeSiteInput) (NodeSiteView, error) {
	if !nodesite.ValidHostname(in.Hostname) || in.Port < 1024 || in.Port > 65535 {
		return NodeSiteView{}, errors.New("center: exact hostname and high loopback TLS port are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NodeSiteView{}, err
	}
	defer tx.Rollback()
	var agentID, appKey string
	var configuredHostname string
	err = tx.QueryRowContext(ctx, `SELECT a.node_id,a.app_key,COALESCE(json_extract(d.config_json,'$.hostname'),'') FROM applications a JOIN deployments d ON d.application_id=a.id WHERE a.id=? AND a.status='running' AND d.state='succeeded' AND d.operation<>'uninstall' ORDER BY d.updated_at DESC LIMIT 1`, id).Scan(&agentID, &appKey, &configuredHostname)
	if err != nil || configuredHostname != in.Hostname || (appKey != "vastora-official/daylight" && appKey != "vastora-official/daylight-https") {
		return NodeSiteView{}, errors.New("center: install Daylight with this exact hostname first")
	}
	if err = nodeSiteIdle(ctx, tx, id, agentID); err != nil {
		return NodeSiteView{}, err
	}
	directory := in.CADirectory
	if directory == "" {
		directory = certmagic.LetsEncryptProductionCA
	}
	if _, err = nodeSiteDirectory(directory); err != nil {
		return NodeSiteView{}, err
	}
	var previousHost, previousDirectory, previousState, previousExpiry, previousChain string
	var previousPort int
	var previousKey []byte
	existingErr := tx.QueryRowContext(ctx, `SELECT hostname,port,ca_directory,state,not_after,certificate_pem,sealed_key FROM node_sites WHERE application_id=?`, id).Scan(&previousHost, &previousPort, &previousDirectory, &previousState, &previousExpiry, &previousChain, &previousKey)
	if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
		return NodeSiteView{}, existingErr
	}
	sameBinding := existingErr == nil && previousHost == in.Hostname && previousDirectory == directory
	expiry, expiryErr := time.Parse(time.RFC3339Nano, previousExpiry)
	if sameBinding && previousPort == in.Port && (previousState == "ready" || previousState == "issuing") {
		if err = tx.Commit(); err != nil {
			return NodeSiteView{}, err
		}
		return s.NodeSite(ctx, id)
	}
	// Explicit retries and port-only changes reuse the managed pair. CertMagic,
	// rather than this API, decides whether that pair also needs renewal.
	if sameBinding && expiryErr == nil && expiry.After(s.now()) && previousChain != "" && len(previousKey) > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE node_sites SET port=?,revision=revision+1,state='pending',error='',updated_at=? WHERE application_id=?`, in.Port, s.now().UTC().Format(time.RFC3339Nano), id); err != nil {
			return NodeSiteView{}, err
		}
		if err = s.queueNodeSitePhase(ctx, tx, id, "apply"); err != nil {
			return NodeSiteView{}, err
		}
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO node_sites(application_id,agent_id,hostname,port,revision,state,ca_directory,updated_at) VALUES(?,?,?,?,1,'issuing',?,?) ON CONFLICT(application_id) DO UPDATE SET agent_id=excluded.agent_id,hostname=excluded.hostname,port=excluded.port,revision=node_sites.revision+1,state='issuing',sealed_key=X'',certificate_pem='',fingerprint='',not_after='',error='',ca_directory=excluded.ca_directory,updated_at=excluded.updated_at`, id, agentID, in.Hostname, in.Port, directory, s.now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return NodeSiteView{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return NodeSiteView{}, err
	}
	return s.NodeSite(ctx, id)
}

func nodeSiteIdle(ctx context.Context, tx *sql.Tx, id, agentID string) error {
	var busy bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_site_tasks WHERE application_id=? AND state IN ('pending','running')) OR EXISTS(SELECT 1 FROM deployments WHERE application_id=? AND state IN ('pending','running')) OR EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state IN ('offered','running','helper_running','unknown','failed'))`, id, id, agentID).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return errors.New("center: resolve the existing node operation first")
	}
	return nil
}

// Stored commands deliberately omit all key material; execution authorization
// seals the hydrated task only after the authenticated Agent claims it.
type nodeSiteCommand struct {
	Action string `json:"action"`
}

func (s *Store) nodeSiteTask(ctx context.Context, tx *sql.Tx, id, action string) (nodesite.Task, string, error) {
	var t nodesite.Task
	var agentID string
	var sealed []byte
	err := tx.QueryRowContext(ctx, `SELECT n.application_id,a.app_key,n.agent_id,n.hostname,n.port,n.revision,n.certificate_pem,n.sealed_key FROM node_sites n JOIN applications a ON a.id=n.application_id WHERE n.application_id=? AND a.node_id=n.agent_id AND a.status='running'`, id).Scan(&t.ApplicationID, &t.AppKey, &agentID, &t.Hostname, &t.Port, &t.Revision, &t.CertificatePEM, &sealed)
	if err != nil {
		return t, "", err
	}
	t.Action = action
	if action == "apply" {
		key, e := secret.Open(s.key, sealed, []byte("node-site-certificate:"+id))
		if e != nil {
			return t, "", errors.New("center: managed node certificate key is unavailable")
		}
		t.PrivateKeyPEM = string(key)
	} else {
		t.CertificatePEM = ""
	}
	return t, agentID, t.Validate()
}
func (s *Store) queueNodeSitePhase(ctx context.Context, tx *sql.Tx, id, action string) error {
	t, agentID, err := s.nodeSiteTask(ctx, tx, id, action)
	if err != nil {
		return err
	}
	if err = nodeSiteIdle(ctx, tx, id, agentID); err != nil {
		return err
	}
	token, err := randomToken(18)
	if err != nil {
		return err
	}
	taskID := nodesite.TaskPrefix + token
	raw, _ := json.Marshal(nodeSiteCommand{Action: action})
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO node_site_tasks(id,application_id,agent_id,revision,input_json,state,updated_at) VALUES(?,?,?,?,?,'pending',?)`, taskID, id, agentID, t.Revision, string(raw), now)
	if err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, taskID, agentID, nodesite.Kind, t.Revision, "queued", action)
}
func (s *Store) claimNodeSite(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var id, app, raw string
	var attempt, revision int64
	err := tx.QueryRowContext(ctx, `SELECT t.id,t.application_id,t.input_json,t.attempt,t.revision FROM node_site_tasks t JOIN node_sites n ON n.application_id=t.application_id JOIN applications a ON a.id=n.application_id WHERE t.agent_id=? AND t.state='pending' AND t.revision=n.revision AND a.node_id=t.agent_id AND n.agent_id=t.agent_id AND a.status='running' ORDER BY t.updated_at LIMIT 1`, agentID).Scan(&id, &app, &raw, &attempt, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var command nodeSiteCommand
	if json.Unmarshal([]byte(raw), &command) != nil {
		return nil, errors.New("center: invalid saved node site command")
	}
	t, owner, err := s.nodeSiteTask(ctx, tx, app, command.Action)
	if err != nil {
		return nil, err
	}
	if owner != agentID || t.Revision != revision {
		return nil, errors.New("center: stale node site binding")
	}
	now := s.now().UTC()
	updated, err := tx.ExecContext(ctx, `UPDATE node_site_tasks SET state='running',attempt=attempt+1,lease_expires_at=?,updated_at=? WHERE id=? AND state='pending' AND attempt=?`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, attempt)
	if err != nil {
		return nil, err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return nil, errStaleTaskLease
	}
	return &AgentTask{ID: id, Kind: nodesite.Kind, Attempt: attempt + 1, Revision: t.Revision, ApplicationID: t.ApplicationID, NodeSite: &t}, nil
}

func (s *Store) completeNodeSite(ctx context.Context, commit projectionCommit, agentID, id string, attempt int64, succeeded bool, taskError string, raw json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return s.projectNodeSite(ctx, tx, commit, agentID, id, attempt, succeeded, taskError, raw)
}
func (s *Store) projectNodeSite(ctx context.Context, tx *sql.Tx, commit projectionCommit, agentID, id string, attempt int64, succeeded bool, taskError string, raw json.RawMessage) error {
	var err error
	var encoded, state, app string
	if err = tx.QueryRowContext(ctx, `SELECT t.input_json,t.state,t.application_id FROM node_site_tasks t JOIN node_sites n ON n.application_id=t.application_id JOIN applications a ON a.id=n.application_id WHERE t.id=? AND t.agent_id=? AND t.attempt=? AND n.revision=t.revision AND n.agent_id=t.agent_id AND a.node_id=t.agent_id AND a.status='running'`, id, agentID, attempt).Scan(&encoded, &state, &app); err != nil {
		return err
	}
	var command nodeSiteCommand
	if json.Unmarshal([]byte(encoded), &command) != nil {
		return errors.New("center: invalid saved node site command")
	}
	t, owner, err := s.nodeSiteTask(ctx, tx, app, command.Action)
	if err != nil {
		return err
	}
	if owner != agentID {
		return errors.New("center: node site recipient changed")
	}

	if (state == "succeeded" && succeeded) || (state == "failed" && !succeeded) {
		return commit(tx)
	}
	if state != "running" {
		return errors.New("center: stale node site result")
	}
	var envelope struct {
		NodeSite *nodesite.Result `json:"nodeSite"`
	}
	if succeeded && (json.Unmarshal(raw, &envelope) != nil || envelope.NodeSite == nil || envelope.NodeSite.Revision != t.Revision || envelope.NodeSite.Hostname != t.Hostname || envelope.NodeSite.Action != t.Action) {
		succeeded = false
		taskError = "Agent did not confirm this node site revision"
	}
	nextState := "action_required"
	taskState := "failed"
	now := s.now().UTC().Format(time.RFC3339Nano)
	if succeeded {
		v := envelope.NodeSite
		switch t.Action {
		case "apply", "inspect":
			var expected string
			if err = tx.QueryRowContext(ctx, `SELECT fingerprint FROM node_sites WHERE application_id=? AND revision=?`, t.ApplicationID, t.Revision).Scan(&expected); err != nil {
				return err
			}
			if v.State != "applied" || v.Fingerprint == "" || v.Fingerprint != expected {
				succeeded = false
				taskError = "Agent certificate does not match desired state"
				break
			}
			nextState = "ready"
			if _, err = tx.ExecContext(ctx, `UPDATE node_sites SET applied_revision=revision WHERE application_id=? AND revision=?`, t.ApplicationID, t.Revision); err != nil {
				return err
			}
		case "rollback":
			if v.State != "rolled_back" {
				return errors.New("center: Agent did not confirm rollback")
			}
			nextState = "rolled_back"
		}
		if succeeded {
			taskState = "succeeded"
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE node_site_tasks SET state=?,error=?,result_json=?,lease_expires_at='',updated_at=? WHERE id=?`, taskState, taskError, string(nodeSitePublicResult(envelope.NodeSite)), now, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE node_sites SET state=?,error=?,updated_at=? WHERE application_id=? AND agent_id=? AND revision=?`, nextState, taskError, now, t.ApplicationID, agentID, t.Revision); err != nil {
		return err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, nodesite.Kind, t.Revision, taskState, ""); err != nil {
		return err
	}
	return commit(tx)
}

func nodeSitePublicResult(result *nodesite.Result) []byte {
	if result == nil {
		return []byte(`{}`)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}
