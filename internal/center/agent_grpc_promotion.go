package center

import (
	"context"

	"github.com/petauron/vastora/internal/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *agentGRPCService) PrepareGatewayPromotion(ctx context.Context, input *agentpb.AgentDocument) (*agentpb.Document, error) {
	if input == nil {
		return nil, status.Error(codes.InvalidArgument, "gateway promotion is required")
	}
	credential, err := s.authenticate(ctx, input.AgentId)
	if err != nil {
		return nil, err
	}
	var request struct {
		ID string `json:"id"`
	}
	if err := decodeAgentDocument(input.Json, 4096, &request); err != nil {
		return nil, err
	}
	grant, err := s.center.store.PrepareGatewayPromotion(ctx, input.AgentId, credential, request.ID)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "exact unexpired Center-operator authorization and an idle node are required")
	}
	return grpcDocument(grant)
}
