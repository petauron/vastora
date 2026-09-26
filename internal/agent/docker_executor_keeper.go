package agent

import (
	"encoding/json"
	"errors"
)

type keeperConfig struct {
	Timezone string `json:"timezone"`
}

type keeperSecrets struct {
	LoginPassword    string `json:"login_password"`
	CPAManagementKey string `json:"cpa_management_key"`
}

func decodeKeeperConfig(rawConfig, rawSecrets json.RawMessage) (keeperConfig, keeperSecrets, error) {
	var config keeperConfig
	var secrets keeperSecrets
	if json.Unmarshal(rawConfig, &config) != nil || json.Unmarshal(rawSecrets, &secrets) != nil || config.Timezone == "" || secrets.LoginPassword == "" || secrets.CPAManagementKey == "" {
		return config, secrets, errors.New("agent: incomplete Keeper configuration")
	}
	return config, secrets, nil
}
