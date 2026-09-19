package agent

import (
	"context"

	"errors"

	"runtime"

	"strings"

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
	var response Enrollment
	if err := c.post(ctx, baseURL+"/api/v1/agents/enroll", map[string]any{
		"token": operation.Token, "operationId": operation.OperationID, "version": Version, "operatingSystem": runtime.GOOS, "architecture": runtime.GOARCH, "publicKey": publicKey,
	}, "", caFingerprint, caCertificatePEM, &response); err != nil {
		return Enrollment{}, err
	}
	if response.ID == "" || response.Credential == "" || strings.TrimSpace(response.Name) == "" || len(response.Roles) == 0 {
		return Enrollment{}, errors.New("agent: Center returned an incomplete enrollment response")
	}
	if err := store.CompleteEnrollmentOperation(ctx, operation, response); err != nil {
		return Enrollment{}, err
	}
	return response, nil
}
