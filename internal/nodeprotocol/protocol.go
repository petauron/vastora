// Package nodeprotocol defines the managed protocols of one subscription node.
package nodeprotocol

import "errors"

const CommandKind = "3xui.protocols.configure"

type Selection struct {
	VLESS bool `json:"vless"`
	HY2   bool `json:"hy2"`
}

func (s Selection) Validate() error {
	if !s.VLESS && !s.HY2 {
		return errors.New("select at least one node protocol")
	}
	return nil
}

// HY2Tag is stable across retries and independent of a controller's numeric IDs.
func HY2Tag(vlessTag string) string { return vlessTag + "-hy2" }

type Task struct {
	Selection
	Phase             string `json:"phase"`
	RootCommandID     string `json:"rootCommandId"`
	ServiceID         string `json:"serviceId"`
	ApplicationID     string `json:"applicationId"`
	ControllerAgentID string `json:"controllerAgentId"`
	TargetAgentID     string `json:"targetAgentId"`
	HY2InboundID      int    `json:"hy2InboundId,omitempty"`
	InboundID         int    `json:"inboundId"`
	InboundTag        string `json:"inboundTag"`
	TargetNodeID      int    `json:"targetNodeId"`
	Hostname          string `json:"hostname"`
	DisplayName       string `json:"displayName"`
	// Certificate material is hydrated only when delivering to the controller.
	CertificatePEM string `json:"certificatePem,omitempty"`
	PrivateKeyPEM  string `json:"privateKeyPem,omitempty"`
}

type Result struct {
	Phase        string `json:"phase"`
	HY2InboundID int    `json:"hy2InboundId"`
}

type View struct {
	Selection
	CommandID string `json:"commandId,omitempty"`
	State     string `json:"state"`
}
