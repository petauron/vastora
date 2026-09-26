package agent

import (
	"encoding/json"
	"errors"
)

type cpaConfig struct {
	Timezone string `json:"timezone"`
	Debug    bool   `json:"debug"`
}

type cpaSecrets struct {
	ManagementKey string `json:"management_key"`
	APIKey        string `json:"api_key"`
}

func decodeCPAConfig(rawConfig, rawSecrets json.RawMessage) (cpaConfig, cpaSecrets, error) {
	var config cpaConfig
	var secrets cpaSecrets
	if json.Unmarshal(rawConfig, &config) != nil || json.Unmarshal(rawSecrets, &secrets) != nil {
		return config, secrets, errors.New("agent: invalid CPA configuration")
	}
	if config.Timezone == "" || secrets.ManagementKey == "" || secrets.APIKey == "" {
		return config, secrets, errors.New("agent: incomplete CPA configuration")
	}
	return config, secrets, nil
}
