package agent

import (
	"context"
	"errors"

	"github.com/petauron/vastora/internal/landing"
)

type LandingServerProvisioner interface {
	Apply(context.Context, string, landing.ServerPlan) error
	Remove(context.Context, string, uint64) error
}

type NativeLandingServer struct{}

func (NativeLandingServer) Apply(ctx context.Context, nodeID string, plan landing.ServerPlan) error {
	return ApplyLanding(ctx, nodeID, plan)
}

func (NativeLandingServer) Remove(ctx context.Context, nodeID string, revision uint64) error {
	return RemoveLanding(ctx, nodeID, revision)
}

func (c Client) applyLandingServerTask(ctx context.Context, store *Store, task DeploymentTask) error {
	state := task.LandingServerState
	if c.LandingServer == nil || state == nil || state.Validate() != nil || task.Revision <= 0 || uint64(task.Revision) != state.Revision {
		return errors.New("agent: invalid or unavailable landing service task")
	}
	connection, err := store.Connection(ctx)
	if err != nil || connection.AgentID != state.NodeID {
		return errors.New("agent: landing service task belongs to another node")
	}
	store.landingMutationMu.Lock()
	defer store.landingMutationMu.Unlock()
	if state.Plan == nil {
		return c.LandingServer.Remove(ctx, state.NodeID, state.Revision)
	}
	return c.LandingServer.Apply(ctx, state.NodeID, *state.Plan)
}
