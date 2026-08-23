package center

import (
	"encoding/json"
	"errors"

	"github.com/petauron/vastora/internal/catalog"
)

func validateApplicationResult(manifest catalog.AppManifest, appKey, role string, configJSON []byte, serviceAddress string, result ApplicationTaskResult) error {
	services := make(map[string]catalog.Service, len(manifest.Services))
	for _, service := range manifest.Services {
		if appKey == threeXUIAppKey && role == threeXUIRoleWorker && service.Name == "subscription" {
			continue
		}
		services[service.Name] = service
	}
	if len(result.Services) != len(services) {
		return errors.New("center: Agent service result does not match the signed manifest")
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(configJSON, &config) != nil {
		return errors.New("center: stored deployment configuration is invalid")
	}
	seen := make(map[string]bool, len(result.Services))
	for _, reported := range result.Services {
		declared, expected := services[reported.Name]
		if !expected || seen[reported.Name] || reported.Protocol != declared.Protocol || reported.ContainerPort != declared.ContainerPort {
			return errors.New("center: Agent service result does not match the signed manifest")
		}
		seen[reported.Name] = true
		expectedPort := declared.DefaultHostPort
		if declared.HostPortField != "" && json.Unmarshal(config[declared.HostPortField], &expectedPort) != nil {
			return errors.New("center: stored service port is invalid")
		}
		if reported.HostPort != expectedPort {
			return errors.New("center: Agent reported an unexpected service port")
		}
		expectedAddress := "127.0.0.1"
		if serviceAddress != "" {
			expectedAddress = serviceAddress
		}
		if reported.Address != expectedAddress {
			return errors.New("center: Agent reported a service outside its confirmed private service address")
		}
	}
	return nil
}
