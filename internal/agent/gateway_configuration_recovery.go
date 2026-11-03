package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"

	"github.com/petauron/vastora/internal/gateway"
)

// Reconcile actual configuration against committed state once at startup.
// An exact match is observation only. No revision or task is replayed.
func (s *Store) restoreGatewayConfiguration(ctx context.Context) error {
	if s.sharedCaddy == nil {
		return nil
	}
	state, err := s.GatewayState(ctx)
	if errors.Is(err, errNoAppliedGatewayState) {
		return nil
	}
	if err != nil {
		return err
	}
	hash, err := gateway.ConfigurationHash(state.Desired, state.Certificates)
	if err != nil || hash != state.ConfigHash {
		return errors.New("agent: committed gateway configuration identity changed")
	}
	sites, err := s.sharedCaddyJournals(ctx)
	if err != nil {
		return err
	}
	for _, site := range sites {
		if site.State != "applied" {
			return errors.New("agent: interrupted website operation requires recovery before gateway startup")
		}
	}
	driver := s.sharedCaddy
	driver.mutationMu.Lock()
	defer driver.mutationMu.Unlock()
	expected, err := driver.configuration(ctx, state.Desired, state.Certificates)
	if err != nil {
		return err
	}
	actual, err := driver.runtimeConfiguration(ctx)
	if err != nil {
		return err
	}
	wanted, err := decodeGatewayRuntimeConfiguration(expected)
	if err != nil {
		return errors.New("agent: committed gateway configuration could not be rendered")
	}
	if reflect.DeepEqual(actual, wanted) {
		return nil
	}
	if err := driver.load(ctx, expected); err != nil {
		return fmt.Errorf("agent: restore committed gateway configuration: %w", err)
	}
	driver.mu.Lock()
	driver.state = state.Desired.Sorted()
	driver.certificates = append([]gateway.Certificate(nil), state.Certificates...)
	driver.mu.Unlock()
	return nil
}

func (driver *CaddyGatewayDriver) runtimeConfigurationLoaded(ctx context.Context) (bool, error) {
	config, err := driver.runtimeConfiguration(ctx)
	if err != nil {
		return false, err
	}
	apps, _ := config["apps"].(map[string]any)
	httpApp, _ := apps["http"].(map[string]any)
	servers, _ := httpApp["servers"].(map[string]any)
	return len(servers) != 0, nil
}

func (driver *CaddyGatewayDriver) runtimeConfiguration(ctx context.Context) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, driver.AdminURL+"/config/", nil)
	if err != nil {
		return nil, err
	}
	response, err := driver.client().Do(request)
	if err != nil {
		return nil, fmt.Errorf("agent: read gateway startup configuration: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("agent: gateway startup configuration is unavailable")
	}
	const limit = 8 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(raw) > limit {
		return nil, errors.New("agent: gateway startup configuration exceeds its limit")
	}
	return decodeGatewayRuntimeConfiguration(raw)
}

func decodeGatewayRuntimeConfiguration(raw []byte) (map[string]any, error) {
	if !json.Valid(raw) {
		return nil, errors.New("agent: invalid gateway startup configuration")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var config map[string]any
	if decoder.Decode(&config) != nil || config == nil {
		return nil, errors.New("agent: invalid gateway startup configuration")
	}
	return config, nil
}
