package controlplane

import "errors"

// RecoveryApplication contains only bounded public diagnostics. Raw runtime
// errors, filesystem paths and credentials must remain in Agent logs.
type RecoveryApplication struct {
	AppKey        string `json:"appKey"`
	ApplicationID string `json:"applicationId,omitempty"`
	Reason        string `json:"reason"`
}

type RecoveryScope struct {
	Stage        string                `json:"stage"`
	Applications []RecoveryApplication `json:"applications,omitempty"`
}

func (scope RecoveryScope) Validate() error {
	switch scope.Stage {
	case "application", "landing", "gateway", "listener":
	default:
		return errors.New("invalid runtime recovery stage")
	}
	if len(scope.Applications) > 16 || scope.Stage != "application" && len(scope.Applications) != 0 {
		return errors.New("invalid runtime recovery applications")
	}
	seen := map[string]bool{}
	for _, application := range scope.Applications {
		switch application.AppKey {
		case "vastora-official/3x-ui", "vastora-official/cpa", "vastora-official/keeper", "vastora-official/komari-agent", "vastora-official/pulse", "vastora-official/pulse-agent":
		default:
			return errors.New("invalid recovery application")
		}
		if seen[application.AppKey] || len(application.ApplicationID) > 128 {
			return errors.New("invalid recovery application identity")
		}
		seen[application.AppKey] = true
		switch application.Reason {
		case "state_incomplete", "image_unavailable", "health_check_failed", "restore_failed":
		default:
			return errors.New("invalid recovery reason")
		}
	}
	return nil
}
