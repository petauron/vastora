package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// A historical controller-only installation can stage a package, but cannot
// implicitly activate an endpoint. Recheck in the completion transaction too.
func dormantMeridianUpgrade(ctx context.Context, db registryCredentialQuerier, deploymentID string) (bool, error) {
	var raw []byte
	var adoption string
	var endpoints int
	err := db.QueryRowContext(ctx, `SELECT r.resources_json,r.adoption_state,
		(SELECT COUNT(*) FROM meridian_endpoints e WHERE e.application_id=d.application_id)
		FROM deployments d JOIN application_resources r ON r.application_id=d.application_id
		WHERE d.id=? AND d.app_key=?`, deploymentID, meridianAppKey).Scan(&raw, &adoption, &endpoints)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var receipt struct {
		IntegrationState string            `json:"integrationState"`
		State            string            `json:"state"`
		Runtime          string            `json:"runtime"`
		Resources        []json.RawMessage `json:"resources"`
	}
	if json.Unmarshal(raw, &receipt) != nil {
		return false, errors.New("center: invalid installed Meridian receipt")
	}
	if receipt.IntegrationState != "dormant" {
		return false, nil
	}
	if adoption != "ready" || receipt.State != "ready" || receipt.Runtime != "docker" || len(receipt.Resources) != 0 || endpoints != 0 {
		return false, errors.New("center: dormant Meridian runtime changed; review required before upgrade")
	}
	return true, nil
}
