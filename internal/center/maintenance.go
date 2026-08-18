package center

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DiagnosticCount struct {
	Total    int `json:"total"`
	Healthy  int `json:"healthy"`
	Warning  int `json:"warning"`
	Failed   int `json:"failed"`
	Disabled int `json:"disabled,omitempty"`
}

type Diagnostics struct {
	GeneratedAt  time.Time         `json:"generatedAt"`
	Version      string            `json:"version"`
	Schema       int               `json:"schema"`
	Nodes        DiagnosticCount   `json:"nodes"`
	Applications DiagnosticCount   `json:"applications"`
	Publications DiagnosticCount   `json:"publications"`
	Integrations []IntegrationView `json:"integrations"`
	RecentErrors []ActionView      `json:"recentErrors"`
}

func (s *Store) Diagnostics(ctx context.Context) (Diagnostics, error) {
	agents, err := s.ListAgents(ctx)
	if err != nil {
		return Diagnostics{}, err
	}
	applications, err := s.ListApplications(ctx)
	if err != nil {
		return Diagnostics{}, err
	}
	publications, err := s.ListPublications(ctx)
	if err != nil {
		return Diagnostics{}, err
	}
	integrations, err := s.ListIntegrations(ctx)
	if err != nil {
		return Diagnostics{}, err
	}
	actions, err := s.ListActions(ctx)
	if err != nil {
		return Diagnostics{}, err
	}
	result := Diagnostics{
		GeneratedAt:  s.now().UTC(),
		Version:      Version,
		Schema:       centerSchemaVersion,
		Integrations: integrations,
		RecentErrors: make([]ActionView, 0),
	}
	for _, agent := range agents {
		result.Nodes.Total++
		switch {
		case agent.Status == "disabled":
			result.Nodes.Disabled++
		case agent.Connected && agent.NetworkProfile != nil:
			result.Nodes.Healthy++
		default:
			result.Nodes.Warning++
		}
	}
	for _, application := range applications {
		if application.Status == "stopped" {
			continue
		}
		result.Applications.Total++
		switch application.Status {
		case "running", "ready":
			result.Applications.Healthy++
		case "failed", "degraded":
			result.Applications.Failed++
		default:
			result.Applications.Warning++
		}
	}
	for _, publication := range publications {
		if publication.Status == "stopped" {
			continue
		}
		result.Publications.Total++
		switch publication.Status {
		case "ready":
			result.Publications.Healthy++
		case "failed", "degraded":
			result.Publications.Failed++
		default:
			result.Publications.Warning++
		}
	}
	cutoff := result.GeneratedAt.Add(-24 * time.Hour)
	for _, action := range actions {
		if action.Event == "failed" && action.CreatedAt.After(cutoff) {
			result.RecentErrors = append(result.RecentErrors, action)
			if len(result.RecentErrors) == 20 {
				break
			}
		}
	}
	return result, nil
}

func (s *Server) handleDiagnostics(writer http.ResponseWriter, request *http.Request) {
	value, err := s.store.Diagnostics(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) handleCreateBackup(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if len(strings.TrimSpace(input.Password)) < 12 {
		writeError(writer, http.StatusBadRequest, errors.New("center: backup password must be at least 12 characters"))
		return
	}
	temporaryDir, err := os.MkdirTemp("", "vastora-web-backup-*")
	if err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Errorf("center: create backup workspace: %w", err))
		return
	}
	defer os.RemoveAll(temporaryDir)
	name := "vastora-center-" + s.store.now().UTC().Format("20060102-150405") + ".vastora"
	path := filepath.Join(temporaryDir, name)
	if err := s.store.Backup(request.Context(), path, input.Password); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Errorf("center: open backup download: %w", err))
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Errorf("center: inspect backup download: %w", err))
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	writer.Header().Set("Content-Type", "application/vnd.vastora.backup")
	http.ServeContent(writer, request, name, stat.ModTime(), file)
}

func countActiveAgents(agents []AgentView) int {
	count := 0
	for _, agent := range agents {
		if agent.Status == "active" {
			count++
		}
	}
	return count
}
