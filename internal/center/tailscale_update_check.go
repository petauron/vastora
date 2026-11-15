package center

import (
	"net/http"
	"time"
)

// The generation is a best-effort request to check again, not a task or a host
// mutation. Each connected Agent observes it on its next heartbeat.
func (s *Server) handleCheckTailscaleUpdates(w http.ResponseWriter, r *http.Request) {
	s.tailscaleCheckGeneration.Store(time.Now().UnixNano())
	writeJSON(w, http.StatusAccepted, map[string]bool{"requested": true})
}
