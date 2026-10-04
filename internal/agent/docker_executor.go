package agent

import (
	"context"

	"github.com/petauron/vastora/internal/pulse"
)

const (
	threeXUIKey                  = "vastora-official/3x-ui"
	meridianKey                  = "vastora-official/meridian"
	threeXUIContainer            = "vastora-3x-ui"
	xrayWorkerContainer          = "vastora-xray"
	meridianXrayContainer        = "meridian-xray"
	threeXUIDatabaseVolume       = "vastora-3x-ui-db"
	cpaKey                       = "vastora-official/cpa"
	cpaContainer                 = "vastora-cpa"
	cpaNetwork                   = "vastora-cpa-network"
	keeperKey                    = "vastora-official/keeper"
	keeperContainer              = "vastora-cpa-usage-keeper"
	komariKey                    = "vastora-official/komari-agent"
	applicationDeploymentIDLabel = "io.vastora.application.deployment-id"
)

var applicationVolumes = map[string][]string{
	threeXUIKey:      {threeXUIDatabaseVolume, "vastora-3x-ui-cert", "vastora-3x-ui-acme"},
	cpaKey:           {"vastora-cpa-auths", "vastora-cpa-logs", "vastora-cpa-plugins"},
	keeperKey:        {"vastora-cpa-keeper-data"},
	pulse.ServiceKey: {"vastora-pulse-data"},
}

type ApplicationExecutor struct {
	PackageBackend        PackageBackend
	PackageStateDirectory string
	DockerSocket          string
	Host                  any
	Store                 *Store
}

func (e ApplicationExecutor) Deploy(ctx context.Context, task DeploymentTask) (ApplicationTaskResult, error) {
	return e.deployPackage(ctx, task)
}

func proxyRuntimeApp(appKey string) bool {
	return appKey == meridianKey || appKey == threeXUIKey
}
