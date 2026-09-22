package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type MeridianCutoverView struct {
	State                         string `json:"state"`
	SubscriptionAuthority         string `json:"subscriptionAuthority"`
	LegacyControllerApplicationID string `json:"legacyControllerApplicationId,omitempty"`
	LegacyControllerName          string `json:"legacyControllerName,omitempty"`
	ImportSHA256                  string `json:"importSha256,omitempty"`
	BackupRevision                int64  `json:"backupRevision,omitempty"`
	ExpectedAccounts              int    `json:"expectedAccounts"`
	ImportedAccounts              int    `json:"importedAccounts"`
	ExpectedCredentials           int    `json:"expectedCredentials"`
	ImportedCredentials           int    `json:"importedCredentials"`
	ExpectedEndpoints             int    `json:"expectedEndpoints"`
	ReadyEndpoints                int    `json:"readyEndpoints"`
	RetiredEndpoints              int    `json:"retiredEndpoints"`
	ExpectedRoutes                int    `json:"expectedRoutes"`
	ReadyRoutes                   int    `json:"readyRoutes"`
	BlockedRoutes                 int    `json:"blockedRoutes"`
	PendingDeployments            int    `json:"pendingDeployments"`
	FailedDeployments             int    `json:"failedDeployments"`
	LastError                     string `json:"lastError,omitempty"`
	UpdatedAt                     string `json:"updatedAt"`
	SwitchedAt                    string `json:"switchedAt,omitempty"`
	Complete                      bool   `json:"complete"`
}

var errMeridianOwnsLegacyLanding = errors.New("center: Meridian authority owns landing configuration")

func meridianOwnsLegacyLanding(ctx context.Context, queryer networkQueryer) (bool, error) {
	var owns bool
	err := queryer.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM meridian_cutover
		WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire','complete')
	)`).Scan(&owns)
	return owns, err
}

func (s *Store) MeridianCutover(ctx context.Context) (MeridianCutoverView, error) {
	var view MeridianCutoverView
	var legacyID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT state,legacy_controller_application_id,subscription_authority,import_sha256,backup_revision,
		expected_accounts,expected_credentials,expected_endpoints,expected_routes,last_error,updated_at,switched_at
		FROM meridian_cutover WHERE id=1`).Scan(
		&view.State, &legacyID, &view.SubscriptionAuthority, &view.ImportSHA256, &view.BackupRevision,
		&view.ExpectedAccounts, &view.ExpectedCredentials, &view.ExpectedEndpoints, &view.ExpectedRoutes,
		&view.LastError, &view.UpdatedAt, &view.SwitchedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MeridianCutoverView{}, errors.New("center: Meridian cutover state is unavailable")
	}
	if err != nil {
		return MeridianCutoverView{}, fmt.Errorf("center: read Meridian cutover state: %w", err)
	}
	if legacyID.Valid {
		view.LegacyControllerApplicationID = legacyID.String
		_ = s.db.QueryRowContext(ctx, `SELECT name FROM applications WHERE id=?`, legacyID.String).Scan(&view.LegacyControllerName)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM meridian_accounts),
		(SELECT COUNT(*) FROM meridian_credentials),
		(SELECT COUNT(*) FROM meridian_endpoints endpoint JOIN services service ON service.id=endpoint.service_id
		 WHERE endpoint.status='ready' AND endpoint.runtime_healthy=1 AND endpoint.desired_revision=endpoint.applied_revision
		 AND service.status='ready' AND (endpoint.vless_enabled=0 OR EXISTS(
			SELECT 1 FROM publications publication WHERE publication.service_id=endpoint.service_id
			AND publication.kind='public_shared_443' AND publication.status='ready'
			AND publication.desired_revision=publication.applied_revision
			AND publication.hostname=endpoint.advertise_host
			AND EXISTS(SELECT 1 FROM json_each(endpoint.server_names_json) WHERE value=publication.sni_hostname)
		 ))),
		(SELECT COUNT(*) FROM meridian_endpoints WHERE status='ready' AND runtime_healthy=1 AND desired_revision=applied_revision AND legacy_retired=1),
		(SELECT COUNT(*) FROM meridian_route_grants WHERE enabled=1 AND status='ready' AND runtime_healthy=1 AND desired_revision=applied_revision),
		(SELECT COUNT(*) FROM meridian_route_grants WHERE enabled=1 AND status='blocked'),
		(SELECT COUNT(*) FROM meridian_deployments WHERE status IN ('pending','applying')) + (SELECT COUNT(*) FROM deployments WHERE app_key='vastora-official/meridian' AND state IN ('pending','running')),
		(SELECT COUNT(*) FROM meridian_deployments WHERE status='failed') + (SELECT COUNT(*) FROM applications WHERE app_key='vastora-official/meridian' AND status='failed' AND id IN (SELECT application_id FROM meridian_endpoints))`).Scan(
		&view.ImportedAccounts, &view.ImportedCredentials, &view.ReadyEndpoints,
		&view.RetiredEndpoints, &view.ReadyRoutes, &view.BlockedRoutes, &view.PendingDeployments, &view.FailedDeployments,
	); err != nil {
		return MeridianCutoverView{}, fmt.Errorf("center: summarize Meridian cutover: %w", err)
	}
	if view.State == "publish" && view.LastError == "" {
		view.LastError, err = meridianCutoverPublicationIssue(ctx, s.db)
		if err != nil {
			return MeridianCutoverView{}, fmt.Errorf("center: inspect Meridian subscription publication: %w", err)
		}
	}
	view.Complete = view.State == "complete" && view.SubscriptionAuthority == "meridian" &&
		view.LastError == "" && view.PendingDeployments == 0 && view.FailedDeployments == 0 &&
		view.ImportedAccounts == view.ExpectedAccounts && view.ImportedCredentials == view.ExpectedCredentials &&
		view.ReadyEndpoints == view.ExpectedEndpoints && view.RetiredEndpoints == view.ExpectedEndpoints &&
		view.ReadyRoutes == view.ExpectedRoutes && view.BlockedRoutes == 0
	return view, nil
}
