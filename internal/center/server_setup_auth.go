package center

import (
	"errors"
	"net/http"

	"github.com/petauron/vastora/internal/networking"
)

func (s *Server) handleSetupStatus(writer http.ResponseWriter, request *http.Request) {
	status, err := s.store.SetupStatus(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	cloudflare := IntegrationView{Kind: "cloudflare", Status: "disabled"}
	publicAddresses := make([]networking.Candidate, 0)
	gatewayAddresses := make([]networking.Candidate, 0)
	observedPublicAddress := ""
	suggestedGatewayAddress := ""
	publicAddressDetection := "unavailable"
	authenticated := false
	loginContext, err := s.loginContext(request.Context(), request)
	if err != nil {
		writeError(writer, http.StatusForbidden, err)
		return
	}
	if cookie, cookieErr := request.Cookie("vastora_session"); cookieErr == nil && s.store.ValidateSession(request.Context(), cookie.Value, "", false) == nil {
		authenticated = true
		cloudflare, err = s.store.Integration(request.Context(), "cloudflare")
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		candidates, discoverErr := s.store.discoverNetworkCandidates(s.store.now().UTC())
		if discoverErr != nil {
			writeError(writer, http.StatusInternalServerError, discoverErr)
			return
		}
		for _, candidate := range candidates {
			if candidate.Kind == networking.KindPublic {
				publicAddresses = append(publicAddresses, candidate)
			}
			if candidate.Kind == networking.KindPublic || candidate.Kind == networking.KindLAN {
				gatewayAddresses = append(gatewayAddresses, candidate)
			}
		}
		if len(publicAddresses) > 0 {
			observedPublicAddress = publicAddresses[0].Address
			suggestedGatewayAddress = publicAddresses[0].Address
			publicAddressDetection = "direct"
		}
		if s.infrastructure != nil && s.store.lookupPublicAddress != nil {
			if observed, lookupErr := s.store.lookupPublicAddress(request.Context()); lookupErr == nil {
				observedPublicAddress = observed
				publicAddressDetection = "cloud_mapping_candidate"
				for _, candidate := range publicAddresses {
					if candidate.Address == observed {
						suggestedGatewayAddress = observed
						publicAddressDetection = "direct"
						break
					}
				}
				if publicAddressDetection == "cloud_mapping_candidate" {
					if address, routeErr := s.store.lookupGatewayAddress(observed); routeErr == nil {
						for _, candidate := range gatewayAddresses {
							if candidate.Address == address {
								suggestedGatewayAddress = address
								break
							}
						}
					}
				}
			}
		}
	}
	setupOperationPhase := ""
	setupLastError := ""
	if authenticated {
		setupOperationPhase = status.OperationPhase
		setupLastError = status.LastError
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"administratorConfigured":       status.AdministratorConfigured,
		"onboardingComplete":            status.OnboardingComplete,
		"suggestedAgentConnectUrl":      s.setupAgentConnectURL,
		"builtinHeadscaleAvailable":     s.infrastructure != nil,
		"cloudflareOAuthAvailable":      s.store.CloudflareOAuthAvailable(),
		"publicNetworkHelperAvailable":  s.store.lookupPublicAddress != nil && s.store.verifyPublicEntry != nil,
		"regionLookupAvailable":         s.store.lookupPublicRegion != nil,
		"cloudflareConfigured":          cloudflare.Status == "configured" && cloudflare.Mode == "oauth",
		"cloudflareAccessConfigured":    cloudflare.Status == "configured" && cloudflare.Mode == "oauth" && cloudflare.AccessManagement,
		"cloudflareTurnstileConfigured": cloudflare.Status == "configured" && cloudflare.Mode == "oauth" && cloudflare.TurnstileManagement,
		"cloudflareZone":                cloudflare.Endpoint,
		"publicAddressCandidates":       publicAddresses,
		"gatewayAddressCandidates":      gatewayAddresses,
		"observedPublicAddress":         observedPublicAddress,
		"suggestedGatewayAddress":       suggestedGatewayAddress,
		"publicAddressDetection":        publicAddressDetection,
		"setupOperationPhase":           setupOperationPhase,
		"setupLastError":                setupLastError,
		"loginProtection":               loginContext.Protection,
	})
}

func (s *Server) handleSetupAdmin(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	session, csrf, err := s.store.CreateFirstAdmin(request.Context(), input.Username, input.Password)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.setSessionCookies(writer, request, session, csrf)
	writeJSON(writer, http.StatusCreated, map[string]bool{"administratorConfigured": true})
}

func (s *Server) handleSetupComplete(writer http.ResponseWriter, request *http.Request) {
	var input InitialSetupInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	result, err := s.CompleteInitialSetup(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (s *Server) handleLogin(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Username       string `json:"username"`
		Password       string `json:"password"`
		TurnstileToken string `json:"turnstileToken,omitempty"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	loginContext, err := s.loginContext(request.Context(), request)
	if err != nil {
		writeLoginError(writer, http.StatusForbidden, "login_protection_unavailable", "center: login protection is unavailable", 0, true)
		return
	}
	throttle, err := s.store.LoginThrottle(request.Context(), input.Username, loginContext.ClientAddress)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if throttle.RetryAfter > 0 {
		writeLoginError(writer, http.StatusTooManyRequests, "login_throttled", "center: too many sign-in attempts; wait before retrying", throttle.RetryAfter, loginContext.Protection.CaptchaRequired)
		return
	}
	if loginContext.Protection.CaptchaRequired {
		if err := s.store.verifyCenterLoginTurnstile(request.Context(), input.TurnstileToken, loginContext.ClientAddress, loginContext.Hostname); err != nil {
			throttle, recordErr := s.store.RecordLoginFailure(request.Context(), input.Username, loginContext.ClientAddress, false)
			if recordErr != nil {
				writeError(writer, http.StatusInternalServerError, recordErr)
				return
			}
			writeLoginError(writer, http.StatusForbidden, "captcha_failed", "center: security check failed; complete a new challenge", throttle.RetryAfter, true)
			return
		}
	}
	session, csrf, err := s.store.Authenticate(request.Context(), input.Username, input.Password)
	if err != nil {
		throttle, recordErr := s.store.RecordLoginFailure(request.Context(), input.Username, loginContext.ClientAddress, true)
		if recordErr != nil {
			writeError(writer, http.StatusInternalServerError, recordErr)
			return
		}
		writeLoginError(writer, http.StatusUnauthorized, "invalid_credentials", "center: sign-in failed", throttle.RetryAfter, loginContext.Protection.CaptchaRequired)
		return
	}
	if err := s.store.ClearLoginFailures(request.Context(), input.Username, loginContext.ClientAddress); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	s.setSessionCookies(writer, request, session, csrf)
	writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) handleLogout(writer http.ResponseWriter, request *http.Request) {
	cookie, err := request.Cookie("vastora_session")
	if err != nil {
		writeError(writer, http.StatusUnauthorized, errors.New("center: authentication required"))
		return
	}
	if err := s.store.Logout(request.Context(), cookie.Value); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	s.clearSessionCookies(writer, request)
	writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *Server) handleChangePassword(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	cookie, err := request.Cookie("vastora_session")
	if err != nil {
		writeError(writer, http.StatusUnauthorized, errors.New("center: authentication required"))
		return
	}
	if err := s.store.ChangePassword(request.Context(), cookie.Value, input.CurrentPassword, input.NewPassword); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"changed": true})
}

func (s *Server) handleStatus(writer http.ResponseWriter, request *http.Request) {
	networkConfig, err := s.store.CenterNetworkConfig(request.Context())
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"version":                 Version,
		"agentInstallerAvailable": s.agentInstallerAvailable(),
		"agentConnectionMode":     networkConfig.AgentConnectionMode,
		"agentConnectUrl":         networkConfig.AgentConnectURL,
	})
}
