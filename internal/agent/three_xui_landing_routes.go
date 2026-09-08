package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

// This adapter is deliberately node-local: a controller's remote-node API
// would modify a different Xray configuration. The task runner must hold the
// application mutation lock and persist the encrypted checkpoint before Apply.
// Apply reconciles settings only. It is not a health check, does not renew a
// firewall lease, and does not claim that existing connections have ended.
type threeXUILandingRoutes struct {
	baseURL string
	token   string
}

func (store *Store) localThreeXUILandingRoutes(ctx context.Context, applicationID string) (threeXUILandingRoutes, error) {
	installation, err := store.AppliedInstallation(ctx, threeXUIKey)
	if err != nil || applicationID == "" || installation.ApplicationID != applicationID {
		return threeXUILandingRoutes{}, errors.New("agent: selected local 3x-ui installation is unavailable")
	}
	config, err := decodeThreeXUIConfig(installation.Config)
	if err != nil {
		return threeXUILandingRoutes{}, errors.New("agent: local 3x-ui settings are unavailable")
	}
	address := net.ParseIP(installation.ServiceAddress)
	parsed, parseErr := netip.ParseAddr(installation.ServiceAddress)
	if address == nil || address.To4() == nil || parseErr != nil || (!parsed.IsPrivate() && !netip.MustParsePrefix("100.64.0.0/10").Contains(parsed)) {
		return threeXUILandingRoutes{}, errors.New("agent: local 3x-ui service address is unavailable")
	}
	var secrets map[string]string
	if json.Unmarshal(installation.Secrets, &secrets) != nil || strings.TrimSpace(secrets["api_token"]) == "" {
		return threeXUILandingRoutes{}, errors.New("agent: local 3x-ui credentials are unavailable")
	}
	return threeXUILandingRoutes{
		baseURL: "http://" + net.JoinHostPort(address.String(), strconv.Itoa(config.PanelPort)),
		token:   secrets["api_token"],
	}, nil
}

func (routes threeXUILandingRoutes) Read(ctx context.Context) (json.RawMessage, string, error) {
	payload, err := routes.request(ctx, http.MethodPost, "/panel/api/xray/", nil)
	if err != nil {
		return nil, "", err
	}
	// Decode the nested string without passing configuration numbers through
	// float64, or an unrelated large integer could be rounded during cutover.
	var nested string
	var settings struct {
		XraySetting     json.RawMessage `json:"xraySetting"`
		OutboundTestURL string          `json:"outboundTestUrl"`
	}
	if json.Unmarshal(payload, &nested) != nil || json.Unmarshal([]byte(nested), &settings) != nil {
		return nil, "", errors.New("agent: local 3x-ui returned invalid routing settings")
	}
	if _, err := landing.RouteSettingsHash(settings.XraySetting); err != nil {
		return nil, "", errors.New("agent: local 3x-ui returned invalid routing settings")
	}
	return settings.XraySetting, settings.OutboundTestURL, nil
}

func (routes threeXUILandingRoutes) Apply(ctx context.Context, change landing.RouteChange, enable bool) error {
	current, outboundTestURL, err := routes.Read(ctx)
	if err != nil {
		return err
	}
	desired, write, err := change.NextWrite(current, enable)
	if err != nil || !write {
		return err
	}
	_, writeErr := routes.request(ctx, http.MethodPost, "/panel/api/xray/update", url.Values{
		"xraySetting": {string(desired)}, "outboundTestUrl": {outboundTestURL},
	})
	// A successful response is not readback; a failed response may still have
	// committed. In both cases resolve the actual state without overwriting a
	// conflicting third state or silently restoring direct during an outage.
	actual, _, readErr := routes.Read(ctx)
	if readErr == nil {
		_, stillNeedsWrite, compareErr := change.NextWrite(actual, enable)
		if compareErr == nil && !stillNeedsWrite {
			return nil
		}
		if compareErr != nil {
			return compareErr
		}
	}
	if ctx.Err() != nil {
		return errors.New("agent: local route change requires reconciliation")
	}
	if writeErr != nil || readErr != nil {
		return errors.New("agent: local route change could not be confirmed")
	}
	return errors.New("agent: local route settings did not match the requested change")
}

func (routes threeXUILandingRoutes) request(ctx context.Context, method, path string, form url.Values) (json.RawMessage, error) {
	body, contentType := "{}", "application/json"
	if method == http.MethodGet {
		body = ""
	}
	if form != nil {
		body, contentType = form.Encode(), "application/x-www-form-urlencoded"
	}
	request, err := http.NewRequestWithContext(ctx, method, routes.baseURL+path, strings.NewReader(body))
	if err != nil {
		return nil, errors.New("agent: invalid local 3x-ui request")
	}
	request.Header.Set("Authorization", "Bearer "+routes.token)
	request.Header.Set("Content-Type", contentType)
	// Neither an environment HTTP proxy nor a server redirect may receive the
	// node's API token or credentials in unrelated original routing settings.
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("agent: local 3x-ui request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	var envelope struct {
		Success bool            `json:"success"`
		Object  json.RawMessage `json:"obj"`
	}
	if err != nil || len(data) > 4<<20 || response.StatusCode < 200 || response.StatusCode >= 300 || json.Unmarshal(data, &envelope) != nil || !envelope.Success {
		// Remote errors may echo the submitted configuration. Never propagate
		// their text into task receipts, diagnostics or public status.
		return nil, errors.New("agent: local 3x-ui rejected the routing request")
	}
	return envelope.Object, nil
}
