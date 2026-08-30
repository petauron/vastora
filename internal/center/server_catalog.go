package center

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/catalog"
)

func (s *Server) handleOfficialCatalog(writer http.ResponseWriter, request *http.Request) {
	envelope, err := s.store.OfficialCatalogEnvelope(request.Context())
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(envelope)
}

func (s *Server) handleListSources(writer http.ResponseWriter, request *http.Request) {
	sources, err := s.store.ListSources(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"sources": sources})
}

func (s *Server) handleCreateSource(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		ID             string `json:"id"`
		DisplayName    string `json:"displayName"`
		URL            string `json:"url"`
		PublicKey      string `json:"publicKey"`
		BearerToken    string `json:"bearerToken"`
		CustomCAPEM    string `json:"customCA"`
		RefreshSeconds int    `json:"refreshIntervalSeconds"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(input.PublicKey)
	if err != nil {
		writeError(writer, http.StatusBadRequest, errors.New("center: public key must be base64url"))
		return
	}
	if err := s.store.CreateSource(request.Context(), SourceInput{
		ID: input.ID, DisplayName: input.DisplayName, URL: input.URL, PublicKey: publicKey,
		BearerToken: input.BearerToken, CustomCAPEM: input.CustomCAPEM, RefreshSeconds: input.RefreshSeconds,
	}); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]string{"id": input.ID})
}

func (s *Server) handleUpdateSource(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		DisplayName    *string `json:"displayName"`
		URL            *string `json:"url"`
		PublicKey      *string `json:"publicKey"`
		BearerToken    *string `json:"bearerToken"`
		CustomCAPEM    *string `json:"customCA"`
		RefreshSeconds *int    `json:"refreshIntervalSeconds"`
		Enabled        *bool   `json:"enabled"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	var publicKey *[]byte
	if input.PublicKey != nil {
		decoded, err := base64.RawURLEncoding.DecodeString(*input.PublicKey)
		if err != nil {
			writeError(writer, http.StatusBadRequest, errors.New("center: public key must be base64url"))
			return
		}
		publicKey = &decoded
	}
	identifier := request.PathValue("id")
	if err := s.store.UpdateSource(request.Context(), identifier, SourceUpdate{
		DisplayName: input.DisplayName, URL: input.URL, PublicKey: publicKey, BearerToken: input.BearerToken,
		CustomCAPEM: input.CustomCAPEM, RefreshSeconds: input.RefreshSeconds, Enabled: input.Enabled,
	}); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"id": identifier})
}

func (s *Server) handleDeleteSource(writer http.ResponseWriter, request *http.Request) {
	if err := s.store.DeleteSource(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

type CatalogRefreshResult struct {
	SourceID    string `json:"sourceID"`
	Apps        int    `json:"apps,omitempty"`
	NotModified bool   `json:"notModified"`
}

func (s *Server) handleRefreshSource(writer http.ResponseWriter, request *http.Request) {
	identifier := request.PathValue("id")
	result, err := s.RefreshCatalogSource(request.Context(), identifier)
	if err != nil {
		writeError(writer, http.StatusBadGateway, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) RefreshCatalogSource(ctx context.Context, identifier string) (CatalogRefreshResult, error) {
	s.catalogRefreshMu.Lock()
	defer s.catalogRefreshMu.Unlock()

	if identifier == OfficialCatalogSourceID {
		if len(s.officialCatalog) == 0 {
			return CatalogRefreshResult{}, errors.New("center: official catalog is unavailable")
		}
		value, err := catalog.ParseCatalog(s.officialCatalog)
		if err != nil {
			return CatalogRefreshResult{}, err
		}
		if err := s.store.SeedOfficialCatalog(ctx, s.officialCatalog); err != nil {
			return CatalogRefreshResult{}, err
		}
		return CatalogRefreshResult{SourceID: identifier, Apps: len(value.Apps)}, nil
	}
	source, err := s.store.SourceForRefresh(ctx, identifier)
	if err != nil {
		return CatalogRefreshResult{}, err
	}
	result, err := catalog.Fetch(ctx, catalog.FetchConfig{
		URL: source.URL, PublicKey: ed25519.PublicKey(source.publicKey), BearerToken: source.bearerToken,
		CustomCAPEM: source.customCA, ETag: source.etag, LastModified: source.lastMod,
	})
	if err != nil {
		if recordErr := s.store.RecordCatalogErrorForRevision(ctx, identifier, source.generation, source.revision, err); recordErr != nil {
			return CatalogRefreshResult{}, errors.Join(err, recordErr)
		}
		return CatalogRefreshResult{}, err
	}
	if result.NotModified {
		if err := s.store.MarkCatalogNotModifiedForRevision(ctx, identifier, source.generation, source.revision, result.ETag, result.LastModified); err != nil {
			if recordErr := s.store.RecordCatalogErrorForRevision(ctx, identifier, source.generation, source.revision, err); recordErr != nil {
				return CatalogRefreshResult{}, errors.Join(err, recordErr)
			}
			return CatalogRefreshResult{}, err
		}
		return CatalogRefreshResult{SourceID: identifier, NotModified: true}, nil
	}
	if err := s.store.CommitCatalogRefresh(ctx, identifier, source.generation, source.revision, result.RawEnvelope, result.ETag, result.LastModified); err != nil {
		if recordErr := s.store.RecordCatalogErrorForRevision(ctx, identifier, source.generation, source.revision, err); recordErr != nil {
			return CatalogRefreshResult{}, errors.Join(err, recordErr)
		}
		return CatalogRefreshResult{}, err
	}
	return CatalogRefreshResult{SourceID: identifier, Apps: len(result.Catalog.Apps)}, nil
}

func (s *Server) RunCatalogRefresh(ctx context.Context, interval time.Duration, onError func(error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		ids, err := s.store.DueCatalogSourceIDs(ctx, s.store.now())
		if err != nil {
			if onError != nil {
				onError(err)
			}
			return
		}
		for _, id := range ids {
			refreshContext, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err := s.RefreshCatalogSource(refreshContext, id)
			cancel()
			if err != nil && ctx.Err() == nil && onError != nil {
				onError(fmt.Errorf("catalog %s: %w", id, err))
			}
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (s *Server) handleListApps(writer http.ResponseWriter, request *http.Request) {
	apps, err := s.store.ListApps(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"apps": apps})
}

func (s *Server) handleCreateRegistryCredential(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Host     string `json:"host"`
		Username string `json:"username"`
		Token    string `json:"token"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	credential, err := s.store.CreateRegistryCredential(request.Context(), input.Host, input.Username, input.Token)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, credential)
}

func (s *Server) handleListRegistryCredentials(writer http.ResponseWriter, request *http.Request) {
	credentials, err := s.store.ListRegistryCredentials(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"credentials": credentials})
}

func (s *Server) handleRotateRegistryCredential(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Username string `json:"username"`
		Token    string `json:"token"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	credential, err := s.store.RotateRegistryCredential(request.Context(), request.PathValue("id"), input.Username, input.Token)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, credential)
}

func (s *Server) handleDeleteRegistryCredential(writer http.ResponseWriter, request *http.Request) {
	if err := s.store.DeleteRegistryCredential(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"deleted": true})
}
