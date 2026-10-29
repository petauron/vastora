package agent

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/controlplane"
)

func (c Client) Enroll(ctx context.Context, store *Store, centerURL, enrollmentToken, caFingerprint, caCertificatePEM string) (Enrollment, error) {
	return c.enroll(ctx, store, centerURL, enrollmentToken, caFingerprint, caCertificatePEM, false)
}

// MigrateEnrollment explicitly replaces an existing Center identity while
// preserving all workload state held by the Agent store.
func (c Client) MigrateEnrollment(ctx context.Context, store *Store, centerURL, enrollmentToken, caFingerprint, caCertificatePEM string) (Enrollment, error) {
	return c.enroll(ctx, store, centerURL, enrollmentToken, caFingerprint, caCertificatePEM, true)
}

func (c Client) enroll(ctx context.Context, store *Store, centerURL, enrollmentToken, caFingerprint, caCertificatePEM string, replace bool) (Enrollment, error) {
	baseURL, err := normalizeCenterURL(centerURL)
	if err != nil {
		return Enrollment{}, err
	}
	if strings.TrimSpace(enrollmentToken) == "" {
		return Enrollment{}, errors.New("agent: enrollment token is required")
	}
	caFingerprint, caCertificatePEM, err = normalizeCenterTrust(baseURL, caFingerprint, caCertificatePEM)
	if err != nil {
		return Enrollment{}, err
	}
	if caFingerprint == "" && !loopbackCenterURL(baseURL) {
		caFingerprint, err = c.probeCenterCAFingerprint(ctx, baseURL)
		if err != nil {
			return Enrollment{}, err
		}
	}
	if err := validateCAFingerprint(baseURL, caFingerprint); err != nil {
		return Enrollment{}, err
	}
	operation, err := store.BeginEnrollmentOperation(ctx, baseURL, enrollmentToken, caFingerprint, caCertificatePEM, replace)
	if err != nil {
		return Enrollment{}, err
	}
	if operation.Phase != "enrollment_pending" {
		return store.EnrollmentForInstallOperation(ctx)
	}
	publicKey, err := controlplane.PublicKey(operation.PrivateKey)
	if err != nil {
		return Enrollment{}, errors.New("agent: stored enrollment identity is invalid")
	}
	connection := Connection{CenterURL: baseURL, CAFingerprint: caFingerprint, CACertificatePEM: caCertificatePEM}
	conn, release, err := c.grpcConnection(ctx, connection, false)
	if err != nil {
		return Enrollment{}, err
	}
	defer release()
	requestContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := agentGRPCClient(conn).Enroll(requestContext, &agentpb.EnrollRequest{
		Token: operation.Token, OperationId: operation.OperationID, Version: Version,
		OperatingSystem: runtime.GOOS, Architecture: runtime.GOARCH, PublicKey: publicKey,
	})
	if err != nil {
		return Enrollment{}, agentGRPCError(err)
	}
	if result == nil {
		return Enrollment{}, errors.New("agent: Center returned an incomplete enrollment response")
	}
	response := Enrollment{ID: result.AgentId, Credential: result.Credential, Name: result.Name, Roles: result.Roles}
	if err := json.Unmarshal(result.CapabilitiesJson, &response.Capabilities); err != nil {
		return Enrollment{}, errors.New("agent: Center returned invalid enrollment capabilities")
	}
	if response.ID == "" || response.Credential == "" || strings.TrimSpace(response.Name) == "" || len(response.Roles) == 0 {
		return Enrollment{}, errors.New("agent: Center returned an incomplete enrollment response")
	}
	if err := store.CompleteEnrollmentOperation(ctx, operation, response); err != nil {
		return Enrollment{}, err
	}
	return response, nil
}
