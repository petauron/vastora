package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/tailscalehost"
)

const tailscaleFixedEndpointSetting = "tailscale_fixed_endpoint"

type TailscaleFixedEndpointInput struct {
	Enabled        bool   `json:"enabled"`
	Endpoint       string `json:"endpoint"`
	LocalAddress   string `json:"localAddress"`
	ConfirmMapping bool   `json:"confirmMapping"`
}

type TailscaleFixedEndpointView struct {
	Available              bool                   `json:"available"`
	Enabled                bool                   `json:"enabled"`
	Endpoint               string                 `json:"endpoint"`
	LocalAddress           string                 `json:"localAddress"`
	DetectedEndpoint       string                 `json:"detectedEndpoint"`
	DetectedLocalAddress   string                 `json:"detectedLocalAddress"`
	LocalAddressCandidates []networking.Candidate `json:"localAddressCandidates"`
	Status                 string                 `json:"status"`
	LastError              string                 `json:"lastError,omitempty"`
}

type tailscaleFixedEndpointConfig struct {
	Enabled      bool      `json:"enabled"`
	Endpoint     string    `json:"endpoint"`
	LocalAddress string    `json:"localAddress"`
	ConfirmedAt  time.Time `json:"confirmedAt"`
}

func (s *Store) TailscaleFixedEndpoint(ctx context.Context) (TailscaleFixedEndpointView, error) {
	view := TailscaleFixedEndpointView{Status: "unavailable", LocalAddressCandidates: []networking.Candidate{}}
	var mode string
	if err := s.db.QueryRowContext(ctx, `SELECT mode FROM network_integrations WHERE kind = 'headscale' AND status = 'configured'`).Scan(&mode); errors.Is(err, sql.ErrNoRows) {
		return view, nil
	} else if err != nil {
		return view, fmt.Errorf("center: read Headscale mode for fixed endpoint: %w", err)
	}
	if mode != "builtin" {
		return view, nil
	}
	view.Available = true
	candidates, err := s.discoverNetworkCandidates(s.now().UTC())
	if err != nil {
		return view, fmt.Errorf("center: discover fixed-endpoint local addresses: %w", err)
	}
	for _, candidate := range candidates {
		if candidate.Kind == networking.KindLAN || candidate.Kind == networking.KindPublic {
			view.LocalAddressCandidates = append(view.LocalAddressCandidates, candidate)
		}
	}
	if binding, configured, bindingErr := s.setupGatewayBinding(ctx); bindingErr != nil {
		return view, fmt.Errorf("center: read detected fixed endpoint: %w", bindingErr)
	} else if configured {
		if endpoint, endpointErr := tailscalehost.StaticEndpoint(binding.PublicAddress); endpointErr == nil {
			view.DetectedEndpoint = endpoint
			view.DetectedLocalAddress = binding.BindAddress
		}
	}
	config, err := s.readTailscaleFixedEndpoint(ctx)
	if err != nil {
		return view, err
	}
	view.Enabled = config.Enabled
	view.Endpoint = config.Endpoint
	view.LocalAddress = config.LocalAddress
	ownerAddress := config.LocalAddress
	if ownerAddress == "" {
		ownerAddress = view.DetectedLocalAddress
	}
	if ownerAddress != "" {
		owner, ownerErr := s.coLocatedTailscaleEndpointOwner(ctx, ownerAddress)
		if ownerErr != nil {
			return view, ownerErr
		}
		if owner != nil && owner.Ownership != "managed" {
			view.Available = false
			view.Status = "unavailable"
			return view, nil
		}
	}
	if !config.Enabled {
		view.Status = "disabled"
		return view, nil
	}
	if !candidateAddressExists(view.LocalAddressCandidates, config.LocalAddress) {
		view.Status = "action_required"
		view.LastError = "The confirmed local address is no longer present; the stale endpoint is not being advertised."
		return view, nil
	}
	owner, err := s.coLocatedTailscaleEndpointOwner(ctx, config.LocalAddress)
	if err != nil {
		return view, err
	}
	if owner == nil {
		view.Status = "action_required"
		view.LastError = "A single active Vastora-managed Agent is not reporting the confirmed local address; the fixed endpoint is not being advertised."
		return view, nil
	}
	view.Status = "configured"
	return view, nil
}

func (s *Store) ConfigureTailscaleFixedEndpoint(ctx context.Context, input TailscaleFixedEndpointInput) (TailscaleFixedEndpointView, error) {
	var builtin int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM network_integrations WHERE kind = 'headscale' AND mode = 'builtin' AND status = 'configured'`).Scan(&builtin); err != nil {
		return TailscaleFixedEndpointView{}, err
	}
	if builtin != 1 {
		return TailscaleFixedEndpointView{}, errors.New("center: a managed fixed endpoint is available only with built-in Headscale")
	}
	config := tailscaleFixedEndpointConfig{Enabled: input.Enabled}
	if input.Enabled {
		if !input.ConfirmMapping {
			return TailscaleFixedEndpointView{}, errors.New("center: confirm the reserved public IPv4 and UDP 41641 mapping")
		}
		endpoint, err := normalizeTailscaleFixedEndpoint(input.Endpoint)
		if err != nil {
			return TailscaleFixedEndpointView{}, err
		}
		localAddress := net.ParseIP(strings.TrimSpace(input.LocalAddress))
		if localAddress == nil || localAddress.To4() == nil {
			return TailscaleFixedEndpointView{}, errors.New("center: select the local IPv4 address that receives UDP 41641")
		}
		candidates, err := s.discoverNetworkCandidates(s.now().UTC())
		if err != nil {
			return TailscaleFixedEndpointView{}, fmt.Errorf("center: discover fixed-endpoint local address: %w", err)
		}
		if !candidateAddressExists(candidates, localAddress.String()) {
			return TailscaleFixedEndpointView{}, errors.New("center: the confirmed local address is stale or is not assigned to this Center host")
		}
		config.Endpoint = endpoint
		config.LocalAddress = localAddress.String()
		config.ConfirmedAt = s.now().UTC()
	}
	payload, err := json.Marshal(config)
	if err != nil {
		return TailscaleFixedEndpointView{}, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, tailscaleFixedEndpointSetting, string(payload)); err != nil {
		return TailscaleFixedEndpointView{}, fmt.Errorf("center: save Tailscale fixed endpoint: %w", err)
	}
	return s.TailscaleFixedEndpoint(ctx)
}

func (s *Store) readTailscaleFixedEndpoint(ctx context.Context) (tailscaleFixedEndpointConfig, error) {
	var payload string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, tailscaleFixedEndpointSetting).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
		return tailscaleFixedEndpointConfig{}, nil
	} else if err != nil {
		return tailscaleFixedEndpointConfig{}, fmt.Errorf("center: read Tailscale fixed endpoint: %w", err)
	}
	var config tailscaleFixedEndpointConfig
	if err := json.Unmarshal([]byte(payload), &config); err != nil {
		return tailscaleFixedEndpointConfig{}, errors.New("center: stored Tailscale fixed endpoint is invalid")
	}
	if !config.Enabled {
		return tailscaleFixedEndpointConfig{}, nil
	}
	endpoint, err := normalizeTailscaleFixedEndpoint(config.Endpoint)
	if err != nil {
		return tailscaleFixedEndpointConfig{}, errors.New("center: stored Tailscale fixed endpoint is invalid")
	}
	localAddress := net.ParseIP(config.LocalAddress)
	if localAddress == nil || localAddress.To4() == nil || config.ConfirmedAt.IsZero() {
		return tailscaleFixedEndpointConfig{}, errors.New("center: stored Tailscale fixed endpoint confirmation is invalid")
	}
	config.Endpoint = endpoint
	config.LocalAddress = localAddress.String()
	return config, nil
}

func normalizeTailscaleFixedEndpoint(value string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil || port != strconv.Itoa(tailscalehost.FixedPort) {
		return "", errors.New("center: the fixed Tailscale endpoint must use UDP port 41641")
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil || networking.Classify("fixed-endpoint", ip) != networking.KindPublic {
		return "", errors.New("center: the fixed Tailscale endpoint requires a public IPv4 address")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(tailscalehost.FixedPort)), nil
}

func candidateAddressExists(candidates []networking.Candidate, address string) bool {
	for _, candidate := range candidates {
		if candidate.Address == address && (candidate.Kind == networking.KindLAN || candidate.Kind == networking.KindPublic) {
			return true
		}
	}
	return false
}
