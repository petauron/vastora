package pulse

import (
	"errors"

	"github.com/google/uuid"
)

type RestoreCredentials struct {
	NodeID string `json:"nodeId"`
	Token  string `json:"token"`
}

type RestoreResult struct {
	NodeID string `json:"nodeId"`
}

func (credentials RestoreCredentials) Validate() error {
	id, err := uuid.Parse(credentials.NodeID)
	if err != nil || id.String() != credentials.NodeID || !validAgentToken(credentials.Token) {
		return errors.New("pulse: invalid original monitoring credentials")
	}
	return nil
}
