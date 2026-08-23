package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	threeXUICentralResetSyncTimeout  = 12 * time.Second
	threeXUICentralResetPollInterval = 250 * time.Millisecond
)

func applyThreeXUIInboundPlan(ctx context.Context, store *Store, baseURL, token string, command ThreeXUIClientCommandTask, resetUsage bool) error {
	if command.InboundID < 1 || command.InboundTotalBytes < 0 {
		return errors.New("agent: invalid 3x-ui inbound traffic plan")
	}
	if _, ok := clientInbound(command.Inbounds, []int{command.InboundID}, command.InboundID); !ok {
		return errors.New("agent: selected 3x-ui inbound is unavailable")
	}
	if resetUsage {
		return resetThreeXUIInboundPlan(ctx, store, baseURL, token, command)
	}
	settledJournal, err := settleThreeXUIResetBeforeInboundUpdate(ctx, store, baseURL, token, command)
	if err != nil {
		return err
	}
	centralInbound, centralUpdate, err := getThreeXUIRealityInbound(ctx, baseURL, token, command.InboundID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(command.InboundTag) == "" || normalizedThreeXUIInboundTag(centralInbound.Tag, command.TargetNodeID) != normalizedThreeXUIInboundTag(command.InboundTag, command.TargetNodeID) || !threeXUIInboundMatchesNode(centralInbound, command.TargetNodeID) {
		return errors.New("agent: managed REALITY inbound identity changed before updating its traffic plan")
	}
	desiredEnabled := centralInbound.Enable
	workerURL, workerToken, workerID, workerTag := "", "", 0, ""
	var workerInbound threeXUIRealityInbound
	if command.TargetNodeID > 0 {
		workerURL, workerToken, workerID, workerTag, err = resolveThreeXUIInboundTarget(ctx, baseURL, token, command)
		if err != nil {
			return err
		}
		workerInbound, _, err = getThreeXUIRealityInbound(ctx, workerURL, workerToken, workerID)
		if err != nil {
			return err
		}
		if normalizedThreeXUIInboundTag(workerInbound.Tag, command.TargetNodeID) != normalizedThreeXUIInboundTag(workerTag, command.TargetNodeID) {
			return errors.New("agent: managed REALITY worker inbound identity changed before updating its traffic plan")
		}
		desiredEnabled = workerInbound.Enable
	}
	if err := updateThreeXUIInboundPlanState(ctx, baseURL, token, command.InboundID, centralUpdate, command.InboundTotalBytes, desiredEnabled); err != nil {
		return err
	}
	if command.TargetNodeID > 0 {
		workerInbound, workerUpdate, workerErr := getThreeXUIRealityInbound(ctx, workerURL, workerToken, workerID)
		if workerErr != nil {
			return workerErr
		}
		if normalizedThreeXUIInboundTag(workerInbound.Tag, command.TargetNodeID) != normalizedThreeXUIInboundTag(workerTag, command.TargetNodeID) {
			return errors.New("agent: managed REALITY worker inbound identity changed while applying its traffic plan")
		}
		if !threeXUIInboundPlanStateMatches(workerInbound, command.InboundTotalBytes, desiredEnabled) {
			if err := updateThreeXUIInboundPlanState(ctx, workerURL, workerToken, workerID, workerUpdate, command.InboundTotalBytes, desiredEnabled); err != nil {
				return err
			}
		}
		if err := verifyThreeXUIInboundPlanState(ctx, workerURL, workerToken, workerID, workerTag, command.TargetNodeID, false, command.InboundTotalBytes, desiredEnabled); err != nil {
			return err
		}
	}
	if err := verifyThreeXUIInboundPlanState(ctx, baseURL, token, command.InboundID, command.InboundTag, command.TargetNodeID, true, command.InboundTotalBytes, desiredEnabled); err != nil {
		return err
	}
	if settledJournal != nil {
		status := "cancelled"
		if threeXUIResetJournalApplied(settledJournal.Status) {
			status = "cancelled_applied"
		}
		if err := store.markThreeXUIResetRecovery(ctx, settledJournal.OperationKey, status, "superseded by a verified explicit traffic plan update"); err != nil {
			return err
		}
	}
	return nil
}

func updateThreeXUIInboundPlanState(ctx context.Context, baseURL, token string, inboundID int, update map[string]any, totalBytes int64, enabled bool) error {
	update["total"] = totalBytes
	update["trafficReset"] = "never"
	update["trafficResetDay"] = 1
	update["enable"] = enabled
	delete(update, "id")
	delete(update, "clientStats")
	delete(update, "fallbackParent")
	if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/update/"+strconv.Itoa(inboundID), token, "application/json", update); err != nil {
		return fmt.Errorf("agent: update 3x-ui inbound traffic plan: %w", err)
	}
	return nil
}

func threeXUIInboundPlanStateMatches(inbound threeXUIRealityInbound, totalBytes int64, enabled bool) bool {
	return inbound.Total == totalBytes && inbound.TrafficReset == "never" && inbound.TrafficResetDay == 1 && inbound.Enable == enabled
}

func validateThreeXUIInboundPlanConfiguration(inbound threeXUIRealityInbound, totalBytes int64) error {
	if inbound.Total != totalBytes || inbound.TrafficReset != "never" || inbound.TrafficResetDay != 1 {
		return errors.New("agent: managed REALITY inbound traffic plan changed outside Vastora; save the node plan before resetting traffic")
	}
	return nil
}

func verifyThreeXUIInboundPlanState(ctx context.Context, baseURL, token string, inboundID int, expectedTag string, nodeID int, requireNodeMatch bool, totalBytes int64, enabled bool) error {
	inbound, _, err := getThreeXUIRealityInbound(ctx, baseURL, token, inboundID)
	if err != nil {
		return err
	}
	if normalizedThreeXUIInboundTag(inbound.Tag, nodeID) != normalizedThreeXUIInboundTag(expectedTag, nodeID) || (requireNodeMatch && !threeXUIInboundMatchesNode(inbound, nodeID)) {
		return errors.New("agent: managed REALITY inbound identity changed while verifying its traffic plan")
	}
	if !threeXUIInboundPlanStateMatches(inbound, totalBytes, enabled) {
		return errors.New("agent: 3x-ui inbound traffic plan did not reach the requested state")
	}
	return nil
}

func resetThreeXUIInboundPlan(ctx context.Context, store *Store, centralURL, centralToken string, command ThreeXUIClientCommandTask) error {
	err := executeThreeXUIInboundPlanReset(ctx, store, centralURL, centralToken, command)
	if err == nil {
		return nil
	}
	journal, found, journalErr := store.unfinishedThreeXUIResetJournal(ctx, command.ServiceID)
	if journalErr != nil {
		return errors.Join(err, journalErr)
	}
	if !found || journal.OperationKey != command.OperationKey {
		return err
	}
	restoreErr := restoreThreeXUIResetDesiredEnabled(ctx, centralURL, centralToken, command, journal)
	applied := threeXUIResetJournalApplied(journal.Status)
	if restoreErr != nil {
		message := fmt.Sprintf("reset failed: %v; restore failed: %v", err, restoreErr)
		status := "restore_pending"
		if applied {
			status = "restore_pending_applied"
		}
		if markErr := store.markThreeXUIResetRecovery(ctx, journal.OperationKey, status, message); markErr != nil {
			return errors.Join(err, restoreErr, markErr)
		}
		return fmt.Errorf("agent: REALITY inbound reset restore pending: %v", restoreErr)
	}
	status := "retry"
	if applied {
		status = "retry_applied"
	}
	if markErr := store.markThreeXUIResetRecovery(ctx, journal.OperationKey, status, err.Error()); markErr != nil {
		return errors.Join(err, markErr)
	}
	return err
}

func executeThreeXUIInboundPlanReset(ctx context.Context, store *Store, centralURL, centralToken string, command ThreeXUIClientCommandTask) error {
	if command.ServiceID == "" || command.ExpectedNextResetAt == "" || command.PlanRevision < 1 || command.OperationKey != threeXUIResetOperationKey(command.ServiceID, command.ExpectedNextResetAt) || command.InboundTag == "" {
		return errors.New("agent: invalid scheduled REALITY inbound reset")
	}
	_, completed, err := store.completedThreeXUIResetJournal(ctx, command.OperationKey, command.ServiceID, command.ExpectedNextResetAt, command.PlanRevision, command.InboundID, command.InboundTag, command.TargetNodeID)
	if err != nil {
		return err
	}
	if completed {
		return nil
	}
	targetURL, targetToken, targetID, targetTag, err := resolveThreeXUIInboundTarget(ctx, centralURL, centralToken, command)
	if err != nil {
		return err
	}
	inbound, _, err := getThreeXUIRealityInbound(ctx, targetURL, targetToken, targetID)
	if err != nil {
		return err
	}
	if normalizedThreeXUIInboundTag(inbound.Tag, command.TargetNodeID) != normalizedThreeXUIInboundTag(targetTag, command.TargetNodeID) {
		return errors.New("agent: target REALITY inbound tag changed before reset")
	}
	if err := validateThreeXUIInboundPlanConfiguration(inbound, command.InboundTotalBytes); err != nil {
		return err
	}
	if command.TargetNodeID > 0 {
		centralInbound, _, centralErr := getThreeXUIRealityInbound(ctx, centralURL, centralToken, command.InboundID)
		if centralErr != nil {
			return centralErr
		}
		if normalizedThreeXUIInboundTag(centralInbound.Tag, command.TargetNodeID) != normalizedThreeXUIInboundTag(command.InboundTag, command.TargetNodeID) || !threeXUIInboundMatchesNode(centralInbound, command.TargetNodeID) {
			return errors.New("agent: central REALITY inbound identity changed before resetting worker traffic")
		}
		if err := validateThreeXUIInboundPlanConfiguration(centralInbound, command.InboundTotalBytes); err != nil {
			return err
		}
	}
	usedBytes, err := threeXUIInboundUsedBytes(inbound)
	if err != nil {
		return err
	}
	desiredEnabled := inbound.Enable || (!inbound.Enable && inbound.Total > 0 && usedBytes >= inbound.Total)
	journal, _, err := store.beginThreeXUIReset(ctx, command.OperationKey, command.ServiceID, command.ExpectedNextResetAt, command.PlanRevision, targetID, targetTag, usedBytes, desiredEnabled)
	if err != nil {
		return err
	}
	if journal.Status == "completed" {
		return nil
	}
	if journal.Status == "restore_pending" || journal.Status == "restore_pending_applied" {
		applied := journal.Status == "restore_pending_applied"
		if err := restoreThreeXUIResetDesiredEnabled(ctx, centralURL, centralToken, command, journal); err != nil {
			return err
		}
		retryStatus := "retry"
		if applied {
			retryStatus = "retry_applied"
		}
		if err := store.markThreeXUIResetRecovery(ctx, journal.OperationKey, retryStatus, ""); err != nil {
			return err
		}
		journal.Status = retryStatus
	}
	if journal.Status == "retry" {
		if err := store.transitionThreeXUIReset(ctx, command.OperationKey, "retry", "disable_started", ""); err != nil {
			return err
		}
		journal.Status = "disable_started"
	}
	// A cancelled journal means an explicit plan update restored the inbound
	// before superseding this boundary. If that update never commits at Center,
	// the original boundary remains authoritative and must still be replayable.
	if journal.Status == "cancelled" {
		if err := store.transitionThreeXUIReset(ctx, command.OperationKey, "cancelled", "disable_started", ""); err != nil {
			return err
		}
		journal.Status = "disable_started"
	}
	if journal.Status == "retry_applied" || journal.Status == "cancelled_applied" {
		from := journal.Status
		if command.TargetNodeID > 0 {
			if err := setAndVerifyThreeXUIInboundEnabled(ctx, centralURL, centralToken, command.InboundID, false, command.InboundTag, command.TargetNodeID, true); err != nil {
				return err
			}
		}
		if err := setAndVerifyThreeXUIInboundEnabled(ctx, targetURL, targetToken, targetID, false, targetTag, command.TargetNodeID, false); err != nil {
			return err
		}
		current, _, currentErr := getThreeXUIRealityInbound(ctx, targetURL, targetToken, targetID)
		if currentErr != nil {
			return currentErr
		}
		if normalizedThreeXUIInboundTag(current.Tag, command.TargetNodeID) != normalizedThreeXUIInboundTag(targetTag, command.TargetNodeID) {
			return errors.New("agent: target REALITY inbound identity changed while resuming an applied traffic reset")
		}
		currentUsed, currentErr := threeXUIInboundUsedBytes(current)
		if currentErr != nil {
			return currentErr
		}
		if err := store.markThreeXUIResetApplied(ctx, command.OperationKey, from, currentUsed); err != nil {
			return err
		}
		journal.Status = "reset_applied"
		journal.SyncUsedBytes = currentUsed
	}
	if journal.Status == "disable_started" {
		if command.TargetNodeID > 0 {
			if err := setAndVerifyThreeXUIInboundEnabled(ctx, centralURL, centralToken, command.InboundID, false, command.InboundTag, command.TargetNodeID, true); err != nil {
				return err
			}
		}
		if err := setAndVerifyThreeXUIInboundEnabled(ctx, targetURL, targetToken, targetID, false, targetTag, command.TargetNodeID, false); err != nil {
			return err
		}
		disabledInbound, _, disabledErr := getThreeXUIRealityInbound(ctx, targetURL, targetToken, targetID)
		if disabledErr != nil {
			return disabledErr
		}
		if normalizedThreeXUIInboundTag(disabledInbound.Tag, command.TargetNodeID) != normalizedThreeXUIInboundTag(targetTag, command.TargetNodeID) || disabledInbound.Enable {
			return errors.New("agent: target REALITY inbound was not stably disabled before recording its traffic reset")
		}
		if err := validateThreeXUIInboundPlanConfiguration(disabledInbound, command.InboundTotalBytes); err != nil {
			return err
		}
		disabledUsed, disabledErr := threeXUIInboundUsedBytes(disabledInbound)
		if disabledErr != nil {
			return disabledErr
		}
		if err := store.markThreeXUIResetDisabled(ctx, command.OperationKey, "disable_started", disabledUsed); err != nil {
			return err
		}
		journal.Status = "disabled"
		journal.SyncUsedBytes = disabledUsed
	}
	if journal.Status == "disabled" {
		if command.TargetNodeID > 0 {
			centralCurrent, _, centralErr := getThreeXUIRealityInbound(ctx, centralURL, centralToken, command.InboundID)
			if centralErr != nil {
				return centralErr
			}
			if normalizedThreeXUIInboundTag(centralCurrent.Tag, command.TargetNodeID) != normalizedThreeXUIInboundTag(command.InboundTag, command.TargetNodeID) || !threeXUIInboundMatchesNode(centralCurrent, command.TargetNodeID) || centralCurrent.Enable {
				return errors.New("agent: central REALITY inbound was not stably disabled before resetting worker traffic")
			}
			if err := validateThreeXUIInboundPlanConfiguration(centralCurrent, command.InboundTotalBytes); err != nil {
				return err
			}
		}
		current, _, currentErr := getThreeXUIRealityInbound(ctx, targetURL, targetToken, targetID)
		if currentErr != nil {
			return currentErr
		}
		if normalizedThreeXUIInboundTag(current.Tag, command.TargetNodeID) != normalizedThreeXUIInboundTag(targetTag, command.TargetNodeID) {
			return errors.New("agent: target REALITY inbound identity changed while resetting traffic")
		}
		if current.Enable {
			return errors.New("agent: target REALITY inbound was re-enabled before its traffic reset")
		}
		if err := validateThreeXUIInboundPlanConfiguration(current, command.InboundTotalBytes); err != nil {
			return err
		}
		currentUsed, currentErr := threeXUIInboundUsedBytes(current)
		if currentErr != nil {
			return currentErr
		}
		if currentUsed != 0 && currentUsed != journal.SyncUsedBytes {
			return errors.New("agent: disabled REALITY inbound usage changed before its reset could be proven")
		}
		var resetErr error
		if currentUsed > 0 {
			_, resetErr = threeXUIAPI(ctx, http.MethodPost, targetURL+"/panel/api/inbounds/"+strconv.Itoa(targetID)+"/resetTraffic", targetToken, "application/json", map[string]any{})
		}
		postReset, err := verifyThreeXUIInboundUsageReset(ctx, targetURL, targetToken, targetID, targetTag, command.TargetNodeID, command.InboundTotalBytes)
		if err != nil {
			if resetErr != nil {
				return errors.Join(fmt.Errorf("agent: reset disabled REALITY inbound traffic: %w", resetErr), err)
			}
			return err
		}
		if err := store.markThreeXUIResetApplied(ctx, command.OperationKey, "disabled", 0); err != nil {
			return err
		}
		journal.Status = "reset_applied"
		journal.SyncUsedBytes = 0
		if postReset.Enable {
			return errors.New("agent: target REALITY inbound was re-enabled while applying its traffic reset")
		}
	}
	if journal.Status == "reset_applied" {
		if command.TargetNodeID > 0 {
			if err := waitForThreeXUICentralResetSync(ctx, centralURL, centralToken, command, journal.SyncUsedBytes); err != nil {
				return err
			}
		}
		if err := store.transitionThreeXUIReset(ctx, command.OperationKey, "reset_applied", "reset_done", ""); err != nil {
			return err
		}
		journal.Status = "reset_done"
	}
	if journal.Status == "reset_done" {
		if command.TargetNodeID > 0 {
			if err := setAndVerifyThreeXUIInboundEnabled(ctx, centralURL, centralToken, command.InboundID, journal.DesiredEnabled, command.InboundTag, command.TargetNodeID, true); err != nil {
				return err
			}
		}
		if err := setAndVerifyThreeXUIInboundEnabled(ctx, targetURL, targetToken, targetID, journal.DesiredEnabled, targetTag, command.TargetNodeID, false); err != nil {
			return err
		}
		if err := store.transitionThreeXUIReset(ctx, command.OperationKey, "reset_done", "enable_done", ""); err != nil {
			return err
		}
		journal.Status = "enable_done"
	}
	if journal.Status == "enable_done" {
		if err := verifyThreeXUIInboundEnabled(ctx, targetURL, targetToken, targetID, journal.DesiredEnabled, targetTag, command.TargetNodeID, false); err != nil {
			return err
		}
		targetInbound, _, targetErr := getThreeXUIRealityInbound(ctx, targetURL, targetToken, targetID)
		if targetErr != nil {
			return targetErr
		}
		if err := validateThreeXUIInboundPlanConfiguration(targetInbound, command.InboundTotalBytes); err != nil {
			return err
		}
		if command.TargetNodeID > 0 {
			if err := verifyThreeXUIInboundEnabled(ctx, centralURL, centralToken, command.InboundID, journal.DesiredEnabled, command.InboundTag, command.TargetNodeID, true); err != nil {
				return err
			}
			centralInbound, _, centralErr := getThreeXUIRealityInbound(ctx, centralURL, centralToken, command.InboundID)
			if centralErr != nil {
				return centralErr
			}
			if err := validateThreeXUIInboundPlanConfiguration(centralInbound, command.InboundTotalBytes); err != nil {
				return err
			}
		}
		if err := store.transitionThreeXUIReset(ctx, command.OperationKey, "enable_done", "completed", ""); err != nil {
			return err
		}
		return nil
	}
	return errors.New("agent: REALITY inbound reset journal is invalid")
}

func settleThreeXUIResetBeforeInboundUpdate(ctx context.Context, store *Store, centralURL, centralToken string, command ThreeXUIClientCommandTask) (*threeXUIResetJournal, error) {
	journal, found, err := store.unfinishedThreeXUIResetJournal(ctx, command.ServiceID)
	if err != nil || !found {
		return nil, err
	}
	if normalizedThreeXUIInboundTag(journal.TargetInboundTag, command.TargetNodeID) != normalizedThreeXUIInboundTag(command.InboundTag, command.TargetNodeID) {
		return nil, errors.New("agent: unfinished REALITY inbound reset belongs to a different managed inbound")
	}
	if err := restoreThreeXUIResetDesiredEnabled(ctx, centralURL, centralToken, command, journal); err != nil {
		message := "plan update restore failed: " + err.Error()
		status := "restore_pending"
		if threeXUIResetJournalApplied(journal.Status) {
			status = "restore_pending_applied"
		}
		if markErr := store.markThreeXUIResetRecovery(ctx, journal.OperationKey, status, message); markErr != nil {
			return nil, errors.Join(err, markErr)
		}
		return nil, fmt.Errorf("agent: finish the pending REALITY inbound reset recovery before updating its plan: %w", err)
	}
	status := "retry"
	if threeXUIResetJournalApplied(journal.Status) {
		status = "retry_applied"
	}
	if err := store.markThreeXUIResetRecovery(ctx, journal.OperationKey, status, "restored before applying an explicit traffic plan update"); err != nil {
		return nil, err
	}
	journal.Status = status
	return &journal, nil
}

func restoreThreeXUIResetDesiredEnabled(ctx context.Context, centralURL, centralToken string, command ThreeXUIClientCommandTask, journal threeXUIResetJournal) error {
	targetURL, targetToken, targetID, targetTag, err := resolveThreeXUIInboundTarget(ctx, centralURL, centralToken, command)
	if err != nil {
		return err
	}
	if targetID != journal.TargetInboundID || normalizedThreeXUIInboundTag(targetTag, command.TargetNodeID) != normalizedThreeXUIInboundTag(journal.TargetInboundTag, command.TargetNodeID) {
		return errors.New("agent: unfinished REALITY inbound reset target identity changed")
	}
	if command.TargetNodeID > 0 {
		if err := setAndVerifyThreeXUIInboundEnabled(ctx, centralURL, centralToken, command.InboundID, journal.DesiredEnabled, command.InboundTag, command.TargetNodeID, true); err != nil {
			return err
		}
	}
	if err := setAndVerifyThreeXUIInboundEnabled(ctx, targetURL, targetToken, targetID, journal.DesiredEnabled, targetTag, command.TargetNodeID, false); err != nil {
		return err
	}
	return nil
}

func verifyThreeXUIInboundUsageReset(ctx context.Context, baseURL, token string, inboundID int, expectedTag string, nodeID int, totalBytes int64) (threeXUIRealityInbound, error) {
	inbound, _, err := getThreeXUIRealityInbound(ctx, baseURL, token, inboundID)
	if err != nil {
		return threeXUIRealityInbound{}, err
	}
	if normalizedThreeXUIInboundTag(inbound.Tag, nodeID) != normalizedThreeXUIInboundTag(expectedTag, nodeID) {
		return threeXUIRealityInbound{}, errors.New("agent: managed REALITY inbound identity changed while verifying its traffic reset")
	}
	if err := validateThreeXUIInboundPlanConfiguration(inbound, totalBytes); err != nil {
		return threeXUIRealityInbound{}, err
	}
	usedBytes, err := threeXUIInboundUsedBytes(inbound)
	if err != nil {
		return threeXUIRealityInbound{}, err
	}
	if usedBytes != 0 {
		return threeXUIRealityInbound{}, errors.New("agent: REALITY inbound traffic reset has not reached the target node")
	}
	return inbound, nil
}

func waitForThreeXUICentralResetSync(ctx context.Context, centralURL, centralToken string, command ThreeXUIClientCommandTask, expectedUsedBytes int64) error {
	deadline := time.NewTimer(threeXUICentralResetSyncTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(threeXUICentralResetPollInterval)
	defer ticker.Stop()
	var lastError error
	for {
		inbound, _, err := getThreeXUIRealityInbound(ctx, centralURL, centralToken, command.InboundID)
		if err == nil {
			if normalizedThreeXUIInboundTag(inbound.Tag, command.TargetNodeID) != normalizedThreeXUIInboundTag(command.InboundTag, command.TargetNodeID) || !threeXUIInboundMatchesNode(inbound, command.TargetNodeID) {
				return errors.New("agent: central REALITY inbound identity changed while waiting for worker traffic synchronization")
			}
			if inbound.Enable {
				return errors.New("agent: central REALITY inbound was re-enabled while waiting for worker traffic synchronization")
			}
			usedBytes, usageErr := threeXUIInboundUsedBytes(inbound)
			if usageErr != nil {
				return usageErr
			}
			if usedBytes == expectedUsedBytes {
				if configErr := validateThreeXUIInboundPlanConfiguration(inbound, command.InboundTotalBytes); configErr != nil {
					return configErr
				}
				return nil
			}
			lastError = fmt.Errorf("central 3x-ui reports %d used bytes while target reports %d", usedBytes, expectedUsedBytes)
		} else {
			lastError = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("agent: wait for central 3x-ui worker traffic synchronization: %w", lastError)
		case <-ticker.C:
		}
	}
}

func resolveThreeXUIInboundTarget(ctx context.Context, centralURL, centralToken string, command ThreeXUIClientCommandTask) (string, string, int, string, error) {
	if command.TargetNodeID == 0 {
		return centralURL, centralToken, command.InboundID, command.InboundTag, nil
	}
	if net.ParseIP(strings.TrimSpace(command.TargetAddress)) == nil || command.TargetPanelPort < 1024 || command.TargetPanelPort > 65535 || strings.TrimSpace(command.TargetAPIToken) == "" {
		return "", "", 0, "", errors.New("agent: target REALITY worker API connection is unavailable")
	}
	targetURL := "http://" + net.JoinHostPort(command.TargetAddress, strconv.Itoa(command.TargetPanelPort))
	targetTag := normalizedThreeXUIInboundTag(command.InboundTag, command.TargetNodeID)
	payload, err := threeXUIAPI(ctx, http.MethodGet, targetURL+"/panel/api/inbounds/list", command.TargetAPIToken, "", nil)
	if err != nil {
		return "", "", 0, "", fmt.Errorf("agent: list target REALITY worker inbounds: %w", err)
	}
	var inbounds []threeXUIRealityInbound
	if json.Unmarshal(payload, &inbounds) != nil {
		return "", "", 0, "", errors.New("agent: target REALITY worker returned invalid inbounds")
	}
	matchedID := 0
	for _, inbound := range inbounds {
		if inbound.Protocol == "vless" && normalizedThreeXUIInboundTag(inbound.Tag, command.TargetNodeID) == targetTag {
			if matchedID != 0 {
				return "", "", 0, "", errors.New("agent: target REALITY worker has duplicate managed inbound tags")
			}
			matchedID = inbound.ID
		}
	}
	if matchedID < 1 {
		return "", "", 0, "", errors.New("agent: target REALITY worker inbound is unavailable")
	}
	return targetURL, strings.TrimSpace(command.TargetAPIToken), matchedID, targetTag, nil
}

func normalizedThreeXUIInboundTag(tag string, nodeID int) string {
	tag = strings.TrimSpace(tag)
	if nodeID > 0 {
		tag = strings.TrimPrefix(tag, "n"+strconv.Itoa(nodeID)+"-")
	}
	return tag
}

func getThreeXUIRealityInbound(ctx context.Context, baseURL, token string, inboundID int) (threeXUIRealityInbound, map[string]any, error) {
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/get/"+strconv.Itoa(inboundID), token, "", nil)
	if err != nil {
		return threeXUIRealityInbound{}, nil, fmt.Errorf("agent: get 3x-ui inbound traffic plan: %w", err)
	}
	var inbound threeXUIRealityInbound
	var update map[string]any
	if json.Unmarshal(payload, &inbound) != nil || json.Unmarshal(payload, &update) != nil || inbound.ID != inboundID || inbound.Protocol != "vless" {
		return threeXUIRealityInbound{}, nil, errors.New("agent: 3x-ui returned invalid inbound traffic plan data")
	}
	var stream struct {
		Security string `json:"security"`
	}
	if json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Security != "reality" {
		return threeXUIRealityInbound{}, nil, errors.New("agent: selected inbound is not VLESS REALITY")
	}
	return inbound, update, nil
}

func threeXUIInboundUsedBytes(inbound threeXUIRealityInbound) (int64, error) {
	if inbound.Up < 0 || inbound.Down < 0 || inbound.Up > int64(^uint64(0)>>1)-inbound.Down {
		return 0, errors.New("agent: 3x-ui returned invalid inbound traffic usage")
	}
	return inbound.Up + inbound.Down, nil
}

func setAndVerifyThreeXUIInboundEnabled(ctx context.Context, baseURL, token string, inboundID int, enabled bool, expectedTag string, nodeID int, requireNodeMatch bool) error {
	inbound, _, err := getThreeXUIRealityInbound(ctx, baseURL, token, inboundID)
	if err != nil {
		return err
	}
	if normalizedThreeXUIInboundTag(inbound.Tag, nodeID) != normalizedThreeXUIInboundTag(expectedTag, nodeID) || (requireNodeMatch && !threeXUIInboundMatchesNode(inbound, nodeID)) {
		return errors.New("agent: managed REALITY inbound identity changed before restoring its enabled state")
	}
	if inbound.Enable == enabled {
		return nil
	}
	if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/setEnable/"+strconv.Itoa(inboundID), token, "application/json", map[string]any{"enable": enabled}); err != nil {
		return fmt.Errorf("agent: restore 3x-ui inbound enabled state after traffic reset: %w", err)
	}
	return verifyThreeXUIInboundEnabled(ctx, baseURL, token, inboundID, enabled, expectedTag, nodeID, requireNodeMatch)
}

func verifyThreeXUIInboundEnabled(ctx context.Context, baseURL, token string, inboundID int, enabled bool, expectedTag string, nodeID int, requireNodeMatch bool) error {
	inbound, _, err := getThreeXUIRealityInbound(ctx, baseURL, token, inboundID)
	if err != nil {
		return err
	}
	if normalizedThreeXUIInboundTag(inbound.Tag, nodeID) != normalizedThreeXUIInboundTag(expectedTag, nodeID) || (requireNodeMatch && !threeXUIInboundMatchesNode(inbound, nodeID)) {
		return errors.New("agent: managed REALITY inbound identity changed while restoring its enabled state")
	}
	if inbound.Enable != enabled {
		return errors.New("agent: 3x-ui inbound enabled state was not restored after traffic reset")
	}
	return nil
}

func observeThreeXUIClientInbounds(ctx context.Context, baseURL, token string, expected []ThreeXUIClientInbound) ([]ThreeXUIClientInbound, error) {
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/list", token, "", nil)
	if err != nil {
		return nil, fmt.Errorf("agent: observe 3x-ui inbound traffic plans: %w", err)
	}
	var inbounds []threeXUIRealityInbound
	if json.Unmarshal(payload, &inbounds) != nil {
		return nil, errors.New("agent: 3x-ui returned invalid inbound traffic plan observations")
	}
	observed := make(map[int]threeXUIRealityInbound, len(inbounds))
	for _, inbound := range inbounds {
		observed[inbound.ID] = inbound
	}
	result := make([]ThreeXUIClientInbound, 0, len(expected))
	for _, reference := range expected {
		inbound, ok := observed[reference.ID]
		if !ok || inbound.Protocol != "vless" || inbound.Up < 0 || inbound.Down < 0 || inbound.Total < 0 || inbound.Up > int64(^uint64(0)>>1)-inbound.Down {
			return nil, errors.New("agent: a managed 3x-ui inbound traffic plan is unavailable")
		}
		var stream struct {
			Security string `json:"security"`
		}
		if json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Security != "reality" {
			return nil, errors.New("agent: managed inbound is not VLESS REALITY")
		}
		reference.Enabled = inbound.Enable
		reference.TotalBytes = inbound.Total
		reference.UsedBytes = inbound.Up + inbound.Down
		reference.InboundTag = inbound.Tag
		result = append(result, reference)
	}
	return result, nil
}
