package deployapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

const CloudflaredImage = "docker.io/cloudflare/cloudflared:2026.7.2@sha256:4f6655284ab3d252b7f28fedb19fe6c8fc82ee5b1295c20ac74d475e5398a52d"

type CenterRemoteAccessRequest struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token,omitempty"`
}

type CenterRemoteAccessManager interface {
	ApplyCenterRemoteAccess(context.Context, CenterRemoteAccessRequest) error
}

func (client *Client) ApplyCenterRemoteAccess(ctx context.Context, input CenterRemoteAccessRequest) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	_, err = client.request(ctx, http.MethodPut, "/v1/center/remote-access", payload)
	if err != nil {
		return fmt.Errorf("center: configure remote access runtime: %w", err)
	}
	return nil
}

func ValidateCenterRemoteAccessRequest(input CenterRemoteAccessRequest) error {
	if input.Enabled && len(input.Token) < 20 {
		return errors.New("deployer: Center remote access requires a valid Tunnel token")
	}
	if !input.Enabled && input.Token != "" {
		return errors.New("deployer: disabled Center remote access cannot include a Tunnel token")
	}
	return nil
}
