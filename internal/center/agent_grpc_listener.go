package center

import (
	"net/http"
	"strings"

	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/controlplane"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
)

const agentGRPCServicePath = "/vastora.agent.v1.AgentControl/"
const decommissionCallbackPrefix = "/api/v1/agent-decommission-results/"

// agentGRPCHandler serves the private Agent service on the existing Center
// listener. User-facing HTTP remains in the original handler. A public cleanup
// callback prefix can invoke only the task-bound callback method.
func (s *Server) agentGRPCHandler(web http.Handler) http.Handler {
	server := grpc.NewServer(grpc.MaxRecvMsgSize(controlplane.LegacyReceiptMaxPayloadBytes+(64<<10)), grpc.MaxSendMsgSize(controlplane.MaxEnvelopeWire), grpc.MaxConcurrentStreams(32))
	agentpb.RegisterAgentControlServer(server, newAgentGRPCService(s))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := strings.LastIndex(r.URL.Path, agentGRPCServicePath)
		if index < 0 || !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/grpc") {
			web.ServeHTTP(w, r)
			return
		}
		prefix, method := r.URL.Path[:index], r.URL.Path[index:]
		clone := r.Clone(r.Context())
		clone.URL.Path = method
		clone.URL.RawPath = ""
		clone.Header.Del("X-Vastora-Callback-Task-ID")
		if prefix != "" {
			if !strings.HasPrefix(prefix, decommissionCallbackPrefix) {
				http.NotFound(w, r)
				return
			}
			taskID := strings.TrimPrefix(prefix, decommissionCallbackPrefix)
			if taskID == "" || strings.Contains(taskID, "/") || method != agentpb.AgentControl_DecommissionCallback_FullMethodName {
				http.NotFound(w, r)
				return
			}
			clone.Header.Set("X-Vastora-Callback-Task-ID", taskID)
		}
		server.ServeHTTP(w, clone)
	})
	return h2c.NewHandler(handler, &http2.Server{MaxConcurrentStreams: 32})
}
