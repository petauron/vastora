package pulse

import "errors"

type RestoreCredentials struct {
	NodeID string `json:"nodeId"`
	Token  string `json:"token"`
}

type RestoreResult struct {
	NodeID string `json:"nodeId"`
}

func (credentials RestoreCredentials) Validate() error {
	if !validNodeID(credentials.NodeID) || !validAgentToken(credentials.Token) {
		return errors.New("pulse: invalid original monitoring credentials")
	}
	return nil
}
