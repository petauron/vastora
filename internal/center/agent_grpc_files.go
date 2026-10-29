package center

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/platform"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const agentGRPCChunkSize = 64 << 10

func (s *agentGRPCService) DownloadAgentBinary(input *agentpb.BinaryRequest, stream agentpb.AgentControl_DownloadAgentBinaryServer) error {
	if input == nil {
		return status.Error(codes.InvalidArgument, "binary target is required")
	}
	if _, err := s.authenticate(stream.Context(), input.AgentId); err != nil {
		return err
	}
	// The client may retry a transport interruption only after it has proof
	// that this TLS-protected call reached the authenticated service.
	if err := stream.SendHeader(metadata.Pairs("x-vastora-binary-authenticated", "1")); err != nil {
		return err
	}
	target, err := platform.Parse(input.OperatingSystem, input.Architecture)
	if err != nil {
		return status.Error(codes.NotFound, "Agent binary target is unavailable")
	}
	path := filepath.Join(s.center.agentBinariesDir, target.AgentBinaryName())
	file, err := os.Open(path)
	if err != nil {
		return status.Error(codes.NotFound, "Agent binary target is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 256<<20 {
		return status.Error(codes.NotFound, "Agent binary target is unavailable")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return status.Error(codes.Internal, "cannot inspect Agent binary")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return status.Error(codes.Internal, "cannot inspect Agent binary")
	}
	header := &agentpb.BinaryHeader{Version: Version, Sha256: hex.EncodeToString(digest.Sum(nil)), Size: info.Size()}
	if err := stream.Send(&agentpb.BinaryFrame{Body: &agentpb.BinaryFrame_Header{Header: header}}); err != nil {
		return err
	}
	buf := make([]byte, agentGRPCChunkSize)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			// Send must finish before buf can be reused.
			if err := stream.Send(&agentpb.BinaryFrame{Body: &agentpb.BinaryFrame_Chunk{Chunk: &agentpb.DataChunk{Data: buf[:n]}}}); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return status.Error(codes.Internal, "cannot read Agent binary")
		}
	}
}

type grpcBackupReader struct {
	stream  agentpb.AgentControl_UploadMigrationBackupServer
	pending []byte
}

func (r *grpcBackupReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 {
		frame, err := r.stream.Recv()
		if err != nil {
			return 0, err
		}
		chunk := frame.GetChunk()
		if chunk == nil || len(chunk.Data) == 0 || len(chunk.Data) > agentGRPCChunkSize {
			return 0, errors.New("invalid backup chunk")
		}
		r.pending = chunk.Data
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (s *agentGRPCService) UploadMigrationBackup(stream agentpb.AgentControl_UploadMigrationBackupServer) error {
	if _, err := grpcCredential(stream.Context()); err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	header := first.GetHeader()
	if header == nil {
		return status.Error(codes.InvalidArgument, "backup header is required")
	}
	credential, err := s.authenticate(stream.Context(), header.AgentId)
	if err != nil {
		return err
	}
	value, err := s.center.store.StoreThreeXUIBackup(stream.Context(), header.AgentId, credential, header.ApplicationId, header.Revision, &grpcBackupReader{stream: stream})
	if err != nil {
		return status.Error(codes.FailedPrecondition, "backup was rejected")
	}
	result, err := grpcDocument(value)
	if err != nil {
		return err
	}
	return stream.SendAndClose(result)
}

func (s *agentGRPCService) DownloadMigrationBackup(input *agentpb.BackupDownloadRequest, stream agentpb.AgentControl_DownloadMigrationBackupServer) error {
	if input == nil {
		return status.Error(codes.InvalidArgument, "migration is required")
	}
	credential, err := s.authenticate(stream.Context(), input.AgentId)
	if err != nil {
		return err
	}
	data, err := s.center.store.ThreeXUIMigrationBackup(stream.Context(), input.AgentId, credential, input.MigrationId)
	if err != nil {
		return status.Error(codes.FailedPrecondition, "backup is unavailable")
	}
	for len(data) > 0 {
		n := min(len(data), agentGRPCChunkSize)
		if err := stream.Send(&agentpb.DataChunk{Data: data[:n]}); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}
