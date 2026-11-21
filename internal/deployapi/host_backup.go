package deployapi

import (
	"context"
	"encoding/json"
	"net/http"
)

// HostBackupSettings configure the host runner, not the Center database export.
type HostBackupSettings struct {
	Enabled      bool   `json:"enabled"`
	Directory    string `json:"directory"`
	TimeUTC      string `json:"timeUTC"`
	IntervalDays int    `json:"intervalDays"`
	KeepLast     int    `json:"keepLast"`
}

type HostBackupInput struct {
	HostBackupSettings
	Password string `json:"password,omitempty"`
}

type HostBackupStatus struct {
	Available   bool               `json:"available"`
	PasswordSet bool               `json:"passwordSet"`
	Settings    HostBackupSettings `json:"settings"`
	Execution   json.RawMessage    `json:"execution"`
	Coverage    json.RawMessage    `json:"coverage"`
	Snapshots   json.RawMessage    `json:"snapshots"`
}

type HostBackupManager interface {
	HostBackupStatus(context.Context) (HostBackupStatus, error)
	ConfigureHostBackup(context.Context, HostBackupInput) (HostBackupStatus, error)
	StartHostBackup(context.Context) (HostBackupStatus, error)
}

func (client *Client) HostBackupStatus(ctx context.Context) (HostBackupStatus, error) {
	return client.hostBackupRequest(ctx, http.MethodGet, nil)
}
func (client *Client) ConfigureHostBackup(ctx context.Context, input HostBackupInput) (HostBackupStatus, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return HostBackupStatus{}, err
	}
	return client.hostBackupRequest(ctx, http.MethodPut, payload)
}
func (client *Client) StartHostBackup(ctx context.Context) (HostBackupStatus, error) {
	return client.hostBackupRequest(ctx, http.MethodPost, []byte(`{}`))
}
func (client *Client) hostBackupRequest(ctx context.Context, method string, payload []byte) (HostBackupStatus, error) {
	body, err := client.request(ctx, method, "/v1/host-backup", payload)
	if err != nil {
		return HostBackupStatus{}, err
	}
	var result HostBackupStatus
	err = json.Unmarshal(body, &result)
	return result, err
}
