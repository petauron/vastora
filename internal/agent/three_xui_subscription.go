package agent

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const (
	threeXUIRawSubscriptionPath        = "/sub/"
	threeXUIClashSubscriptionPath      = "/clash/"
	threeXUISubscriptionRemarkTemplate = "{{INBOUND}}"
	threeXUIRestartSettleTime          = 4 * time.Second
)

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
