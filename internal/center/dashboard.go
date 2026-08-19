package center

import (
	"net/http"
)

type DashboardStatusView struct {
	Version                 string `json:"version"`
	CatalogSources          int    `json:"catalogSources"`
	CatalogApps             int    `json:"catalogApps"`
	Agents                  int    `json:"agents"`
	Deployments             int    `json:"deployments"`
	AgentInstallerAvailable bool   `json:"agentInstallerAvailable"`
	AgentConnectionMode     string `json:"agentConnectionMode"`
	AgentConnectURL         string `json:"agentConnectUrl"`
}

type DashboardView struct {
	Status        DashboardStatusView `json:"status"`
	Sources       []CatalogSource     `json:"sources"`
	Apps          []AppView           `json:"apps"`
	Agents        []AgentView         `json:"agents"`
	Deployments   []DeploymentView    `json:"deployments"`
	Organizations []OrganizationView  `json:"organizations"`
	Sites         []SiteView          `json:"sites"`
	Applications  []ApplicationView   `json:"applications"`
	Services      []ServiceView       `json:"services"`
	Publications  []PublicationView   `json:"publications"`
	Routes        []RouteView         `json:"routes"`
	Integrations  []IntegrationView   `json:"integrations"`
	Actions       []ActionView        `json:"actions"`
}

func (s *Server) handleDashboard(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	networkConfig, err := s.store.CenterNetworkConfig(ctx)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	value := DashboardView{}
	if value.Sources, err = s.store.ListSources(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Apps, err = s.store.ListApps(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Agents, err = s.store.ListAgents(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Deployments, err = s.store.ListDeployments(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Organizations, err = s.store.ListOrganizations(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Sites, err = s.store.ListSites(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Applications, err = s.store.ListApplications(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Services, err = s.store.ListServices(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Publications, err = s.store.listPublications(ctx, value.Apps); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Routes, err = s.store.ListRoutes(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Integrations, err = s.store.ListIntegrations(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if value.Actions, err = s.store.ListActions(ctx); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	value.Status = DashboardStatusView{
		Version:                 Version,
		CatalogSources:          len(value.Sources),
		CatalogApps:             len(value.Apps),
		Agents:                  countActiveAgents(value.Agents),
		Deployments:             len(value.Deployments),
		AgentInstallerAvailable: s.agentInstallerAvailable(),
		AgentConnectionMode:     networkConfig.AgentConnectionMode,
		AgentConnectURL:         networkConfig.AgentConnectURL,
	}
	writeJSON(writer, http.StatusOK, value)
}
