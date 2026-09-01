package networking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// LookupPublicIPv4 asks the Vastora address reflector which IPv4 address made
// this request. The response identifies outbound NAT only; callers must not
// treat it as proof of inbound reachability.
func LookupPublicIPv4(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(endpoint), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("network: detect public address: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	if err != nil {
		return "", fmt.Errorf("network: read public address response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("network: public address service returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Address string `json:"address"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return "", errors.New("network: public address service returned invalid JSON")
	}
	address := net.ParseIP(strings.TrimSpace(result.Address))
	if address == nil || address.To4() == nil || Classify("external", address) != KindPublic {
		return "", errors.New("network: public address service returned an invalid IPv4 address")
	}
	return address.String(), nil
}

// DetectPublicEgress observes the public IPv4 and matches the kernel-selected
// local receiving address to a discovered Agent interface.
func DetectPublicEgress(ctx context.Context, client *http.Client, endpoint string, candidates []Candidate, now time.Time) (*PublicEgress, error) {
	publicAddress, err := LookupPublicIPv4(ctx, client, endpoint)
	if err != nil {
		return nil, err
	}
	bindAddress, err := DefaultRouteAddress(publicAddress)
	if err != nil {
		return nil, err
	}
	bindKind := ""
	for _, candidate := range candidates {
		if candidate.Address == bindAddress && (candidate.Kind == KindLAN || candidate.Kind == KindPublic) {
			bindKind = candidate.Kind
			break
		}
	}
	if bindKind == "" {
		return nil, errors.New("network: default route address was not reported by the Agent")
	}
	mode := PublicModeNAT
	if publicAddress == bindAddress && bindKind == KindPublic {
		mode = PublicModeDirect
	}
	return &PublicEgress{Address: publicAddress, BindAddress: bindAddress, Mode: mode, ObservedAt: now.UTC()}, nil
}
