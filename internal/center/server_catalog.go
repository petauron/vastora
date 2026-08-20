package center

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"

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

func (s *Server) handleRefreshSource(writer http.ResponseWriter, request *http.Request) {
	identifier := request.PathValue("id")
	if identifier == OfficialCatalogSourceID {
		if len(s.officialCatalog) == 0 {
			writeError(writer, http.StatusNotFound, errors.New("center: official catalog is unavailable"))
			return
		}
		if err := s.store.SeedOfficialCatalog(request.Context(), s.officialCatalog); err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"sourceID": identifier, "apps": 0, "notModified": false})
		return
	}
	source, err := s.store.SourceForRefresh(request.Context(), identifier)
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	result, err := catalog.Fetch(request.Context(), catalog.FetchConfig{
		URL: source.URL, PublicKey: ed25519.PublicKey(source.publicKey), BearerToken: source.bearerToken,
		CustomCAPEM: source.customCA, ETag: source.etag, LastModified: source.lastMod,
	})
	if err != nil {
		_ = s.store.RecordCatalogError(request.Context(), identifier, err)
		writeError(writer, http.StatusBadGateway, err)
		return
	}
	if result.NotModified {
		if err := s.store.ClearCatalogError(request.Context(), identifier); err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"sourceID": identifier, "notModified": true})
		return
	}
	if err := s.store.SaveCatalog(request.Context(), identifier, result.RawEnvelope, result.ETag, result.LastModified); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"sourceID": identifier, "apps": len(result.Catalog.Apps), "notModified": false})
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
