package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/tailscalehost"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type grpcHeartbeat struct {
	TailscaleUpdateObservation   *tailscalehost.UpdateObservation   `json:"tailscaleUpdateObservation"`
	LandingEgressAddresses       []landing.EgressAddress            `json:"landingEgressAddresses"`
	PublicKey                    []byte                             `json:"publicKey"`
	Version                      string                             `json:"version"`
	AppliedInstallations         int                                `json:"appliedInstallations"`
	Roles                        []string                           `json:"roles"`
	Capabilities                 NodeCapabilities                   `json:"capabilities"`
	NetworkCandidates            []networking.Candidate             `json:"networkCandidates"`
	PublicEgress                 *networking.PublicEgress           `json:"publicEgress"`
	ApplicationEndpoints         []ApplicationEndpointObservation   `json:"applicationEndpoints"`
	ApplicationEndpointsObserved bool                               `json:"applicationEndpointsObserved"`
	MeridianRuntime              *meridianruntime.Result            `json:"meridianRuntime"`
	GatewayHealthy               bool                               `json:"gatewayHealthy"`
	RuntimeRecovery              string                             `json:"runtimeRecovery"`
	RuntimeRecoveryApplications  []controlplane.RecoveryApplication `json:"runtimeRecoveryApplications"`
	GatewayRevision              int64                              `json:"gatewayRevision"`
	GatewayConfigHash            string                             `json:"gatewayConfigHash"`
	NodeListenerHealthy          bool                               `json:"nodeListenerHealthy"`
	LandingHealth                *landing.Health                    `json:"landingHealth"`
	LandingServerHealth          *landing.ServerHealth              `json:"landingServerHealth"`
	PrivateNetworkPeer           *landing.PeerIdentity              `json:"privateNetworkPeer"`
	LandingClientRuntime         *landing.ClientRuntime             `json:"landingClientRuntime"`
	NodeListenerRevision         int64                              `json:"nodeListenerRevision"`
	NodeListenerConfigHash       string                             `json:"nodeListenerConfigHash"`
	ApplicationRuntimeGeneration int                                `json:"applicationRuntimeGeneration"`
	RemoteUpdateSupported        bool                               `json:"remoteUpdateSupported"`
	TailscaleEnrolled            bool                               `json:"tailscaleEnrolled"`
	TailscaleOwnership           string                             `json:"tailscaleOwnership"`
	Startup                      bool                               `json:"startup"`
}

func (s *agentGRPCService) Heartbeat(ctx context.Context, input *agentpb.AgentDocument) (*agentpb.Document, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "heartbeat is required")
	}
	credential, err := s.authenticate(ctx, input.AgentId)
	if err != nil {
		return nil, err
	}
	var value grpcHeartbeat
	if err := decodeAgentDocument(input.Json, controlplane.MaxJSONPayload, &value); err != nil {
		return nil, err
	}
	store := s.center.store
	heartbeat := NodeHeartbeat{TailscaleUpdateObservation: value.TailscaleUpdateObservation, LandingServerHealth: value.LandingServerHealth,
		PrivateNetworkPeer: value.PrivateNetworkPeer, LandingEgressAddresses: value.LandingEgressAddresses, LandingClientRuntime: value.LandingClientRuntime,
		LandingHealth: value.LandingHealth, PublicKey: value.PublicKey, Version: value.Version, AppliedInstallations: value.AppliedInstallations,
		Roles: value.Roles, Capabilities: value.Capabilities, NetworkCandidates: value.NetworkCandidates, PublicEgress: value.PublicEgress,
		ApplicationEndpoints: value.ApplicationEndpoints, ApplicationEndpointsObserved: value.ApplicationEndpointsObserved, MeridianRuntime: value.MeridianRuntime,
		GatewayHealthy: value.GatewayHealthy, RuntimeRecovery: value.RuntimeRecovery, RuntimeRecoveryApplications: value.RuntimeRecoveryApplications,
		GatewayRevision: value.GatewayRevision, GatewayConfigHash: value.GatewayConfigHash, NodeListenerHealthy: value.NodeListenerHealthy,
		NodeListenerRevision: value.NodeListenerRevision, NodeListenerConfigHash: value.NodeListenerConfigHash,
		ApplicationRuntimeGeneration: value.ApplicationRuntimeGeneration, RemoteUpdateSupported: value.RemoteUpdateSupported,
		TailscaleOwnership: value.TailscaleOwnership, Startup: value.Startup}
	if err := store.RecordAgentHeartbeat(ctx, input.AgentId, credential, heartbeat); err != nil {
		slog.ErrorContext(ctx, "Agent heartbeat projection failed", "agent_id", input.AgentId, "error", controlplane.SafeError(err.Error()))
		return nil, grpcTaskError(err)
	}
	targets, err := store.landingLatencyTargets(ctx, input.AgentId)
	if err != nil {
		return nil, grpcTaskError(err)
	}
	network, err := store.CenterNetworkConfig(ctx)
	if err != nil {
		return nil, grpcTaskError(err)
	}
	var isolation *TailscaleIsolationDesiredState
	if value.TailscaleEnrolled {
		isolation, err = store.tailscaleIsolationDesiredState(ctx, input.AgentId)
		if err != nil {
			return nil, grpcTaskError(err)
		}
	}
	centerURL := network.AgentConnectURL
	if s.center.coLocatedAgentURL != "" {
		coLocated, err := store.networkCandidatesAreCoLocated(value.NetworkCandidates)
		if err != nil {
			return nil, grpcTaskError(err)
		}
		if coLocated {
			centerURL = s.center.coLocatedAgentURL
		}
	}
	updateTaskID, err := store.activeTailscaleUpdateTask(ctx, input.AgentId)
	if err != nil {
		return nil, grpcTaskError(err)
	}
	return grpcDocument(map[string]any{"tailscaleUpdateTaskId": updateTaskID, "connected": true,
		"tailscaleCheckGeneration": s.center.tailscaleCheckGeneration.Load(), "landingLatencyTargets": targets,
		"centerUrl": centerURL, "tailscaleIsolation": isolation, "publicAddressLookupUrl": s.center.publicAddressLookupURL,
		"publicHelperAllowPrivate": s.center.publicHelperAllowPrivate})
}

// Results belong to the authenticated source; removed peers cannot report.
func (s *agentGRPCService) ReportLandingLatency(ctx context.Context, input *agentpb.AgentDocument) (*agentpb.Document, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "latency observation is required")
	}
	if _, err := s.authenticate(ctx, input.AgentId); err != nil {
		return nil, err
	}
	var observation landing.LatencyObservation
	if err := decodeAgentDocument(input.Json, controlplane.MaxJSONPayload, &observation); err != nil {
		return nil, err
	}
	targets, err := s.center.store.landingLatencyTargets(ctx, input.AgentId)
	if err != nil {
		return nil, grpcTaskError(err)
	}
	if !s.center.store.recordLandingLatency(input.AgentId, targets, &observation) {
		return nil, status.Error(codes.FailedPrecondition, "latency observation is stale or unauthorized")
	}
	return grpcDocument(map[string]bool{"recorded": true})
}

func (s *agentGRPCService) ImportLegacyReceipts(ctx context.Context, input *agentpb.AgentDocument) (*agentpb.Document, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "receipt is required")
	}
	credential, err := s.authenticate(ctx, input.AgentId)
	if err != nil {
		return nil, err
	}
	var value controlplane.LegacyReceiptImport
	if err := decodeAgentDocument(input.Json, controlplane.LegacyReceiptMaxPayloadBytes, &value); err != nil {
		return nil, err
	}
	id, err := s.center.store.ImportLegacyReceipt(ctx, input.AgentId, credential, value)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "legacy receipt rejected")
	}
	return grpcDocument(map[string]any{"archived": true, "id": id, "digest": value.Digest})
}

func (s *agentGRPCService) CompleteTask(ctx context.Context, input *agentpb.TaskDocument) (*agentpb.Document, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "task result is required")
	}
	credential, err := s.authenticate(ctx, input.AgentId)
	if err != nil {
		return nil, err
	}
	var value struct {
		ExecutionID                  string          `json:"executionId"`
		SessionID                    string          `json:"sessionId"`
		HostUpdateHelper             bool            `json:"hostUpdateHelper"`
		Attempt                      int64           `json:"attempt"`
		Succeeded                    bool            `json:"succeeded"`
		Error                        string          `json:"error"`
		Result                       json.RawMessage `json:"result"`
		ReconciliationRequired       bool            `json:"reconciliationRequired"`
		ApplicationRuntimeGeneration *int            `json:"applicationRuntimeGeneration"`
	}
	if err := decodeAgentDocument(input.Json, controlplane.MaxJSONPayload, &value); err != nil {
		return nil, err
	}
	store := s.center.store
	var matches bool
	if err := store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE id=? AND agent_id=? AND task_id=? AND attempt=? AND session_id=?)`, value.ExecutionID, input.AgentId, input.TaskId, value.Attempt, value.SessionID).Scan(&matches); err != nil || !matches {
		return nil, status.Error(codes.FailedPrecondition, "execution authorization is no longer valid")
	}
	if len(value.Result) == 0 {
		value.Result = json.RawMessage(`{}`)
	}
	if err := store.StoreExecutionResult(ctx, input.AgentId, value.SessionID, value.ExecutionID, value.Result, value.Succeeded, value.ReconciliationRequired, value.Error, value.ApplicationRuntimeGeneration, value.HostUpdateHelper); err != nil {
		return nil, grpcTaskError(err)
	}
	generations := []int{}
	if value.ApplicationRuntimeGeneration != nil {
		generations = append(generations, *value.ApplicationRuntimeGeneration)
	}
	committed := false
	commit := func(tx *sql.Tx) error {
		if err := validateExecutionProjection(ctx, tx, value.ExecutionID, value.Succeeded); err != nil {
			return err
		}
		if err := store.finalizeExecution(ctx, tx, input.AgentId, value.SessionID, value.ExecutionID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	}
	if err := store.completeTaskWithDisposition(ctx, commit, input.AgentId, credential, input.TaskId, value.Attempt, value.Succeeded, value.Error, value.Result, value.ReconciliationRequired, generations...); err != nil {
		if errors.Is(err, errInvalidReconciliationDisposition) {
			return nil, status.Error(codes.InvalidArgument, "invalid reconciliation disposition")
		}
		return nil, status.Error(codes.FailedPrecondition, "task result could not be finalized")
	}
	if !committed {
		return nil, status.Error(codes.FailedPrecondition, "execution authorization is no longer valid")
	}
	return grpcDocument(map[string]bool{"completed": true})
}

type grpcLifecycleRequest struct {
	ExecutionID string `json:"executionId"`
	SessionID   string `json:"sessionId"`
	TaskID      string `json:"taskId,omitempty"`
	Attempt     int64  `json:"attempt"`
}

func (s *agentGRPCService) BeginAgentUpdate(ctx context.Context, input *agentpb.TaskDocument) (*agentpb.Document, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "task is required")
	}
	credential, err := s.authenticate(ctx, input.AgentId)
	if err != nil {
		return nil, err
	}
	var value grpcLifecycleRequest
	if err := decodeAgentDocument(input.Json, controlplane.MaxJSONPayload, &value); err != nil {
		return nil, err
	}
	if err := s.center.store.beginAgentUpdateExecution(ctx, input.AgentId, credential, input.TaskId, value.Attempt, value.ExecutionID, value.SessionID); err != nil {
		return nil, grpcTaskError(err)
	}
	return grpcDocument(map[string]bool{"started": true})
}

func (s *agentGRPCService) BeginTailscaleUpdate(ctx context.Context, input *agentpb.TaskDocument) (*agentpb.Document, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "task is required")
	}
	credential, err := s.authenticate(ctx, input.AgentId)
	if err != nil {
		return nil, err
	}
	var value grpcLifecycleRequest
	if err := decodeAgentDocument(input.Json, controlplane.MaxJSONPayload, &value); err != nil {
		return nil, err
	}
	if err := s.center.store.beginTailscaleUpdate(ctx, input.AgentId, credential, input.TaskId, value.ExecutionID, value.SessionID, value.Attempt); err != nil {
		return nil, grpcTaskError(err)
	}
	return grpcDocument(map[string]bool{"started": true})
}

func (s *agentGRPCService) BeginDecommission(ctx context.Context, input *agentpb.AgentDocument) (*agentpb.Document, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "decommission is required")
	}
	credential, err := s.authenticate(ctx, input.AgentId)
	if err != nil {
		return nil, err
	}
	var value grpcLifecycleRequest
	if err := decodeAgentDocument(input.Json, controlplane.MaxJSONPayload, &value); err != nil {
		return nil, err
	}
	if err := s.center.store.beginAgentDecommission(ctx, input.AgentId, credential, value.TaskID, value.Attempt, value.ExecutionID, value.SessionID); err != nil {
		return nil, grpcTaskError(err)
	}
	return grpcDocument(map[string]bool{"started": true})
}

func (s *agentGRPCService) DecommissionCallback(ctx context.Context, input *agentpb.DecommissionRequest) (*agentpb.Document, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "callback is required")
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if ids := md.Get("x-vastora-callback-task-id"); len(ids) > 0 && (len(ids) != 1 || ids[0] != input.TaskId) {
			return nil, status.Error(codes.PermissionDenied, "callback task does not match its path")
		}
	}
	token, err := grpcCredential(ctx)
	if err != nil {
		return nil, err
	}
	var value struct {
		Action   string `json:"action,omitempty"`
		Sequence int64  `json:"sequence,omitempty"`
		Phase    string `json:"phase,omitempty"`
		Attempt  int64  `json:"attempt"`
		Error    string `json:"error,omitempty"`
	}
	if err := decodeAgentDocument(input.Json, controlplane.MaxJSONPayload, &value); err != nil {
		return nil, err
	}
	if value.Action == "step" {
		if value.Error != "" {
			return nil, status.Error(codes.InvalidArgument, "invalid cleanup step")
		}
		if err := s.center.store.AuthorizeDecommissionStep(ctx, input.TaskId, token, value.Attempt, value.Sequence, value.Phase); err != nil {
			return nil, grpcTaskError(err)
		}
		return grpcDocument(map[string]bool{"recorded": true})
	}
	if value.Action != "" || value.Sequence != 0 || value.Phase != "" {
		return nil, status.Error(codes.InvalidArgument, "invalid cleanup callback")
	}
	if err := s.center.store.completeAgentDecommissionCallback(ctx, input.TaskId, token, value.Attempt, value.Error); err != nil {
		return nil, status.Error(codes.Unauthenticated, "cleanup callback rejected")
	}
	return grpcDocument(map[string]bool{"completed": value.Error == "", "recorded": true})
}
