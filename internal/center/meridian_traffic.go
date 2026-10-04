package center

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"
)

// These observational totals never participate in quota enforcement or runtime
// projection. The existing authenticated receipt transaction owns both ledgers.
func observeMeridianLineUsage(ctx context.Context, tx *sql.Tx, credential string, up, down int64, stamp string) error {
	var previousUp, previousDown int64
	err := tx.QueryRowContext(ctx, `SELECT upload_bytes,download_bytes FROM meridian_line_usage WHERE credential_id=?`, credential).Scan(&previousUp, &previousDown)
	if errors.Is(err, sql.ErrNoRows) {
		// The first sample is a baseline, including for existing credentials. Its
		// process counters may contain days of untracked historical usage.
		_, err = tx.ExecContext(ctx, `INSERT INTO meridian_line_usage(credential_id,started_at,observed_at) VALUES(?,?,?)`, credential, stamp, stamp)
		return err
	}
	if err != nil {
		return err
	}
	if up < 0 || down < 0 {
		return errors.New("center: invalid directional usage delta")
	}
	_, err = tx.ExecContext(ctx, `UPDATE meridian_line_usage SET upload_bytes=?,download_bytes=?,observed_at=? WHERE credential_id=?`, saturatingAdd(previousUp, up), saturatingAdd(previousDown, down), stamp, credential)
	return err
}

type MeridianLineUsage struct {
	EntryNodeID        string `json:"entryNodeId"`
	EntryName          string `json:"entryName"`
	EgressNodeID       string `json:"egressNodeId,omitempty"`
	EgressName         string `json:"egressName,omitempty"`
	UploadBytes        int64  `json:"uploadBytes"`
	DownloadBytes      int64  `json:"downloadBytes"`
	TotalBytes         int64  `json:"totalBytes"`
	CredentialCount    int    `json:"credentialCount"`
	TrackedCredentials int    `json:"trackedCredentials"`
	StartedAt          string `json:"startedAt,omitempty"`
	ObservedAt         string `json:"observedAt,omitempty"`
	State              string `json:"state"`
}

type MeridianTrafficView struct {
	Lines     []MeridianLineUsage `json:"lines"`
	Period    string              `json:"period"`
	CheckedAt string              `json:"checkedAt"`
}

// A single read snapshot groups all credentials (including revoked credentials
// whose history is retained) by their actual entry node and egress choice.
// No network operation, reset, task, or quota mutation occurs on this path.
func (s *Store) MeridianTraffic(ctx context.Context) (MeridianTrafficView, error) {
	now := s.now().UTC()
	view := MeridianTrafficView{Lines: []MeridianLineUsage{}, Period: "since_tracking_started", CheckedAt: now.Format(time.RFC3339Nano)}
	rows, err := s.db.QueryContext(ctx, `SELECT app.node_id,entry.name,COALESCE(credential.egress_node_id,''),COALESCE(egress.name,''),
 usage.upload_bytes,usage.download_bytes,usage.started_at,usage.observed_at,credential.enabled
 FROM meridian_credentials credential
 JOIN meridian_endpoints endpoint ON endpoint.id=credential.endpoint_id
 JOIN applications app ON app.id=endpoint.application_id
 JOIN agents entry ON entry.id=app.node_id
 LEFT JOIN agents egress ON egress.id=credential.egress_node_id
 LEFT JOIN meridian_line_usage usage ON usage.credential_id=credential.id
 WHERE endpoint.status<>'retired'
 ORDER BY entry.name,app.node_id,credential.egress_node_id,endpoint.id,credential.id`)
	if err != nil {
		return view, err
	}
	defer rows.Close()
	type aggregate struct {
		line         MeridianLineUsage
		latest       time.Time
		oldestActive time.Time
		missing      bool
	}
	grouped := map[string]*aggregate{}
	for rows.Next() {
		var line MeridianLineUsage
		var up, down sql.NullInt64
		var start, observed sql.NullString
		var enabled bool
		if err := rows.Scan(&line.EntryNodeID, &line.EntryName, &line.EgressNodeID, &line.EgressName, &up, &down, &start, &observed, &enabled); err != nil {
			return view, err
		}
		key := line.EntryNodeID + "/" + line.EgressNodeID
		item := grouped[key]
		if item == nil {
			item = &aggregate{line: line}
			grouped[key] = item
		}
		item.line.CredentialCount++
		if !up.Valid || !down.Valid || !start.Valid || !observed.Valid {
			item.missing = true
			continue
		}
		started, err := time.Parse(time.RFC3339Nano, start.String)
		if err != nil {
			return view, err
		}
		checked, err := time.Parse(time.RFC3339Nano, observed.String)
		if err != nil {
			return view, err
		}
		item.line.TrackedCredentials++
		item.line.UploadBytes = saturatingAdd(item.line.UploadBytes, up.Int64)
		item.line.DownloadBytes = saturatingAdd(item.line.DownloadBytes, down.Int64)
		if item.line.StartedAt == "" {
			item.line.StartedAt = start.String
		} else {
			previous, _ := time.Parse(time.RFC3339Nano, item.line.StartedAt)
			if started.Before(previous) {
				item.line.StartedAt = start.String
			}
		}
		if checked.After(item.latest) {
			item.latest = checked
			item.line.ObservedAt = observed.String
		}
		if enabled && (item.oldestActive.IsZero() || checked.Before(item.oldestActive)) {
			item.oldestActive = checked
		}
	}
	if err := rows.Err(); err != nil {
		return view, err
	}
	for _, item := range grouped {
		item.line.TotalBytes = saturatingAdd(item.line.UploadBytes, item.line.DownloadBytes)
		item.line.State = "current"
		if item.line.TrackedCredentials == 0 {
			item.line.State = "missing"
		} else if item.missing {
			item.line.State = "partial"
		} else if item.oldestActive.IsZero() || now.Sub(item.oldestActive) > 15*time.Minute || item.oldestActive.After(now.Add(time.Minute)) {
			item.line.State = "stale"
		}
		view.Lines = append(view.Lines, item.line)
	}
	sort.Slice(view.Lines, func(i, j int) bool {
		a, b := view.Lines[i], view.Lines[j]
		if a.EntryName != b.EntryName {
			return a.EntryName < b.EntryName
		}
		if a.EntryNodeID != b.EntryNodeID {
			return a.EntryNodeID < b.EntryNodeID
		}
		return a.EgressNodeID < b.EgressNodeID
	})
	return view, nil
}
