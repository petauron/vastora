package agent

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/petauron/vastora/internal/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const grpcFileChunkSize = 64 << 10
const grpcAgentBinaryMaxSize = 256 << 20

type agentBinaryReader struct {
	stream   agentpb.AgentControl_DownloadAgentBinaryClient
	release  func()
	pending  []byte
	size     int64
	received int64
	done     bool
}

func (r *agentBinaryReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 {
		if r.done {
			return 0, io.EOF
		}
		frame, err := r.stream.Recv()
		if errors.Is(err, io.EOF) {
			r.done = true
			if r.received != r.size {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, io.EOF
		}
		if err != nil {
			// A received header proves the TLS handshake and authentication have
			// succeeded. Retry only an interrupted body, never a TLS failure.
			if status.Code(err) == codes.Unavailable {
				return 0, fmt.Errorf("%w: %v", io.ErrUnexpectedEOF, err)
			}
			return 0, agentGRPCError(err)
		}
		chunk := frame.GetChunk()
		if chunk == nil || len(chunk.Data) == 0 || len(chunk.Data) > grpcFileChunkSize || r.received+int64(len(chunk.Data)) > r.size {
			return 0, errors.New("agent: invalid Agent binary chunk")
		}
		r.received += int64(len(chunk.Data))
		r.pending = chunk.Data
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (r *agentBinaryReader) Close() error {
	if r.release != nil {
		r.release()
		r.release = nil
	}
	return nil
}

// OpenAgentBinary returns an authenticated, bounded binary stream and the
// metadata required for the caller's existing checksum/version validation.
func OpenAgentBinary(ctx context.Context, connection Connection, operatingSystem, architecture string) (io.ReadCloser, string, string, int64, error) {
	conn, release, err := (Client{}).grpcConnection(ctx, connection, false)
	if err != nil {
		return nil, "", "", 0, err
	}
	streamContext, cancel := context.WithCancel(grpcAuthenticatedContext(ctx, connection.Credential))
	closeStream := func() { cancel(); release() }
	stream, err := agentGRPCClient(conn).DownloadAgentBinary(streamContext, &agentpb.BinaryRequest{AgentId: connection.AgentID, OperatingSystem: operatingSystem, Architecture: architecture})
	if err != nil {
		closeStream()
		return nil, "", "", 0, agentGRPCError(err)
	}
	first, err := stream.Recv()
	if err != nil {
		headers, headerErr := stream.Header()
		closeStream()
		if headerErr == nil && status.Code(err) == codes.Unavailable {
			values := headers.Get("x-vastora-binary-authenticated")
			if len(values) == 1 && values[0] == "1" {
				return nil, "", "", 0, fmt.Errorf("%w: authenticated binary transfer interrupted", io.ErrUnexpectedEOF)
			}
		}
		return nil, "", "", 0, agentGRPCError(err)
	}
	header := first.GetHeader()
	if header == nil || header.Version == "" || header.Size <= 0 || header.Size > grpcAgentBinaryMaxSize || len(header.Sha256) != 64 {
		closeStream()
		return nil, "", "", 0, errors.New("agent: Center returned invalid Agent binary metadata")
	}
	if _, err := hex.DecodeString(header.Sha256); err != nil {
		closeStream()
		return nil, "", "", 0, errors.New("agent: Center returned invalid Agent binary digest")
	}
	return &agentBinaryReader{stream: stream, release: closeStream, size: header.Size}, header.Version, header.Sha256, header.Size, nil
}

func (c Client) grpcUploadThreeXUIBackup(ctx context.Context, connection Connection, applicationID string, revision int64, content []byte) error {
	if len(content) < 16 || len(content) > threeXUIControllerBackupMaxSize {
		return errors.New("agent: invalid 3x-ui restore point")
	}
	conn, release, err := c.grpcConnection(ctx, connection, false)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(grpcAuthenticatedContext(ctx, connection.Credential), 2*time.Minute)
	defer cancel()
	stream, err := agentGRPCClient(conn).UploadMigrationBackup(ctx)
	if err != nil {
		return agentGRPCError(err)
	}
	if err := stream.Send(&agentpb.BackupUploadFrame{Body: &agentpb.BackupUploadFrame_Header{Header: &agentpb.BackupUploadHeader{AgentId: connection.AgentID, ApplicationId: applicationID, Revision: revision}}}); err != nil {
		return agentGRPCError(err)
	}
	for len(content) > 0 {
		n := min(len(content), grpcFileChunkSize)
		if err := stream.Send(&agentpb.BackupUploadFrame{Body: &agentpb.BackupUploadFrame_Chunk{Chunk: &agentpb.DataChunk{Data: content[:n]}}}); err != nil {
			return agentGRPCError(err)
		}
		content = content[n:]
	}
	_, err = stream.CloseAndRecv()
	return agentGRPCError(err)
}

func (c Client) grpcDownloadThreeXUIBackup(ctx context.Context, connection Connection, migrationID string) ([]byte, error) {
	conn, release, err := c.grpcConnection(ctx, connection, false)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(grpcAuthenticatedContext(ctx, connection.Credential), 2*time.Minute)
	defer cancel()
	stream, err := agentGRPCClient(conn).DownloadMigrationBackup(ctx, &agentpb.BackupDownloadRequest{AgentId: connection.AgentID, MigrationId: migrationID})
	if err != nil {
		return nil, agentGRPCError(err)
	}
	var content []byte
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, agentGRPCError(err)
		}
		if chunk == nil || len(chunk.Data) == 0 || len(chunk.Data) > grpcFileChunkSize || len(content)+len(chunk.Data) > threeXUIControllerBackupMaxSize {
			return nil, errors.New("agent: invalid 3x-ui restore point chunk")
		}
		content = append(content, chunk.Data...)
	}
	if len(content) < 16 || string(content[:16]) != "SQLite format 3\x00" {
		return nil, errors.New("agent: Center returned an invalid 3x-ui migration restore point")
	}
	return content, nil
}
