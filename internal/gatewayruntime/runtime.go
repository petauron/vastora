// Package gatewayruntime defines the single host-level gateway runtime shared
// by the Center deployment helper and the Agent.
package gatewayruntime

const (
	CaddyImage           = "docker.io/library/caddy:2.11.4@sha256:df7f1c2fb114453b951de51a98efc010db1655a92c2e86be6706714e2417a78d"
	CaddyContainer       = "vastora-gateway-caddy"
	CaddyAdminSocket     = "/run/vastora/caddy-admin.sock"
	CaddyComponentLabel  = "gateway"
	HAProxyContainer     = "vastora-gateway-haproxy"
	Layer4ComponentLabel = "layer4-gateway"

	ManagedLabel        = "io.vastora.managed"
	ComponentLabel      = "io.vastora.component"
	SystemServicesLabel = "io.vastora.system-services"
	SystemServices      = "center,headscale"

	// LegacyCenterCaddyContainer is removed by the forward runtime migration.
	// It must never be created by current code.
	LegacyCenterCaddyContainer = "vastora-center-gateway"
)
