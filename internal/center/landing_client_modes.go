package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

type LandingClientModeView struct {
	ParentID string                 `json:"parentId"`
	Mode     landing.PublishingMode `json:"mode"`
	Revision uint64                 `json:"revision"`
}

type LandingClientModeInput struct {
	LandingClientModeView
	ConfirmSessionReset bool `json:"confirmSessionReset"`
}

func (s *Store) LandingClientMode(ctx context.Context, parentID string) (LandingClientModeView, error) {
	view := LandingClientModeView{ParentID: parentID}
	err := s.db.QueryRowContext(ctx, `SELECT mode,revision FROM three_x_ui_client_accounts WHERE id=?`, parentID).Scan(&view.Mode, &view.Revision)
	return view, err
}

func (s *Store) ConfigureLandingClientMode(ctx context.Context, input LandingClientModeInput) (LandingClientModeView, error) {
	if input.ParentID == "" || input.Revision == 0 || !input.Mode.Valid() || !input.ConfirmSessionReset {
		return LandingClientModeView{}, errors.New("center: select a publishing mode and confirm the affected entry instances")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LandingClientModeView{}, err
	}
	defer tx.Rollback()
	var controllerID string
	var revision uint64
	if err := tx.QueryRowContext(ctx, `SELECT controller_id,revision FROM three_x_ui_client_accounts WHERE id=?`, input.ParentID).Scan(&controllerID, &revision); err != nil {
		return LandingClientModeView{}, err
	}
	controller, nodeID, err := runningGlobalThreeXUIController(ctx, tx)
	if err != nil || controller != controllerID || revision != input.Revision {
		return LandingClientModeView{}, errors.New("center: client changed; refresh and retry")
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1)`, nodeID).Scan(&busy); err != nil {
		return LandingClientModeView{}, err
	}
	if busy > 0 {
		return LandingClientModeView{}, errors.New("center: wait for the current client operation to finish")
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM landing_client_grants WHERE parent_id=? AND status<>'revoked' ORDER BY application_id,id`, input.ParentID)
	if err != nil {
		return LandingClientModeView{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return LandingClientModeView{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return LandingClientModeView{}, err
	}
	records := []landingGrantRecord{}
	for _, id := range ids {
		record, err := readLandingGrant(ctx, tx, id)
		if err != nil {
			return LandingClientModeView{}, err
		}
		if record.Status != "ready" && record.Status != "failed" {
			return LandingClientModeView{}, errors.New("center: wait for landing changes to finish")
		}
		if !record.Enabled {
			return LandingClientModeView{}, errors.New("center: finish pending revocations first")
		}
		record.Revision++
		record.Status = "preparing"
		records = append(records, record)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_client_accounts SET mode=?,revision=revision+1 WHERE id=? AND revision=?`, input.Mode, input.ParentID, input.Revision); err != nil {
		return LandingClientModeView{}, err
	}
	for _, record := range records {
		if _, err := tx.ExecContext(ctx, `UPDATE landing_client_grants SET desired_revision=?,status='preparing',last_error='',updated_at=? WHERE id=?`, record.Revision, s.now().UTC().Format(time.RFC3339Nano), record.ID); err != nil {
			return LandingClientModeView{}, err
		}
		if err := s.queueLandingClientCommand(ctx, tx, record, "prepare"); err != nil {
			return LandingClientModeView{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return LandingClientModeView{}, err
	}
	return LandingClientModeView{ParentID: input.ParentID, Mode: input.Mode, Revision: input.Revision + 1}, nil
}

func (s *Server) handleLandingClientMode(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		view, err := s.store.LandingClientMode(r.Context(), r.URL.Query().Get("parentId"))
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, errors.New("client not found"))
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, view)
		return
	}
	var input LandingClientModeInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	view, err := s.store.ConfigureLandingClientMode(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}

// Confirm active identity ownership before parent metadata can be used for a
// newly requested grant; a cached row never supplies an authentication secret.
func landingAccountMetadata(raw []byte, id string) (ThreeXUIClientView, error) {
	var client ThreeXUIClientView
	if json.Unmarshal(raw, &client) != nil || client.ID != id {
		return client, errors.New("center: invalid client identity")
	}
	return client, nil
}
