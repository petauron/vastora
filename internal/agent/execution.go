package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"

	"github.com/petauron/vastora/internal/controlplane"
)

type activeExecution struct{ cancel context.CancelCauseFunc }

// taskCompletion exists only for the current in-memory execution. Center owns
// durable results; Agent does not persist or replay an execution outbox.
type taskCompletion struct {
	TaskID                       string                `json:"taskId"`
	Attempt                      int64                 `json:"attempt"`
	Result                       ApplicationTaskResult `json:"result"`
	Error                        string                `json:"error"`
	ReconciliationRequired       bool                  `json:"reconciliationRequired"`
	ApplicationRuntimeGeneration int                   `json:"applicationRuntimeGeneration"`
}

// Current execution ownership is process-local, not persistent task history.
func (s *Store) beginExecution(parent context.Context) (context.Context, context.CancelCauseFunc, func(), error) {
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	if s.activeExecution != nil {
		return nil, nil, nil, errors.New("agent: another execution is still active")
	}
	ctx, cancel := context.WithCancelCause(parent)
	owner := &activeExecution{cancel: cancel}
	s.activeExecution = owner
	return ctx, cancel, func() {
		cancel(context.Canceled)
		s.executionMu.Lock()
		if s.activeExecution == owner {
			s.activeExecution = nil
		}
		s.executionMu.Unlock()
	}, nil
}

func (s *Store) stopActiveExecution(cause error) {
	if cause == nil {
		return
	}
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	if s.activeExecution != nil {
		s.activeExecution.cancel(cause)
	}
}

func newExecutionSessionID() (string, error) {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (c Client) registerExecutionSession(ctx context.Context, store *Store) error {
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	connection, err = c.ensureConnectionPinned(ctx, store, connection)
	if err != nil {
		return err
	}
	var response struct {
		Registered bool `json:"registered"`
	}
	err = c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/execution-session", controlplane.ExecutionSessionRequest{SessionID: c.executionSession, Protocol: controlplane.ExecutionProtocol}, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, &response)
	if err != nil {
		return err
	}
	if !response.Registered {
		return errors.New("agent: execution session was not registered")
	}
	return nil
}

func (c Client) executionTransition(ctx context.Context, store *Store, action, phase string, unknown bool, message string) error {
	if c.executionSession == "" || c.execution.ID == "" || c.execution.Protocol != controlplane.ExecutionProtocol {
		return errors.New("agent: execution authorization is required")
	}
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	var response struct {
		Recorded bool `json:"recorded"`
	}
	err = c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/executions/"+url.PathEscape(c.execution.ID), controlplane.ExecutionTransitionRequest{SessionID: c.executionSession, Action: action, Digest: c.execution.Digest, Phase: phase, Unknown: unknown, Error: message}, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, &response)
	if err != nil {
		return err
	}
	if !response.Recorded {
		return errors.New("agent: execution transition was not confirmed")
	}
	return nil
}
