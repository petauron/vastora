package center

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	cloudflareTurnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	centerLoginTurnstileAction   = "center_login"
)

type LoginProtectionView struct {
	CaptchaRequired  bool   `json:"captchaRequired"`
	TurnstileSiteKey string `json:"turnstileSiteKey,omitempty"`
	MaxFailures      int    `json:"maxFailures"`
	LockoutSeconds   int    `json:"lockoutSeconds"`
}

type loginRequestContext struct {
	ClientAddress string
	Hostname      string
	Protection    LoginProtectionView
}

func (s *Server) loginContext(ctx context.Context, request *http.Request) (loginRequestContext, error) {
	result := loginRequestContext{ClientAddress: requestRemoteIP(request), Protection: LoginProtectionView{MaxFailures: loginMaxFailures, LockoutSeconds: int(loginLockoutDuration / time.Second)}}
	record, exists, err := s.store.centerRemoteAccessRecord(ctx)
	if err != nil {
		return loginRequestContext{}, err
	}
	requestHostname := normalizedRequestHostname(request.Host)
	if !exists || requestHostname == "" || !strings.EqualFold(requestHostname, record.Hostname) {
		return result, nil
	}
	forwardedValid := false
	if forwarded := strings.TrimSpace(request.Header.Get("CF-Connecting-IP")); forwarded != "" {
		ip := net.ParseIP(forwarded)
		if ip == nil {
			return loginRequestContext{}, errors.New("center: Cloudflare client address is invalid")
		}
		result.ClientAddress = ip.String()
		forwardedValid = true
	}
	if record.ProtectionMode != "native" {
		return result, nil
	}
	if !forwardedValid {
		return loginRequestContext{}, errors.New("center: direct Tunnel login requires Cloudflare client identity")
	}
	if record.Status != "configured" {
		return loginRequestContext{}, errors.New("center: direct Tunnel login protection is not ready")
	}
	if record.TurnstileSiteKey == "" || record.TurnstileSecretID == "" {
		return loginRequestContext{}, errors.New("center: direct Tunnel login protection is incomplete")
	}
	result.Hostname = record.Hostname
	result.Protection.CaptchaRequired = true
	result.Protection.TurnstileSiteKey = record.TurnstileSiteKey
	return result, nil
}

func requestRemoteIP(request *http.Request) string {
	if request == nil {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(request.RemoteAddr))
	if err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
	}
	if ip := net.ParseIP(strings.TrimSpace(request.RemoteAddr)); ip != nil {
		return ip.String()
	}
	return "unknown"
}

func normalizedRequestHostname(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(value, "[]"), "."))
}

func (s *Store) verifyCenterLoginTurnstile(ctx context.Context, token, clientAddress, hostname string) error {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 2048 {
		return errors.New("center: complete the security check")
	}
	var secretID string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(turnstile_secret_id, '') FROM center_remote_access
		WHERE id = 1 AND status = 'configured' AND protection_mode = 'native' AND hostname = ?`, hostname).Scan(&secretID)
	if err != nil || secretID == "" {
		return errors.New("center: login security check is unavailable")
	}
	secret, err := s.getSecret(ctx, secretID, "center-remote-access-turnstile")
	if err != nil {
		return errors.New("center: login security check is unavailable")
	}
	values := url.Values{
		"secret":   {string(secret)},
		"response": {token},
		"remoteip": {clientAddress},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.turnstileVerifyURL, strings.NewReader(values.Encode()))
	if err != nil {
		return errors.New("center: login security check is unavailable")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.turnstileHTTPClient.Do(request)
	if err != nil {
		return errors.New("center: login security check is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 16<<10))
		return errors.New("center: login security check is unavailable")
	}
	var result struct {
		Success  bool     `json:"success"`
		Hostname string   `json:"hostname"`
		Action   string   `json:"action"`
		Errors   []string `json:"error-codes"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<10))
	if err := decoder.Decode(&result); err != nil {
		return errors.New("center: login security check is unavailable")
	}
	if !result.Success || !strings.EqualFold(strings.TrimSuffix(result.Hostname, "."), hostname) || result.Action != centerLoginTurnstileAction {
		return errors.New("center: security check failed; complete a new challenge")
	}
	return nil
}

func writeLoginError(writer http.ResponseWriter, status int, code, message string, retryAfter time.Duration, captchaRequired bool) {
	retrySeconds := durationSecondsCeil(retryAfter)
	if retrySeconds > 0 {
		writer.Header().Set("Retry-After", fmt.Sprintf("%d", retrySeconds))
	}
	writeJSON(writer, status, map[string]any{
		"code":              code,
		"error":             message,
		"retryAfterSeconds": retrySeconds,
		"captchaRequired":   captchaRequired,
	})
}

func durationSecondsCeil(value time.Duration) int {
	if value <= 0 {
		return 0
	}
	return int((value + time.Second - 1) / time.Second)
}
