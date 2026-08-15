package master

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
)

const Version = "0.1.0-dev"

type Server struct {
	store         *Store
	staticDir     string
	secureCookies bool
}

func NewServer(store *Store, staticDir string, secureCookies bool) *Server {
	return &Server{store: store, staticDir: staticDir, secureCookies: secureCookies}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/v1/setup/status", s.handleSetupStatus)
	mux.HandleFunc("POST /api/v1/setup/admin", s.handleSetupAdmin)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireAuth(true, s.handleLogout))
	mux.HandleFunc("GET /api/v1/status", s.requireAuth(false, s.handleStatus))
	mux.HandleFunc("GET /api/v1/catalog/sources", s.requireAuth(false, s.handleListSources))
	mux.HandleFunc("POST /api/v1/catalog/sources", s.requireAuth(true, s.handleCreateSource))
	mux.HandleFunc("POST /api/v1/catalog/sources/{id}/refresh", s.requireAuth(true, s.handleRefreshSource))
	mux.HandleFunc("GET /api/v1/catalog/apps", s.requireAuth(false, s.handleListApps))
	mux.HandleFunc("POST /api/v1/registry-credentials", s.requireAuth(true, s.handleCreateRegistryCredential))
	mux.Handle("/", s.staticHandler())
	return securityHeaders(mux)
}

func (s *Server) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok", "version": Version})
}

func (s *Server) handleSetupStatus(writer http.ResponseWriter, request *http.Request) {
	configured, err := s.store.SetupStatus(request.Context())
	if err != nil && !configured {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"configured": configured})
}

func (s *Server) handleSetupAdmin(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		BootstrapToken string `json:"bootstrapToken"`
		Password       string `json:"password"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	session, csrf, err := s.store.CreateFirstAdmin(request.Context(), input.BootstrapToken, input.Password)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	s.setSessionCookies(writer, session, csrf)
	writeJSON(writer, http.StatusCreated, map[string]bool{"configured": true})
}

func (s *Server) handleLogin(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	session, csrf, err := s.store.Authenticate(request.Context(), input.Password)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	s.setSessionCookies(writer, session, csrf)
	writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) handleLogout(writer http.ResponseWriter, _ *http.Request) {
	s.clearSessionCookies(writer)
	writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *Server) handleStatus(writer http.ResponseWriter, request *http.Request) {
	sources, err := s.store.ListSources(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	apps, err := s.store.ListApps(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"version":        Version,
		"catalogSources": len(sources),
		"catalogApps":    len(apps),
		"nodes":          0,
		"deployments":    0,
	})
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
		writeError(writer, http.StatusBadRequest, errors.New("master: public key must be base64url"))
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

func (s *Server) requireAuth(mutation bool, handler http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("vastora_session")
		if err != nil {
			writeError(writer, http.StatusUnauthorized, errors.New("master: authentication required"))
			return
		}
		if err := s.store.ValidateSession(request.Context(), cookie.Value, request.Header.Get("X-CSRF-Token"), mutation); err != nil {
			writeError(writer, http.StatusUnauthorized, err)
			return
		}
		handler(writer, request)
	}
}

func (s *Server) setSessionCookies(writer http.ResponseWriter, session, csrf string) {
	expires := time.Now().Add(sessionLifetime)
	http.SetCookie(writer, &http.Cookie{Name: "vastora_session", Value: session, Path: "/", Expires: expires, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secureCookies})
	http.SetCookie(writer, &http.Cookie{Name: "vastora_csrf", Value: csrf, Path: "/", Expires: expires, HttpOnly: false, SameSite: http.SameSiteStrictMode, Secure: s.secureCookies})
}

func (s *Server) clearSessionCookies(writer http.ResponseWriter) {
	for _, name := range []string{"vastora_session", "vastora_csrf"} {
		http.SetCookie(writer, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: name == "vastora_session", SameSite: http.SameSiteStrictMode, Secure: s.secureCookies})
	}
}

func (s *Server) staticHandler() http.Handler {
	if s.staticDir == "" {
		return http.NotFoundHandler()
	}
	index := filepath.Join(s.staticDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		return http.NotFoundHandler()
	}
	files := http.FileServer(spaFileSystem{root: http.Dir(s.staticDir)})
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.NotFound(writer, request)
			return
		}
		files.ServeHTTP(writer, request)
	})
}

type spaFileSystem struct {
	root http.FileSystem
}

func (files spaFileSystem) Open(name string) (http.File, error) {
	file, err := files.root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return files.root.Open("/index.html")
	}
	return file, err
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; object-src 'none'")
		next.ServeHTTP(writer, request)
	})
}

func decodeJSON(request *http.Request, target any) error {
	if !strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
		return errors.New("master: Content-Type must be application/json")
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("master: decode JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("master: request must contain one JSON value")
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	message := "request failed"
	if err != nil {
		message = err.Error()
	}
	writeJSON(writer, status, map[string]string{"error": message})
}

// ContextWithTimeout is exposed for callers that refresh multiple sources in
// parallel without leaking request cancellation across jobs.
func ContextWithTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 15*time.Second)
}
