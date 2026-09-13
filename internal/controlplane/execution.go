package controlplane

// ExecutionProtocol is deliberately not negotiated down. Center and Agent must
// be upgraded in a task-claim pause before changing the execution contract.
const ExecutionProtocol = 2

type ExecutionSessionRequest struct {
	SessionID string `json:"sessionId"`
	Protocol  int    `json:"protocol"`
}

type ExecutionTransitionRequest struct {
	SessionID string `json:"sessionId"`
	Action    string `json:"action"`
	Digest    string `json:"digest,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Unknown   bool   `json:"unknown,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ExecutionAuthorization identifies a single Center-persisted permission to
// execute. Neither task identity nor a successful heartbeat grants permission.
type ExecutionAuthorization struct {
	ID       string `json:"id"`
	Protocol int    `json:"protocol"`
	Digest   string `json:"digest"`
}

// ExecutionDisposition is an operator decision, never a retry policy.
type ExecutionDisposition struct {
	Action           string `json:"action"`
	ExecutionStopped bool   `json:"executionStopped"`
	Note             string `json:"note"`
}
