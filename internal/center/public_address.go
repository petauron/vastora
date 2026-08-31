package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/networking"
)

const publicEntryVerificationURL = "https://vastora.petauron.com/network/verify-public-entry"

func vastoraPublicAddressLookup(client *http.Client) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		address, err := networking.LookupPublicIPv4(ctx, client, networking.PublicAddressLookupURL)
		if err != nil {
			return "", fmt.Errorf("center: detect public address: %w", err)
		}
		return address, nil
	}
}

func vastoraPublicEntryVerifier(client *http.Client) func(context.Context, string, deployapi.PublicEntryProbe) error {
	return func(ctx context.Context, publicAddress string, probe deployapi.PublicEntryProbe) error {
		publicIP := net.ParseIP(strings.TrimSpace(publicAddress))
		if publicIP == nil || publicIP.To4() == nil || publicIP.IsPrivate() || !publicIP.IsGlobalUnicast() {
			return errors.New("center: public entry verification requires a valid public address")
		}
		payload, err := json.Marshal(map[string]any{"ports": probe.Ports, "challenge": probe.Challenge})
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, publicEntryVerificationURL, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return fmt.Errorf("center: request public entry verification: %w", err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		if err != nil {
			return fmt.Errorf("center: read public entry verification: %w", err)
		}
		var result struct {
			Status  string `json:"status"`
			Address string `json:"address"`
			Ports   []struct {
				Port  int  `json:"port"`
				Ready bool `json:"ready"`
			} `json:"ports"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return errors.New("center: public entry verification returned invalid JSON")
		}
		if response.StatusCode != http.StatusOK {
			if strings.TrimSpace(result.Error) != "" {
				return fmt.Errorf("center: public ports 80 and 443 are not reachable: %s", result.Error)
			}
			return fmt.Errorf("center: public entry verification returned HTTP %d", response.StatusCode)
		}
		observedIP := net.ParseIP(strings.TrimSpace(result.Address))
		if result.Status != "ready" || observedIP == nil || observedIP.To4() == nil || !observedIP.Equal(publicIP) || len(result.Ports) != len(probe.Ports) {
			return errors.New("center: public entry verification returned an unexpected result")
		}
		for index, port := range result.Ports {
			if port.Port != probe.Ports[index] || !port.Ready {
				return errors.New("center: public entry verification did not confirm every required port")
			}
		}
		return nil
	}
}
