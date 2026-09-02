package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gateway"
)

type NodeListenerAppliedState struct {
	Desired    gateway.NodeListenerState
	ConfigHash string
	AppliedAt  time.Time
}

func nodeListenerHash(state gateway.NodeListenerState) (string, error) {
	return gateway.NodeListenerConfigurationHash(state)
}

func validateAgentNodeListenerState(state gateway.NodeListenerState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	for _, route := range state.Listener.Routes {
		if !route.ManagedReality {
			if route.ProxyProtocol == gateway.ProxyProtocolV2 {
				return errors.New("agent: Proxy Protocol v2 is reserved for managed REALITY routes")
			}
			continue
		}
		if route.ProxyProtocol != gateway.ProxyProtocolV2 || len(route.Upstreams) != 1 || route.Upstreams[0].Address != dockerruntime.ThreeXUIAlias || route.Upstreams[0].Port != threeXUIRealityPort {
			return errors.New("agent: managed REALITY listener must target the local 3x-ui port 443 with Proxy Protocol v2")
		}
	}
	return nil
}

func nodeListenerRuntimeStatus(ctx context.Context, store *Store, provisioner NodeListenerProvisioner) (bool, int64, string) {
	state, err := store.NodeListenerState(ctx)
	if errors.Is(err, errNoAppliedNodeListenerState) {
		return true, 0, ""
	}
	if err != nil {
		return false, 0, ""
	}
	if len(state.Desired.Listener.Routes) == 0 {
		return provisioner != nil && provisioner.Absent(ctx) == nil, state.Desired.Revision, state.ConfigHash
	}
	if provisioner == nil || provisioner.Health(ctx) != nil {
		return false, state.Desired.Revision, state.ConfigHash
	}
	if err := verifyNodeListenerReadBack(ctx, provisioner, state.Desired.Listener); err != nil {
		return false, state.Desired.Revision, state.ConfigHash
	}
	return true, state.Desired.Revision, state.ConfigHash
}

func verifyNodeListenerReadBack(ctx context.Context, provisioner NodeListenerProvisioner, desired gateway.SharedHTTPS) error {
	liveHash, err := provisioner.ConfigurationHash(ctx)
	if err != nil {
		return err
	}
	configuration, err := haproxyConfiguration(desired)
	if err != nil {
		return err
	}
	expectedHash := haproxyConfigurationHash(configuration)
	if liveHash != expectedHash {
		return errors.New("agent: HAProxy configuration differs from the node-listener desired state")
	}
	return nil
}

func (s *Store) NodeListenerState(ctx context.Context) (NodeListenerAppliedState, error) {
	var value NodeListenerAppliedState
	var encoded []byte
	var appliedAt string
	err := s.db.QueryRowContext(ctx, `SELECT desired_json, config_hash, applied_at FROM node_listener_applied_state WHERE id = 1`).Scan(&encoded, &value.ConfigHash, &appliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeListenerAppliedState{}, errNoAppliedNodeListenerState
	}
	if err != nil {
		return NodeListenerAppliedState{}, fmt.Errorf("agent: read node listener state: %w", err)
	}
	if json.Unmarshal(encoded, &value.Desired) != nil || validateAgentNodeListenerState(value.Desired) != nil {
		return NodeListenerAppliedState{}, errors.New("agent: stored node listener state is invalid")
	}
	value.AppliedAt, err = time.Parse(time.RFC3339Nano, appliedAt)
	if err != nil {
		return NodeListenerAppliedState{}, errors.New("agent: stored node listener timestamp is invalid")
	}
	return value, nil
}

func (s *Store) RecordNodeListenerState(ctx context.Context, desired gateway.NodeListenerState) error {
	desired = desired.Sorted()
	if err := validateAgentNodeListenerState(desired); err != nil {
		return err
	}
	encoded, err := json.Marshal(desired)
	if err != nil {
		return err
	}
	hash, err := nodeListenerHash(desired)
	if err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `INSERT INTO node_listener_applied_state(id, applied_revision, desired_json, config_hash, applied_at)
		VALUES(1, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET applied_revision=excluded.applied_revision, desired_json=excluded.desired_json,
		config_hash=excluded.config_hash, applied_at=excluded.applied_at WHERE excluded.applied_revision >= node_listener_applied_state.applied_revision`, desired.Revision, encoded, hash, now)
	if err != nil {
		return fmt.Errorf("agent: record node listener state: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("agent: stale node listener revision")
	}
	return nil
}

type NodeListenerCoordinator interface {
	PrepareNodeListener(context.Context) error
	RestoreGatewayPublicBindings(context.Context) error
	RestoreGatewayAfterNodeListenerFailure(context.Context) error
}

func applyNodeListenerState(ctx context.Context, store *Store, provisioner NodeListenerProvisioner, coordinator NodeListenerCoordinator, desired gateway.NodeListenerState) error {
	store.gatewayMutationMu.Lock()
	defer store.gatewayMutationMu.Unlock()
	if provisioner == nil {
		return errors.New("agent: node listener provisioning is not configured")
	}
	if err := validateAgentNodeListenerState(desired); err != nil {
		return err
	}
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	if desired.NodeID != connection.AgentID {
		return errors.New("agent: node listener is owned by another Agent")
	}
	current, currentErr := store.NodeListenerState(ctx)
	if currentErr == nil && desired.Revision < current.Desired.Revision {
		return nil
	}
	var previous *NodeListenerAppliedState
	if currentErr == nil {
		previous = &current
	}
	if currentErr == nil && desired.Revision == current.Desired.Revision {
		hash, err := nodeListenerHash(desired)
		if err != nil {
			return err
		}
		if hash != current.ConfigHash {
			return errors.New("agent: node listener revision conflicts with persisted state")
		}
		if len(desired.Listener.Routes) == 0 {
			if err := provisioner.Remove(ctx); err != nil {
				return err
			}
			if err := provisioner.Absent(ctx); err != nil {
				return err
			}
			if coordinator != nil {
				return coordinator.RestoreGatewayPublicBindings(ctx)
			}
			return nil
		}
		if provisioner.Health(ctx) == nil && verifyNodeListenerReadBack(ctx, provisioner, desired.Listener) == nil {
			return nil
		}
	}
	if currentErr != nil && !errors.Is(currentErr, errNoAppliedNodeListenerState) {
		return currentErr
	}
	if len(desired.Listener.Routes) == 0 {
		if err := provisioner.Remove(ctx); err != nil {
			return err
		}
		if err := provisioner.Absent(ctx); err != nil {
			return err
		}
		if coordinator != nil {
			if err := coordinator.RestoreGatewayPublicBindings(ctx); err != nil {
				return rollbackNodeListener(ctx, provisioner, coordinator, previous, err)
			}
		}
	} else {
		if coordinator != nil {
			if err := coordinator.PrepareNodeListener(ctx); err != nil {
				return err
			}
		}
		if err := provisioner.Apply(ctx, desired.Listener); err != nil {
			return rollbackNodeListener(ctx, provisioner, coordinator, previous, err)
		}
		if err := provisioner.Health(ctx); err != nil {
			return rollbackNodeListener(ctx, provisioner, coordinator, previous, err)
		}
		if err := verifyNodeListenerReadBack(ctx, provisioner, desired.Listener); err != nil {
			return rollbackNodeListener(ctx, provisioner, coordinator, previous, err)
		}
	}
	if err := store.RecordNodeListenerState(ctx, desired); err != nil {
		return rollbackNodeListener(ctx, provisioner, coordinator, previous, err)
	}
	return nil
}

func rollbackNodeListener(ctx context.Context, provisioner NodeListenerProvisioner, coordinator NodeListenerCoordinator, previous *NodeListenerAppliedState, cause error) error {
	var rollbackErr error
	if previous != nil && len(previous.Desired.Listener.Routes) > 0 {
		if coordinator != nil {
			rollbackErr = coordinator.PrepareNodeListener(ctx)
		}
		if rollbackErr == nil {
			rollbackErr = provisioner.Apply(ctx, previous.Desired.Listener)
		}
		if rollbackErr == nil {
			rollbackErr = provisioner.Health(ctx)
		}
		if rollbackErr == nil {
			rollbackErr = verifyNodeListenerReadBack(ctx, provisioner, previous.Desired.Listener)
		}
	} else {
		rollbackErr = provisioner.Remove(ctx)
		if rollbackErr == nil {
			rollbackErr = provisioner.Absent(ctx)
		}
		if rollbackErr == nil && coordinator != nil {
			rollbackErr = coordinator.RestoreGatewayAfterNodeListenerFailure(ctx)
		}
	}
	if rollbackErr != nil {
		return errors.Join(cause, fmt.Errorf("agent: restore previous ingress after node-listener failure: %w", rollbackErr))
	}
	return cause
}

func restoreNodeListenerState(ctx context.Context, store *Store, provisioner NodeListenerProvisioner, coordinator NodeListenerCoordinator) error {
	if provisioner == nil {
		return nil
	}
	state, err := store.NodeListenerState(ctx)
	if errors.Is(err, errNoAppliedNodeListenerState) {
		return nil
	}
	if err != nil {
		return err
	}
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	if state.Desired.NodeID != connection.AgentID {
		return errors.New("agent: stored node listener is owned by another Agent")
	}
	if len(state.Desired.Listener.Routes) == 0 {
		if err := provisioner.Remove(ctx); err != nil {
			return err
		}
		if err := provisioner.Absent(ctx); err != nil {
			return err
		}
		if coordinator != nil {
			return coordinator.RestoreGatewayPublicBindings(ctx)
		}
		return nil
	}
	if coordinator != nil {
		if err := coordinator.PrepareNodeListener(ctx); err != nil {
			return err
		}
	}
	if err := provisioner.Apply(ctx, state.Desired.Listener); err != nil {
		return fmt.Errorf("agent: restore node listener: %w", err)
	}
	if err := provisioner.Health(ctx); err != nil {
		return err
	}
	return verifyNodeListenerReadBack(ctx, provisioner, state.Desired.Listener)
}

func RestoreNodeListenerStartup(ctx context.Context, store *Store, provisioner NodeListenerProvisioner, coordinator NodeListenerCoordinator) error {
	return restoreNodeListenerState(ctx, store, provisioner, coordinator)
}
