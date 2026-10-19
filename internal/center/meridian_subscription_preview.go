package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/petauron/glivela"
)

// Explicit allowlist DTOs: never serialize subscription/runtime credential structs.
type MeridianSubscriptionItem struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
}

type MeridianSubscriptionExclusion struct {
	EndpointID string `json:"endpointId"`
	RouteID    string `json:"routeId,omitempty"`
	Name       string `json:"name"`
	Protocol   string `json:"protocol,omitempty"`
	Reason     string `json:"reason"`
}

type MeridianSubscriptionPreview struct {
	AccountID       string                          `json:"accountId"`
	Source          string                          `json:"source"`
	Reason          string                          `json:"reason,omitempty"`
	AppliedRevision uint64                          `json:"appliedRevision"`
	CheckedAt       time.Time                       `json:"checkedAt"`
	Fingerprint     string                          `json:"fingerprint"`
	Count           int                             `json:"count"`
	Items           []MeridianSubscriptionItem      `json:"items"`
	Excluded        []MeridianSubscriptionExclusion `json:"excluded"`
}

type subscriptionDecisionsKey struct{}

// This is an observer of the actual selection path, never an alternate filter.
func recordSubscriptionExclusion(ctx context.Context, issue MeridianSubscriptionExclusion) {
	if sink, ok := ctx.Value(subscriptionDecisionsKey{}).(*[]MeridianSubscriptionExclusion); ok {
		*sink = append(*sink, issue)
	}
}

func (s *Store) MeridianSubscriptionPreview(ctx context.Context, accountID string) (MeridianSubscriptionPreview, error) {
	view := MeridianSubscriptionPreview{AccountID: accountID, Source: "unavailable", CheckedAt: s.now().UTC(), Items: []MeridianSubscriptionItem{}, Excluded: []MeridianSubscriptionExclusion{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return view, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_accounts WHERE id=?)`, accountID).Scan(&exists); err != nil {
		return view, err
	}
	if !exists {
		return view, sql.ErrNoRows
	}
	issues := []MeridianSubscriptionExclusion{}
	ctx = context.WithValue(ctx, subscriptionDecisionsKey{}, &issues)
	result, err := s.resolveMeridianSubscriptionInTx(ctx, tx, accountID, false)
	view.AppliedRevision = result.Account.AppliedRevision
	if err != nil && !errors.Is(err, errMeridianSubscriptionNotFound) {
		return view, errors.New("center: subscription preview unavailable")
	}
	if err != nil {
		view.Reason = result.Reason
		if view.Reason == "" {
			view.Reason = "subscription_unavailable"
		}
	} else {
		// Decode the real ordinary renderer output; no preview-only selection or
		// naming rules. Both Glivela encoders consume that same resolved inventory.
		rendered, err := glivela.RenderLinks(result.Entries, accountID, glivela.FixedMode, result.Routes, false)
		if err != nil {
			return view, errors.New("center: subscription preview unavailable")
		}
		view.Source = result.Source
		lines := []string{}
		if text := strings.TrimSpace(string(rendered)); text != "" {
			lines = strings.Split(text, "\n")
		}
		for _, line := range lines {
			parsed, err := url.Parse(line)
			if err != nil || parsed.Fragment == "" {
				return view, errors.New("center: subscription preview unavailable")
			}
			protocol := string(glivela.VLESSReality)
			if parsed.Scheme == "hysteria2" || parsed.Scheme == "hy2" {
				protocol = string(glivela.Hysteria2)
			}
			view.Items = append(view.Items, MeridianSubscriptionItem{Name: parsed.Fragment, Protocol: protocol})
		}
		for _, entry := range result.Entries {
			// Only the display fragment is rewritten by the renderer for native
			// entries. Compare the complete remaining URI, including protocol and all
			// transport parameters; a routed/combined identity cannot match this.
			native, _, _ := strings.Cut(strings.TrimSpace(entry.Link), "#")
			present := false
			for _, line := range lines {
				output, _, _ := strings.Cut(line, "#")
				if output == native {
					present = true
					break
				}
			}
			if !present {
				recordSubscriptionNativeExclusion(ctx, tx, entry, "native_hidden")
			}
		}
	}
	// A live attempt may have rejected an entry later retained by the applied
	// snapshot. Do not present that rejection as a missing item in the final output.
	presentEndpoints, presentRoutes, presentProtocols := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, entry := range result.Entries {
		presentEndpoints[entry.Material.Credential.EntryID] = true
		presentProtocols[entry.Material.Credential.EntryID+"\x00"+string(entry.Protocol)] = true
	}
	for _, route := range result.Routes {
		presentRoutes[route.Grant.ID] = true
	}
	endpointApplications := map[string]string{}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT endpoint.id,endpoint.application_id FROM meridian_endpoints endpoint JOIN meridian_credentials credential ON credential.endpoint_id=endpoint.id WHERE credential.account_id=?`, accountID)
	if err != nil {
		return view, err
	}
	for rows.Next() {
		var id, app string
		if err := rows.Scan(&id, &app); err != nil {
			rows.Close()
			return view, err
		}
		endpointApplications[id] = app
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return view, err
	}
	rows.Close()
	seen := map[string]bool{}
	for _, issue := range issues {
		included := false
		switch {
		case issue.RouteID != "":
			included = presentRoutes[issue.RouteID]
		case issue.Protocol != "":
			included = presentProtocols[endpointApplications[issue.EndpointID]+"\x00"+issue.Protocol]
		default:
			included = presentEndpoints[endpointApplications[issue.EndpointID]]
		}
		if view.Reason == "" && included && issue.Reason != "native_hidden" {
			continue
		}
		key := issue.EndpointID + "\x00" + issue.RouteID + "\x00" + issue.Protocol + "\x00" + issue.Name + "\x00" + issue.Reason
		if !seen[key] {
			view.Excluded = append(view.Excluded, issue)
			seen[key] = true
		}
	}
	view.Count = len(view.Items)
	if view.Count == 0 && view.Reason == "" {
		view.Reason = "subscription_unavailable"
	}
	metadata, _ := json.Marshal(view.Items)
	digest := sha256.Sum256(metadata)
	view.Fingerprint = hex.EncodeToString(digest[:])
	return view, nil
}

func (s *Server) handleMeridianSubscriptionPreview(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	view, err := s.store.MeridianSubscriptionPreview(request.Context(), request.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, errors.New("center: account not found"))
		return
	}
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("center: subscription preview unavailable"))
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

// Snapshot removal is observed where it actually occurs. Resolve only display
// metadata; an absent historical endpoint never causes a credential disclosure.
func recordSubscriptionNativeExclusion(ctx context.Context, tx *sql.Tx, entry glivela.NativeEntry, reason string) {
	if _, ok := ctx.Value(subscriptionDecisionsKey{}).(*[]MeridianSubscriptionExclusion); !ok {
		return
	}
	issue := MeridianSubscriptionExclusion{Reason: reason, Name: "Native entry", Protocol: string(entry.Protocol)}
	if parsed, err := url.Parse(entry.Link); err == nil {
		issue.Name = parsed.Fragment
	}
	_ = tx.QueryRowContext(ctx, `SELECT id FROM meridian_endpoints WHERE application_id=? ORDER BY id LIMIT 1`, entry.Material.Credential.EntryID).Scan(&issue.EndpointID)
	recordSubscriptionExclusion(ctx, issue)
}
