package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/controlplane"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// agentGRPCService is separate from the public web API and from Glivela's
// control plane. A newer stream cancels an older stream for the same Agent;
// persisted execution sessions and leases remain the authoritative fence.
type agentGRPCService struct {
	agentpb.UnimplementedAgentControlServer
	center  *Server
	mu      sync.Mutex
	streams map[string]*agentStream
}

type agentStream struct{ cancel context.CancelFunc }

func newAgentGRPCService(center *Server) *agentGRPCService {
	return &agentGRPCService{center: center, streams: make(map[string]*agentStream)}
}

func grpcCredential(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "agent credential required")
	}
	values := md.Get("authorization")
	if len(values) != 1 {
		return "", status.Error(codes.Unauthenticated, "agent credential required")
	}
	credential, ok := strings.CutPrefix(values[0], "Bearer ")
	credential = strings.TrimSpace(credential)
	if !ok || credential == "" {
		return "", status.Error(codes.Unauthenticated, "agent credential required")
	}
	return credential, nil
}

func (s *agentGRPCService) authenticate(ctx context.Context, agentID string) (string, error) {
	credential, err := grpcCredential(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(agentID) == "" || s.center.store.authenticateAgent(ctx, agentID, credential) != nil {
		return "", status.Error(codes.Unauthenticated, "agent authentication failed")
	}
	return credential, nil
}

func decodeAgentDocument(raw []byte, limit int, target any) error {
	if len(raw) == 0 || len(raw) > limit {
		return status.Error(codes.InvalidArgument, "invalid document size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return status.Error(codes.InvalidArgument, "invalid document")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return status.Error(codes.InvalidArgument, "invalid document")
	}
	return nil
}

func grpcDocument(value any) (*agentpb.Document, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot encode result")
	}
	if len(raw) > controlplane.MaxEnvelopeWire {
		return nil, status.Error(codes.ResourceExhausted, "result exceeds size limit")
	}
	return &agentpb.Document{Json: raw}, nil
}

func grpcTaskError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	if errors.Is(err, errExecutionBlocked) || errors.Is(err, errExecutionAuthorization) || errors.Is(err, errStaleTaskLease) {
		return status.Error(codes.FailedPrecondition, controlplane.SafeError(err.Error()))
	}
	return status.Error(codes.Internal, "agent operation failed")
}

func (s *agentGRPCService) Probe(context.Context, *agentpb.ProbeRequest) (*agentpb.ProbeResponse, error) {
	return &agentpb.ProbeResponse{Ready: true}, nil
}

func (s *agentGRPCService) Enroll(ctx context.Context, input *agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "enrollment is required")
	}
	credential, err := s.center.store.EnrollAgentOperation(ctx, input.Token, input.OperationId, input.Version, input.OperatingSystem, input.Architecture, input.PublicKey)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "enrollment rejected")
	}
	capabilities, err := json.Marshal(credential.Capabilities)
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot encode capabilities")
	}
	return &agentpb.EnrollResponse{AgentId: credential.ID, Credential: credential.Credential, Name: credential.Name, Roles: credential.Roles, CapabilitiesJson: capabilities}, nil
}

// waitForAssignment has no periodic polling. Subscribe before claiming so an
// enqueue racing with the database query cannot leave the stream asleep.
func (s *agentGRPCService) waitForAssignment(ctx context.Context, agentID, credential, sessionID string) (*AgentTask, error) {
	for {
		key := "agent:" + agentID
		changed := s.center.store.taskChanges.subscribe(key)
		task, err := s.center.store.claimExecutionTask(ctx, agentID, credential, sessionID, 0)
		if err != nil || task != nil {
			s.center.store.taskChanges.unsubscribe(key, changed)
			return task, err
		}
		// A bounded local recheck covers persisted changes from another Center
		// process without making the Agent poll over the network.
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			s.center.store.taskChanges.unsubscribe(key, changed)
			return nil, ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
			s.center.store.taskChanges.unsubscribe(key, changed)
		}
	}
}

func (s *agentGRPCService) replaceStream(ctx context.Context, agentID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	current := &agentStream{cancel: cancel}
	s.mu.Lock()
	if previous := s.streams[agentID]; previous != nil {
		previous.cancel()
	}
	s.streams[agentID] = current
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		s.mu.Lock()
		if s.streams[agentID] == current {
			delete(s.streams, agentID)
		}
		s.mu.Unlock()
	}
}

func sendAgentFrame(ctx context.Context, stream agentpb.AgentControl_ConnectServer, frame *agentpb.CenterFrame) error {
	result := make(chan error, 1)
	go func() { result <- stream.Send(frame) }()
	select {
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	case err := <-result:
		return err
	}
}

func (s *agentGRPCService) Connect(stream agentpb.AgentControl_ConnectServer) error {
	if _, err := grpcCredential(stream.Context()); err != nil {
		return err
	}
	type firstFrame struct {
		frame *agentpb.AgentFrame
		err   error
	}
	firstResult := make(chan firstFrame, 1)
	go func() { frame, err := stream.Recv(); firstResult <- firstFrame{frame: frame, err: err} }()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	var first *agentpb.AgentFrame
	select {
	case <-stream.Context().Done():
		return status.FromContextError(stream.Context().Err()).Err()
	case <-timer.C:
		return status.Error(codes.DeadlineExceeded, "Agent stream hello timed out")
	case result := <-firstResult:
		if result.err != nil {
			return result.err
		}
		first = result.frame
	}
	hello := first.GetHello()
	if hello == nil || hello.AgentId == "" || hello.SessionId == "" || hello.ExecutionProtocol != int32(controlplane.ExecutionProtocol) {
		return status.Error(codes.InvalidArgument, "invalid Agent stream hello")
	}
	credential, err := s.authenticate(stream.Context(), hello.AgentId)
	if err != nil {
		return err
	}
	if err := s.center.store.executionClaimAllowed(stream.Context(), hello.AgentId, hello.SessionId); err != nil {
		return grpcTaskError(err)
	}
	ctx, release := s.replaceStream(stream.Context(), hello.AgentId)
	defer release()
	if err := sendAgentFrame(ctx, stream, &agentpb.CenterFrame{Body: &agentpb.CenterFrame_Welcome{Welcome: &agentpb.Welcome{ExecutionProtocol: int32(controlplane.ExecutionProtocol)}}}); err != nil {
		return err
	}
	type received struct {
		frame *agentpb.AgentFrame
		err   error
	}
	receivedFrames := make(chan received, 1)
	go func() {
		for {
			frame, recvErr := stream.Recv()
			select {
			case receivedFrames <- received{frame: frame, err: recvErr}:
			case <-ctx.Done():
				return
			}
			if recvErr != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case received := <-receivedFrames:
			if errors.Is(received.err, io.EOF) {
				return nil
			}
			if received.err != nil {
				return received.err
			}
			if received.frame.GetReady() == nil {
				return status.Error(codes.InvalidArgument, "expected ready frame")
			}
			task, err := s.waitForAssignment(ctx, hello.AgentId, credential, hello.SessionId)
			if err != nil {
				return grpcTaskError(err)
			}
			if task.Kind == "landing.server.apply" || task.Kind == "landing.proxy.apply" && task.LandingProxyState != nil && task.LandingProxyState.Active() {
				if err := s.center.syncLandingAccess(ctx); err != nil {
					return status.Error(codes.Unavailable, "landing access could not be synchronized")
				}
			}
			encrypted, err := s.center.store.EncryptAgentTask(ctx, hello.AgentId, *task)
			if err != nil {
				return status.Error(codes.Unavailable, "task encryption failed")
			}
			assignment := &agentpb.Assignment{TaskId: encrypted.ID, Attempt: encrypted.Attempt,
				ExecutionId: task.Authorization.ID, ExecutionProtocol: int32(task.Authorization.Protocol), Digest: task.Authorization.Digest,
				EnvelopeVersion: int32(encrypted.Envelope.Version), EphemeralPublicKey: encrypted.Envelope.EphemeralPublicKey,
				Nonce: encrypted.Envelope.Nonce, Ciphertext: encrypted.Envelope.Ciphertext}
			if err := sendAgentFrame(ctx, stream, &agentpb.CenterFrame{Body: &agentpb.CenterFrame_Assignment{Assignment: assignment}}); err != nil {
				return err
			}
		}
	}
}

func (s *agentGRPCService) RegisterExecutionSession(ctx context.Context, input *agentpb.AgentDocument) (*agentpb.Document, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "session is required")
	}
	credential, err := s.authenticate(ctx, input.AgentId)
	if err != nil {
		return nil, err
	}
	var value controlplane.ExecutionSessionRequest
	if err := decodeAgentDocument(input.Json, controlplane.MaxJSONPayload, &value); err != nil {
		return nil, err
	}
	if err := s.center.store.RegisterExecutionSession(ctx, input.AgentId, credential, value.SessionID, value.Protocol); err != nil {
		return nil, grpcTaskError(err)
	}
	return grpcDocument(map[string]bool{"registered": true})
}

func (s *agentGRPCService) TransitionExecution(ctx context.Context, input *agentpb.ExecutionDocument) (*agentpb.Document, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "execution is required")
	}
	if _, err := s.authenticate(ctx, input.AgentId); err != nil {
		return nil, err
	}
	var value controlplane.ExecutionTransitionRequest
	if err := decodeAgentDocument(input.Json, controlplane.MaxJSONPayload, &value); err != nil {
		return nil, err
	}
	store := s.center.store
	if value.Action == "helper-observe" {
		ready, err := store.UpdateHelperObserved(ctx, input.AgentId, value.SessionID, input.ExecutionId)
		if err != nil {
			return nil, grpcTaskError(err)
		}
		return grpcDocument(map[string]bool{"ready": ready})
	}
	var err error
	switch value.Action {
	case "start":
		err = store.StartExecution(ctx, input.AgentId, value.SessionID, input.ExecutionId, value.Digest)
	case "step":
		err = store.CheckExecutionStep(ctx, input.AgentId, value.SessionID, input.ExecutionId, value.Phase)
	case "renew":
		err = store.RenewExecution(ctx, input.AgentId, value.SessionID, input.ExecutionId)
	case "helper-step":
		err = store.CheckUpdateHelperStep(ctx, input.AgentId, value.SessionID, input.ExecutionId, value.Phase)
	case "stop":
		err = store.StopExecution(ctx, input.AgentId, value.SessionID, input.ExecutionId, value.Unknown, value.Error)
	default:
		return nil, status.Error(codes.InvalidArgument, "invalid execution action")
	}
	if err != nil {
		return nil, grpcTaskError(err)
	}
	return grpcDocument(map[string]bool{"recorded": true})
}

func (s *agentGRPCService) RenewTaskLease(ctx context.Context, input *agentpb.TaskDocument) (*agentpb.Document, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "task is required")
	}
	credential, err := s.authenticate(ctx, input.AgentId)
	if err != nil {
		return nil, err
	}
	var value struct {
		Attempt int64 `json:"attempt"`
	}
	if err := decodeAgentDocument(input.Json, controlplane.MaxJSONPayload, &value); err != nil {
		return nil, err
	}
	expiresAt, err := s.center.store.RenewTaskLease(ctx, input.AgentId, credential, input.TaskId, value.Attempt)
	if err != nil {
		return nil, grpcTaskError(err)
	}
	return grpcDocument(map[string]string{"leaseExpiresAt": expiresAt.Format(time.RFC3339Nano)})
}
