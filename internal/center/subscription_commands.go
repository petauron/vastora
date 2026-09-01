package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func (s *Store) CreateSubscriptionCommand(ctx context.Context, input SubscriptionCommandInput) (ApplicationCommandView, error) {
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	input.GatewayNodeID = strings.TrimSpace(input.GatewayNodeID)
	input.Hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.Hostname), "."))
	input.Kind = strings.TrimSpace(input.Kind)
	input.DNSProvider = strings.TrimSpace(input.DNSProvider)
	if input.ApplicationID == "" || input.GatewayNodeID == "" {
		return ApplicationCommandView{}, errors.New("center: application and entry node are required")
	}
	if input.Hostname != "" && !domainSuffixPattern.MatchString(input.Hostname) {
		return ApplicationCommandView{}, errors.New("center: subscription hostname is invalid")
	}
	if input.Kind != publicationCloudflare && input.Kind != publicationPublic {
		return ApplicationCommandView{}, errors.New("center: public subscription must use Cloudflare Tunnel or direct public HTTPS")
	}
	if !validPublicationDNS(input.Kind, input.DNSProvider) {
		return ApplicationCommandView{}, errors.New("center: DNS provider is not valid for this subscription")
	}
	var agentID, appKey, status, role, serviceID string
	err := s.db.QueryRowContext(ctx, `SELECT a.node_id, a.app_key, a.status, a.role, s.id
		FROM applications a JOIN services s ON s.application_id = a.id AND s.name = 'subscription'
		WHERE a.id = ? AND s.status <> 'stopped'`, input.ApplicationID).Scan(&agentID, &appKey, &status, &role, &serviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, errors.New("center: running 3x-ui subscription service not found")
	}
	if err != nil {
		return ApplicationCommandView{}, err
	}
	if appKey != threeXUIAppKey || status != "running" || role != threeXUIRoleMaster {
		return ApplicationCommandView{}, errors.New("center: public subscription is available only on the running Site 3x-ui controller")
	}
	var active int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id = ? AND kind <> ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, agentID, controllerCommandKind).Scan(&active); err != nil {
		return ApplicationCommandView{}, err
	}
	if active != 0 {
		return ApplicationCommandView{}, errors.New("center: this 3x-ui application already has an operation in progress")
	}
	publication, err := s.CreatePublication(ctx, PublicationInput{ServiceID: serviceID, Kind: input.Kind, GatewayNodeID: input.GatewayNodeID, Hostname: input.Hostname, DNSProvider: input.DNSProvider})
	if err != nil {
		return ApplicationCommandView{}, err
	}
	baseURI := (&url.URL{Scheme: "https", Host: publication.Hostname, Path: "/sub/"}).String()
	task := SubscriptionCommandTask{Domain: publication.Hostname, BaseURI: baseURI, PublicationID: publication.ID}
	encoded, _ := json.Marshal(task)
	token, err := randomToken(18)
	if err != nil {
		_ = s.StopPublication(context.WithoutCancel(ctx), publication.ID)
		return ApplicationCommandView{}, err
	}
	id := "application-command-" + token
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		_ = s.StopPublication(context.WithoutCancel(ctx), publication.ID)
		return ApplicationCommandView{}, err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, id, input.ApplicationID, agentID, input.GatewayNodeID, subscriptionCommandKind, encoded, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		_ = s.StopPublication(context.WithoutCancel(ctx), publication.ID)
		return ApplicationCommandView{}, fmt.Errorf("center: create subscription operation: %w", err)
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, "application.command", 1, "queued", "3x-ui public subscription configuration queued"); err != nil {
		_ = tx.Rollback()
		_ = s.StopPublication(context.WithoutCancel(ctx), publication.ID)
		return ApplicationCommandView{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = s.StopPublication(context.WithoutCancel(ctx), publication.ID)
		return ApplicationCommandView{}, err
	}
	return s.ApplicationCommand(ctx, id)
}
