package deployapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

type CenterUpdateExecution struct {
	Available     bool   `json:"available"`
	State         string `json:"state"`
	TargetVersion string `json:"targetVersion,omitempty"`
	Message       string `json:"message,omitempty"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
}

type CenterUpdateRequest struct {
	Version          string `json:"version"`
	InstallerBaseURL string `json:"installerBaseUrl"`
	InstallerHost    string `json:"installerHost"`
	InstallerPort    string `json:"installerPort"`
	InstallerAddress string `json:"installerAddress"`
}

type CenterUpdater interface {
	CenterUpdateStatus(context.Context) (CenterUpdateExecution, error)
	StartCenterUpdate(context.Context, CenterUpdateRequest) (CenterUpdateExecution, error)
}

func (client *Client) CenterUpdateStatus(ctx context.Context) (CenterUpdateExecution, error) {
	body, err := client.request(ctx, http.MethodGet, "/v1/center/update", nil)
	if err != nil {
		return CenterUpdateExecution{}, err
	}
	return decodeCenterUpdateExecution(body)
}

func (client *Client) StartCenterUpdate(ctx context.Context, input CenterUpdateRequest) (CenterUpdateExecution, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return CenterUpdateExecution{}, err
	}
	body, err := client.request(ctx, http.MethodPost, "/v1/center/update", payload)
	if err != nil {
		return CenterUpdateExecution{}, err
	}
	return decodeCenterUpdateExecution(body)
}

func decodeCenterUpdateExecution(body []byte) (CenterUpdateExecution, error) {
	var result CenterUpdateExecution
	if err := json.Unmarshal(body, &result); err != nil {
		return CenterUpdateExecution{}, fmt.Errorf("center: decode update helper response: %w", err)
	}
	if result.State == "" {
		return CenterUpdateExecution{}, errors.New("center: update helper returned an incomplete result")
	}
	return result, nil
}
