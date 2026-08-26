package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// DecommissionApplications uninstalls every application currently managed by
// Center. It preserves the control plane until every Agent has acknowledged
// removal, so an offline node cannot silently become an orphaned workload.
func (s *Store) DecommissionApplications(ctx context.Context, deleteData bool, progress func(string)) error {
	applications, err := s.ListApplications(ctx)
	if err != nil {
		return fmt.Errorf("center: list applications before decommission: %w", err)
	}
	agents, err := s.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("center: list nodes before decommission: %w", err)
	}
	connectedAgents := make(map[string]bool, len(agents))
	for _, agent := range agents {
		connectedAgents[agent.ID] = agent.Connected
	}
	groups := make([][]ApplicationView, 3)
	for _, application := range applications {
		if application.Status == "stopped" {
			continue
		}
		activeTask, err := s.activeDecommissionTask(ctx, application, deleteData)
		if err != nil {
			return err
		}
		if activeTask == "" {
			active, err := s.activeDeployment(ctx, application.NodeID, application.AppKey)
			if err != nil {
				return err
			}
			if !active.Installed {
				continue
			}
		}
		if !connectedAgents[application.NodeID] {
			return fmt.Errorf("center: node %s is offline; reconnect it before uninstalling managed applications", application.NodeID)
		}
		priority := decommissionPriority(application)
		groups[priority] = append(groups[priority], application)
	}
	for _, group := range groups {
		sort.Slice(group, func(left, right int) bool {
			if group[left].NodeID == group[right].NodeID {
				return group[left].AppKey < group[right].AppKey
			}
			return group[left].NodeID < group[right].NodeID
		})
	}
	total := len(groups[0]) + len(groups[1]) + len(groups[2])
	if progress != nil {
		progress(fmt.Sprintf("Managed applications to uninstall: %d", total))
	}
	for priority, group := range groups {
		deployments := make(map[string]ApplicationView, len(group))
		for _, application := range group {
			deploymentID, err := s.activeDecommissionTask(ctx, application, deleteData)
			if err != nil {
				return err
			}
			if deploymentID == "" {
				deployment, err := s.CreateDeployment(ctx, DeploymentRequest{
					AgentID: application.NodeID, AppKey: application.AppKey,
					Operation: "uninstall", DeleteData: deleteData,
				})
				if err != nil {
					return fmt.Errorf("center: queue uninstall for %s on %s: %w", application.Name, application.NodeID, err)
				}
				deploymentID = deployment.ID
			}
			deployments[deploymentID] = application
			if progress != nil {
				progress(fmt.Sprintf("Queued: %s (%s)", application.Name, application.NodeID))
			}
		}
		if err := s.waitForApplicationDecommission(ctx, deployments, progress); err != nil {
			return err
		}
		if priority == 0 {
			if err := s.waitForThreeXUIWorkerRemoval(ctx); err != nil {
				return err
			}
		}
	}
	if progress != nil && total != 0 {
		progress("Waiting for gateway, tunnel, and DNS cleanup...")
	}
	return s.waitForDecommissionInfrastructure(ctx)
}

// Workers and plugins are removed before their controllers. This preserves the
// dependency rules already enforced by the normal per-application lifecycle.
func decommissionPriority(application ApplicationView) int {
	if (application.AppKey == threeXUIAppKey && application.Role == threeXUIRoleWorker) || application.AppKey == "vastora-official/keeper" {
		return 0
	}
	if application.AppKey == threeXUIAppKey && application.Role == threeXUIRoleMaster {
		return 2
	}
	return 1
}

func (s *Store) waitForDecommissionInfrastructure(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := s.retryPublicationCleanups(ctx); err != nil {
			return fmt.Errorf("center: remove application DNS and tunnel records: %w", err)
		}
		var commandFailure string
		err := s.db.QueryRowContext(ctx, `SELECT error FROM application_commands WHERE reconciliation_required = 1 LIMIT 1`).Scan(&commandFailure)
		if err == nil {
			if commandFailure == "" {
				commandFailure = "Agent result requires reconciliation"
			}
			return fmt.Errorf("center: application cleanup command failed: %s", commandFailure)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("center: inspect failed application cleanup command: %w", err)
		}
		var publicationCleanup, gatewayPending, tunnelPending, commandPending int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE status = 'stopped' AND cleanup_pending = 1`).Scan(&publicationCleanup); err != nil {
			return fmt.Errorf("center: inspect publication cleanup: %w", err)
		}
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM gateway_states WHERE status IN ('pending', 'applying')`).Scan(&gatewayPending); err != nil {
			return fmt.Errorf("center: inspect gateway cleanup: %w", err)
		}
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloudflare_tunnels WHERE status IN ('pending', 'applying')`).Scan(&tunnelPending); err != nil {
			return fmt.Errorf("center: inspect tunnel cleanup: %w", err)
		}
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE state IN ('pending', 'running') OR reconciliation_required = 1`).Scan(&commandPending); err != nil {
			return fmt.Errorf("center: inspect application cleanup commands: %w", err)
		}
		if publicationCleanup == 0 && gatewayPending == 0 && tunnelPending == 0 && commandPending == 0 {
			var failedComponent, failedMessage string
			err := s.db.QueryRowContext(ctx, `SELECT component, message FROM (
				SELECT 'gateway' AS component, last_error AS message FROM gateway_states WHERE status = 'failed'
				UNION ALL
				SELECT 'Cloudflare tunnel' AS component, last_error AS message FROM cloudflare_tunnels WHERE status = 'failed'
			) WHERE message <> '' LIMIT 1`).Scan(&failedComponent, &failedMessage)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("center: inspect failed infrastructure cleanup: %w", err)
			}
			return fmt.Errorf("center: %s cleanup failed: %s", failedComponent, failedMessage)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("center: gateway, tunnel, or DNS cleanup did not finish: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Store) activeDecommissionTask(ctx context.Context, application ApplicationView, deleteData bool) (string, error) {
	var id, operation string
	var activeDeleteData bool
	err := s.db.QueryRowContext(ctx, `SELECT id, operation, delete_data FROM deployments
		WHERE agent_id = ? AND app_key = ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)
		ORDER BY rowid DESC LIMIT 1`, application.NodeID, application.AppKey).Scan(&id, &operation, &activeDeleteData)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("center: inspect active task for %s on %s: %w", application.Name, application.NodeID, err)
	}
	if operation != "uninstall" {
		return "", fmt.Errorf("center: %s on %s has an active %s operation; wait for it before uninstalling Center", application.Name, application.NodeID, operation)
	}
	if activeDeleteData != deleteData {
		return "", fmt.Errorf("center: %s on %s already has an uninstall with a different data-retention choice; wait for it to finish", application.Name, application.NodeID)
	}
	return id, nil
}

func (s *Store) waitForApplicationDecommission(ctx context.Context, deployments map[string]ApplicationView, progress func(string)) error {
	pending := make(map[string]ApplicationView, len(deployments))
	for id, application := range deployments {
		pending[id] = application
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for len(pending) != 0 {
		for id, application := range pending {
			var state, taskError string
			var reconciliationRequired int
			if err := s.db.QueryRowContext(ctx, `SELECT state, reconciliation_required, error FROM deployments WHERE id = ?`, id).Scan(&state, &reconciliationRequired, &taskError); err != nil {
				return fmt.Errorf("center: inspect uninstall for %s on %s: %w", application.Name, application.NodeID, err)
			}
			switch {
			case state == "succeeded":
				delete(pending, id)
				if progress != nil {
					progress(fmt.Sprintf("Removed: %s (%s)", application.Name, application.NodeID))
				}
			case state == "failed" || reconciliationRequired != 0:
				if taskError == "" {
					taskError = "Agent did not confirm application removal"
				}
				return fmt.Errorf("center: uninstall %s on %s failed: %s", application.Name, application.NodeID, taskError)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("center: application cleanup did not finish: %w", ctx.Err())
		case <-ticker.C:
		}
	}
	return nil
}

func (s *Store) waitForThreeXUIWorkerRemoval(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var pending int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM three_x_ui_nodes node
			JOIN applications worker ON worker.id = node.worker_application_id
			WHERE worker.status <> 'stopped' OR node.status IN ('pending', 'applying')`).Scan(&pending); err != nil {
			return fmt.Errorf("center: inspect 3x-ui node removal: %w", err)
		}
		if pending == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("center: 3x-ui node cleanup did not finish: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
