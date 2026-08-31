package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/networking"
)

const (
	setupPublicEntryVerificationSetting = "setup_public_entry_verification"
	setupPublicEntryVerificationMaxAge  = 30 * time.Minute
)

type SetupPublicEntryInput struct {
	PublicAddress  string `json:"publicAddress"`
	GatewayAddress string `json:"gatewayAddress"`
	NATConfirmed   bool   `json:"natConfirmed"`
}

type SetupPublicEntryResult struct {
	Status         string `json:"status"`
	PublicAddress  string `json:"publicAddress"`
	GatewayAddress string `json:"gatewayAddress"`
	Ports          []int  `json:"ports"`
}

type setupPublicEntryVerification struct {
	PublicAddress string    `json:"publicAddress"`
	BindAddress   string    `json:"bindAddress"`
	VerifiedAt    time.Time `json:"verifiedAt"`
}

type AgentPublicEntryResult struct {
	Status         string             `json:"status"`
	PublicAddress  string             `json:"publicAddress"`
	GatewayAddress string             `json:"gatewayAddress"`
	Mode           string             `json:"mode"`
	Profile        networking.Profile `json:"profile"`
}

func (s *Server) handleVerifySetupPublicEntry(writer http.ResponseWriter, request *http.Request) {
	var input SetupPublicEntryInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	result, err := s.verifySetupPublicEntry(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) verifySetupPublicEntry(ctx context.Context, input SetupPublicEntryInput) (SetupPublicEntryResult, error) {
	if s.infrastructure == nil {
		return SetupPublicEntryResult{}, errors.New("center: this installation cannot verify a public entry")
	}
	candidates, err := s.store.discoverNetworkCandidates(s.store.now().UTC())
	if err != nil {
		return SetupPublicEntryResult{}, fmt.Errorf("center: discover local addresses: %w", err)
	}
	binding, err := s.store.resolveSetupGatewayBinding(ctx, SetupDNSInput{
		PublicAddress:  input.PublicAddress,
		GatewayAddress: input.GatewayAddress,
		NATConfirmed:   input.NATConfirmed,
	}, candidates)
	if err != nil {
		return SetupPublicEntryResult{}, err
	}
	probe, err := s.infrastructure.StartPublicEntryProbe(ctx, deployapi.PublicEntryProbeRequest{BindAddress: binding.BindAddress})
	if err != nil {
		return SetupPublicEntryResult{}, err
	}
	verificationErr := s.store.verifyPublicEntry(ctx, binding.PublicAddress, probe)
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	stopErr := s.infrastructure.StopPublicEntryProbe(stopCtx, probe.ID)
	cancel()
	if verificationErr != nil {
		return SetupPublicEntryResult{}, verificationErr
	}
	if stopErr != nil {
		return SetupPublicEntryResult{}, fmt.Errorf("center: stop public entry probe: %w", stopErr)
	}
	if err := s.store.savePublicEntryVerification(ctx, binding); err != nil {
		return SetupPublicEntryResult{}, err
	}
	return SetupPublicEntryResult{Status: "ready", PublicAddress: binding.PublicAddress, GatewayAddress: binding.BindAddress, Ports: probe.Ports}, nil
}

func (s *Server) handleEnableDetectedAgentPublicEntry(writer http.ResponseWriter, request *http.Request) {
	result, err := s.enableDetectedAgentPublicEntry(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

// enableDetectedAgentPublicEntry promotes the already externally verified
// Center-host mapping into the co-located Agent network profile. Remote Agents
// are deliberately excluded: the Center deployer cannot prove their sockets.
func (s *Server) enableDetectedAgentPublicEntry(ctx context.Context, agentID string) (AgentPublicEntryResult, error) {
	if s.infrastructure == nil {
		return AgentPublicEntryResult{}, errors.New("center: this installation cannot detect a public entry")
	}
	var capabilitiesJSON []byte
	var gatewayHealthy int
	if err := s.store.db.QueryRowContext(ctx, `SELECT capabilities_json, gateway_healthy FROM agents WHERE id = ? AND status = 'active'`, strings.TrimSpace(agentID)).Scan(&capabilitiesJSON, &gatewayHealthy); errors.Is(err, sql.ErrNoRows) {
		return AgentPublicEntryResult{}, errors.New("center: active Agent not found")
	} else if err != nil {
		return AgentPublicEntryResult{}, err
	}
	var capabilities NodeCapabilities
	if json.Unmarshal(capabilitiesJSON, &capabilities) != nil || !capabilities.Gateway {
		return AgentPublicEntryResult{}, errors.New("center: automatic public entry requires a Gateway-capable Agent")
	}
	if gatewayHealthy != 1 {
		return AgentPublicEntryResult{}, errors.New("center: wait for the co-located Gateway to become healthy before enabling public ingress")
	}
	candidates, err := networkCandidates(ctx, s.store.db, agentID)
	if err != nil {
		return AgentPublicEntryResult{}, err
	}
	coLocated, err := s.store.networkCandidatesAreCoLocated(candidates)
	if err != nil {
		return AgentPublicEntryResult{}, err
	}
	if !coLocated {
		return AgentPublicEntryResult{}, errors.New("center: automatic public entry is available only for the Agent running on this Center host")
	}
	hostCandidates, err := s.store.discoverNetworkCandidates(s.store.now().UTC())
	if err != nil {
		return AgentPublicEntryResult{}, fmt.Errorf("center: discover Center host addresses: %w", err)
	}
	hostHeadscale := map[string]struct{}{}
	for _, candidate := range hostCandidates {
		if candidate.Kind == networking.KindHeadscale {
			hostHeadscale[candidate.Address] = struct{}{}
		}
	}
	sharedHeadscale := false
	for _, candidate := range candidates {
		if candidate.Kind == networking.KindHeadscale {
			_, sharedHeadscale = hostHeadscale[candidate.Address]
			if sharedHeadscale {
				break
			}
		}
	}
	if len(hostHeadscale) == 0 || !sharedHeadscale {
		return AgentPublicEntryResult{}, errors.New("center: automatic public entry requires the Agent to share this Center host's secure-private address")
	}
	binding, configured, err := s.store.setupGatewayBinding(ctx)
	if err != nil {
		return AgentPublicEntryResult{}, err
	}
	if !configured {
		return AgentPublicEntryResult{}, errors.New("center: no verified Center public mapping is configured")
	}
	verification, err := s.store.readPublicEntryVerification(ctx)
	if err != nil {
		return AgentPublicEntryResult{}, err
	}
	if verification.PublicAddress != binding.PublicAddress || verification.BindAddress != binding.BindAddress {
		return AgentPublicEntryResult{}, errors.New("center: the stored public mapping has not passed external verification")
	}
	observedPublicAddress, err := s.store.lookupPublicAddress(ctx)
	if err != nil {
		return AgentPublicEntryResult{}, err
	}
	if observedPublicAddress != binding.PublicAddress {
		return AgentPublicEntryResult{}, errors.New("center: the detected public address changed; verify the Center public mapping again")
	}
	bindKind := ""
	for _, candidate := range candidates {
		if candidate.Address == binding.BindAddress && (candidate.Kind == networking.KindLAN || candidate.Kind == networking.KindPublic) {
			bindKind = candidate.Kind
			break
		}
	}
	if bindKind == "" {
		return AgentPublicEntryResult{}, errors.New("center: the verified receiving address is no longer assigned to this Agent")
	}
	profile, err := networkProfile(ctx, s.store.db, agentID)
	if err != nil {
		return AgentPublicEntryResult{}, err
	}
	if profile == nil {
		profile = suggestedNetworkProfile(candidates)
	}
	profile.DirectPublic = true
	profile.PublicAddress = binding.PublicAddress
	profile.PublicBindAddress = binding.BindAddress
	profile.PublicMode = networking.PublicModeNAT
	if bindKind == networking.KindPublic && binding.PublicAddress == binding.BindAddress {
		profile.PublicMode = networking.PublicModeDirect
	}
	profile.EnabledKinds = uniqueStrings(append(profile.EnabledKinds, networking.KindPublic))
	verifiedAt := verification.VerifiedAt
	if profile.PublicMode == networking.PublicModeDirect {
		verifiedAt = time.Time{}
	}
	confirmed, err := s.store.confirmNetworkProfile(ctx, agentID, *profile, verifiedAt)
	if err != nil {
		return AgentPublicEntryResult{}, err
	}
	return AgentPublicEntryResult{Status: "ready", PublicAddress: binding.PublicAddress, GatewayAddress: binding.BindAddress, Mode: confirmed.PublicMode, Profile: *confirmed}, nil
}

func suggestedNetworkProfile(candidates []networking.Candidate) *networking.Profile {
	profile := &networking.Profile{ServiceAddress: "127.0.0.1", EnabledKinds: []string{}}
	for _, candidate := range candidates {
		switch candidate.Kind {
		case networking.KindLAN:
			if profile.LANAddress == "" {
				profile.LANAddress = candidate.Address
				profile.EnabledKinds = append(profile.EnabledKinds, networking.KindLAN)
				profile.ServiceAddress = candidate.Address
			}
		case networking.KindHeadscale:
			if profile.HeadscaleAddress == "" {
				profile.HeadscaleAddress = candidate.Address
				profile.EnabledKinds = append(profile.EnabledKinds, networking.KindHeadscale)
				profile.ServiceAddress = candidate.Address
			}
		}
	}
	return profile
}

func (s *Store) savePublicEntryVerification(ctx context.Context, binding setupGatewayBinding) error {
	encoded, err := json.Marshal(setupPublicEntryVerification{
		PublicAddress: binding.PublicAddress,
		BindAddress:   binding.BindAddress,
		VerifiedAt:    s.now().UTC(),
	})
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, setupPublicEntryVerificationSetting, string(encoded)); err != nil {
		return fmt.Errorf("center: save public entry verification: %w", err)
	}
	return nil
}

func (s *Store) requireFreshPublicEntryVerification(ctx context.Context, binding setupGatewayBinding) error {
	verification, err := s.readPublicEntryVerification(ctx)
	if err != nil {
		return err
	}
	age := s.now().UTC().Sub(verification.VerifiedAt)
	if verification.PublicAddress != binding.PublicAddress || verification.BindAddress != binding.BindAddress || age < -time.Minute || age > setupPublicEntryVerificationMaxAge {
		return errors.New("center: public entry verification is missing or expired; check ports 80 and 443 again")
	}
	return nil
}

func (s *Store) readPublicEntryVerification(ctx context.Context) (setupPublicEntryVerification, error) {
	var encoded string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, setupPublicEntryVerificationSetting).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return setupPublicEntryVerification{}, errors.New("center: verify the public ports 80 and 443 before configuring DNS")
		}
		return setupPublicEntryVerification{}, fmt.Errorf("center: read public entry verification: %w", err)
	}
	var verification setupPublicEntryVerification
	if err := json.Unmarshal([]byte(encoded), &verification); err != nil {
		return setupPublicEntryVerification{}, errors.New("center: stored public entry verification is invalid")
	}
	if verification.VerifiedAt.IsZero() {
		return setupPublicEntryVerification{}, errors.New("center: stored public entry verification is invalid")
	}
	return verification, nil
}
