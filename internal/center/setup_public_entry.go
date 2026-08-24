package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
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
	var encoded string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, setupPublicEntryVerificationSetting).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("center: verify the public ports 80 and 443 before configuring DNS")
		}
		return fmt.Errorf("center: read public entry verification: %w", err)
	}
	var verification setupPublicEntryVerification
	if err := json.Unmarshal([]byte(encoded), &verification); err != nil {
		return errors.New("center: stored public entry verification is invalid")
	}
	age := s.now().UTC().Sub(verification.VerifiedAt)
	if verification.PublicAddress != binding.PublicAddress || verification.BindAddress != binding.BindAddress || age < -time.Minute || age > setupPublicEntryVerificationMaxAge {
		return errors.New("center: public entry verification is missing or expired; check ports 80 and 443 again")
	}
	return nil
}
