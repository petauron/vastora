package agent

import "github.com/petauron/vastora/internal/controlplane"

func (s *Store) setApplicationRecovery(application controlplane.RecoveryApplication) {
	s.runtimeRecoveryMu.Lock()
	defer s.runtimeRecoveryMu.Unlock()
	if s.runtimeRecoveryApps == nil {
		s.runtimeRecoveryApps = map[string]controlplane.RecoveryApplication{}
	}
	s.runtimeRecoveryApps[application.AppKey] = application
}

func (s *Store) clearApplicationRecovery(appKey string) {
	s.runtimeRecoveryMu.Lock()
	defer s.runtimeRecoveryMu.Unlock()
	delete(s.runtimeRecoveryApps, appKey)
}

func (s *Store) runtimeRecovery() (string, []controlplane.RecoveryApplication) {
	s.runtimeRecoveryMu.RLock()
	defer s.runtimeRecoveryMu.RUnlock()
	if len(s.runtimeRecoveryApps) == 0 {
		return "", nil
	}
	applications := make([]controlplane.RecoveryApplication, 0, len(s.runtimeRecoveryApps))
	for _, application := range s.runtimeRecoveryApps {
		applications = append(applications, application)
	}
	return "application", applications
}
