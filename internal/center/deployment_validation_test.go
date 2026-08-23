package center

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/catalog"
)

func TestValidateApplicationResultUsesThreeXUIWorkerServices(t *testing.T) {
	manifest := catalog.AppManifest{ID: "3x-ui", Services: []catalog.Service{
		{Name: "panel", Protocol: "http", ContainerPort: 2053, DefaultHostPort: 2053, HostPortField: "panel_port"},
		{Name: "subscription", Protocol: "http", ContainerPort: 2096, DefaultHostPort: 2096},
	}}
	config := json.RawMessage(`{"panel_port":2053}`)
	panel := ApplicationServiceResult{Name: "panel", Protocol: "http", ContainerPort: 2053, HostPort: 2053, Address: "100.64.0.3"}

	if err := validateApplicationResult(manifest, threeXUIAppKey, threeXUIRoleWorker, config, "100.64.0.3", ApplicationTaskResult{Services: []ApplicationServiceResult{panel}}); err != nil {
		t.Fatalf("worker panel result was rejected: %v", err)
	}

	subscription := ApplicationServiceResult{Name: "subscription", Protocol: "http", ContainerPort: 2096, HostPort: 2096, Address: "100.64.0.3"}
	if err := validateApplicationResult(manifest, threeXUIAppKey, threeXUIRoleWorker, config, "100.64.0.3", ApplicationTaskResult{Services: []ApplicationServiceResult{subscription}}); err == nil || !strings.Contains(err.Error(), "signed manifest") {
		t.Fatalf("worker subscription result error = %v", err)
	}

	if err := validateApplicationResult(manifest, threeXUIAppKey, threeXUIRoleMaster, config, "100.64.0.3", ApplicationTaskResult{Services: []ApplicationServiceResult{panel}}); err == nil || !strings.Contains(err.Error(), "signed manifest") {
		t.Fatalf("controller incomplete result error = %v", err)
	}
}
