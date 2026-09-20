package center

import "strings"

// Agent updates only depend on the running Center and its packaged binaries.
// Gateway, application and listener reconciliation are separate node-scoped
// concerns and must never block healthy Agents from receiving an update.
func (s *Server) agentUpdateRolloutAvailable() bool {
	return s.startupReady.Load() && strings.TrimSpace(s.agentBinariesDir) != ""
}
