package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	threeXUIRawSubscriptionPath        = "/sub/"
	threeXUIClashSubscriptionPath      = "/clash/"
	threeXUISubscriptionRemarkTemplate = "{{INBOUND}}"
	threeXUIRestartSettleTime          = 4 * time.Second
)

func applySubscriptionCommand(ctx context.Context, store *Store, command SubscriptionCommandTask) (SubscriptionCommandResult, error) {
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(command.Domain), "."))
	baseURI := strings.TrimSpace(command.BaseURI)
	parsed, err := url.Parse(baseURI)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != domain || parsed.Port() != "" || parsed.Path != threeXUIRawSubscriptionPath || parsed.RawQuery != "" || parsed.Fragment != "" {
		return SubscriptionCommandResult{}, errors.New("agent: invalid public subscription address")
	}
	installation, err := store.AppliedInstallation(ctx, threeXUIKey)
	if err != nil {
		return SubscriptionCommandResult{}, errors.New("agent: managed subscription controller is unavailable")
	}
	readyContext, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		store.landingSubscriptionMu.RLock()
		ready := store.landingSubscriptionAddress == installation.ServiceAddress
		store.landingSubscriptionMu.RUnlock()
		if ready {
			break
		}
		select {
		case <-readyContext.Done():
			return SubscriptionCommandResult{}, errors.New("agent: native subscription endpoint is unavailable")
		case <-ticker.C:
		}
	}
	return SubscriptionCommandResult{Domain: domain, BaseURI: baseURI}, nil
}

func restartThreeXUIPanel(ctx context.Context, endpoint, token string, settleTime time.Duration) error {
	if _, err := threeXUIRequest(ctx, http.MethodPost, endpoint+"/panel/api/setting/restartPanel", token, map[string]any{}); err != nil {
		return fmt.Errorf("agent: restart 3x-ui after subscription update: %w", err)
	}
	restartContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	restartStarted := time.Now()
	sawUnavailable := false
	for {
		probeContext, probeCancel := context.WithTimeout(restartContext, time.Second)
		_, probeErr := threeXUIRequest(probeContext, http.MethodPost, endpoint+"/panel/api/setting/all", token, map[string]any{})
		probeCancel()
		if probeErr != nil {
			sawUnavailable = true
		} else if sawUnavailable || time.Since(restartStarted) >= settleTime {
			return nil
		}
		select {
		case <-restartContext.Done():
			return fmt.Errorf("agent: wait for 3x-ui subscription reload: %w", restartContext.Err())
		case <-ticker.C:
		}
	}
}
