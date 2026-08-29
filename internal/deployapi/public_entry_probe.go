package deployapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

type PublicEntryProbeRequest struct {
	BindAddress string `json:"bindAddress"`
}

type PublicEntryProbe struct {
	ID        string `json:"id"`
	Challenge string `json:"challenge"`
	Ports     []int  `json:"ports"`
	ExpiresAt string `json:"expiresAt"`
}

type PublicEntryProber interface {
	StartPublicEntryProbe(context.Context, PublicEntryProbeRequest) (PublicEntryProbe, error)
	StopPublicEntryProbe(context.Context, string) error
}

type InfrastructureManager interface {
	HeadscaleInstaller
	PublicEntryProber
	CenterRemoteAccessManager
}

func (client *Client) StartPublicEntryProbe(ctx context.Context, input PublicEntryProbeRequest) (PublicEntryProbe, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return PublicEntryProbe{}, err
	}
	body, err := client.request(ctx, http.MethodPost, "/v1/public-entry/probes", payload)
	if err != nil {
		return PublicEntryProbe{}, err
	}
	var result PublicEntryProbe
	if err := json.Unmarshal(body, &result); err != nil {
		return PublicEntryProbe{}, fmt.Errorf("center: decode public entry probe response: %w", err)
	}
	if result.ID == "" || len(result.Challenge) != 43 || len(result.Ports) != 2 || result.Ports[0] != 80 || result.Ports[1] != 443 || result.ExpiresAt == "" {
		return PublicEntryProbe{}, errors.New("center: deployment helper returned an incomplete public entry probe")
	}
	return result, nil
}

func (client *Client) StopPublicEntryProbe(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("center: public entry probe ID is required")
	}
	_, err := client.request(ctx, http.MethodDelete, "/v1/public-entry/probes/"+id, nil)
	return err
}

func (client *Client) request(ctx context.Context, method, path string, payload []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, "http://deployer"+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if len(payload) != 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("center: contact deployment helper: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("center: read deployment helper response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &failure) == nil && failure.Error != "" {
			return nil, errors.New(failure.Error)
		}
		return nil, fmt.Errorf("center: deployment helper returned HTTP %d", response.StatusCode)
	}
	return body, nil
}
