//go:build !linux

package osreinstall

import (
	"context"
	"errors"
)

func VerifyRestoredOnboarding(context.Context, string, int) (OnboardingEvidence, error) {
	return OnboardingEvidence{}, errors.New("reinstall: onboarding verification requires Linux systemd")
}
