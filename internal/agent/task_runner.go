package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/ipquality"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/nodediagnostics"
	"github.com/petauron/vastora/internal/nodeprotocol"
	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/pulse"
	"github.com/petauron/vastora/internal/xrayrecovery"
)

func (c Client) RunTasks(ctx context.Context, store *Store, report func(error)) {
	reportError := func(err error) {
		if err != nil && report != nil && ctx.Err() == nil {
			report(err)
		}
	}
	for {
		session, err := newExecutionSessionID()
		if err != nil {
			reportError(err)
			return
		}
		c.executionSession = session
		for {
			if err := c.registerExecutionSession(ctx, store); err == nil {
				break
			} else {
				reportError(err)
			}
			if !waitForTaskRetry(ctx) {
				return
			}
		}
		if !c.transferLegacyReceiptsBeforeTasks(ctx, store, reportError) {
			return
		}
		for {
			claimContext, cancel := context.WithTimeout(ctx, 15*time.Second)
			task, err := c.claimNextTask(claimContext, store, 10*time.Second)
			cancel()
			if err != nil {
				reportError(err)
				if !waitForTaskRetry(ctx) {
					return
				}
				continue
			}
			if task == nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			if c.processTaskWithLease(ctx, store, *task, report) {
				// The previous authorization is terminal. A new session fences
				// that execution at Center before any other task can be claimed.
				break
			}
		}
		if !waitForTaskRetry(ctx) {
			return
		}
	}
}

func freshTaskControlContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), taskControlTimeout)
}

func (c Client) processTaskWithLease(parent context.Context, store *Store, task DeploymentTask, report func(error)) bool {
	return c.processTaskWithLeaseInterval(parent, store, task, report, taskLeaseRenewInterval)
}

func (c Client) processTaskWithLeaseInterval(parent context.Context, store *Store, task DeploymentTask, report func(error), interval time.Duration) bool {
	c.execution = task.Authorization
	executionContext, cancelExecution, release, err := store.beginExecution(parent)
	if err != nil {
		if report != nil {
			report(err)
		}
		return false
	}
	defer release()
	c.executionAbort = cancelExecution
	if err := c.executionTransition(executionContext, store, "start", "", false, ""); err != nil {
		if report != nil {
			report(err)
		}
		return terminalTaskAuthorityConflict(err)
	}
	renewalResult := make(chan error, 1)
	go func() {
		err := c.renewTaskLeaseLoop(executionContext, store, task, interval)
		if err != nil {
			cancelExecution(err)
		}
		renewalResult <- err
	}()
	processErr := c.processTask(executionContext, store, task, report)
	cancelExecution(context.Canceled)
	renewalErr := <-renewalResult
	if renewalErr != nil && parent.Err() == nil && report != nil {
		report(renewalErr)
	}
	return terminalTaskAuthorityConflict(renewalErr) || terminalTaskAuthorityConflict(processErr)
}

func (c Client) renewTaskLeaseLoop(ctx context.Context, store *Store, task DeploymentTask, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("agent: task lease renewal interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			requestContext, cancel := context.WithTimeout(ctx, taskControlTimeout)
			err := c.executionTransition(requestContext, store, "renew", "", false, "")
			if err == nil {
				err = c.renewTaskLease(requestContext, store, task.ID, task.Attempt)
			}
			cancel()
			if err != nil {
				return fmt.Errorf("agent: renew task lease: %w", err)
			}
		}
	}
}

func waitForTaskRetry(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Second):
		return true
	}
}

func (c Client) processTask(ctx context.Context, store *Store, task DeploymentTask, report func(error)) error {
	if err := c.executionTransition(ctx, store, "step", "apply", false, ""); err != nil {
		if report != nil {
			report(err)
		}
		return err
	}
	var result ApplicationTaskResult
	var err error
	if task.Kind == "application.apply" {
		// Hold through RecordApplied/RemoveApplied as well as Deploy: landing
		// must never prepare a route against an installation being replaced.
		store.landingMutationMu.Lock()
		defer store.landingMutationMu.Unlock()
	}
	decommissionHandedOff := false
	updateHandedOff := false
	switch task.Kind {
	case ipquality.Kind:
		checker, ok := c.Executor.(interface {
			CheckIPQuality(context.Context, ipquality.Task) (ipquality.Result, error)
		})
		if !ok || !c.Capabilities.IPQuality || !c.Capabilities.Docker || task.IPQuality == nil {
			err = errors.New("agent: IP quality capability is not configured")
		} else {
			value, checkErr := checker.CheckIPQuality(ctx, *task.IPQuality)
			result.IPQuality, err = &value, checkErr
		}
	case nodediagnostics.NetworkKind:
		checker, ok := c.Executor.(interface {
			CheckNetworkQuality(context.Context, nodediagnostics.Task) (nodediagnostics.Result, error)
		})
		if !ok || !c.Capabilities.NetworkDiagnostics || task.NodeDiagnostics == nil {
			err = errors.New("agent: network diagnostics capability is not configured")
		} else {
			value, checkErr := checker.CheckNetworkQuality(ctx, *task.NodeDiagnostics)
			result.NodeDiagnostics, err = &value, checkErr
		}
	case nodediagnostics.ReturnRouteKind:
		checker, ok := c.Executor.(interface {
			CheckReturnRoutes(context.Context, nodediagnostics.Task) (nodediagnostics.Result, error)
		})
		if !ok || !c.Capabilities.ReturnRoute || task.NodeDiagnostics == nil {
			err = errors.New("agent: return-route capability is not configured")
		} else {
			value, checkErr := checker.CheckReturnRoutes(ctx, *task.NodeDiagnostics)
			result.NodeDiagnostics, err = &value, checkErr
		}
	case nodediagnostics.BandwidthKind:
		checker, ok := c.Executor.(interface {
			CheckInternationalBandwidth(context.Context, nodediagnostics.Task) (nodediagnostics.Result, error)
		})
		if !ok || !c.Capabilities.BandwidthDiagnostics || task.NodeDiagnostics == nil {
			err = errors.New("agent: bandwidth diagnostics capability is not configured")
		} else {
			value, checkErr := checker.CheckInternationalBandwidth(ctx, *task.NodeDiagnostics)
			result.NodeDiagnostics, err = &value, checkErr
		}
	case nodediagnostics.HostProfileKind:
		checker, ok := c.Executor.(interface {
			CheckHostProfile(context.Context, nodediagnostics.Task) (nodediagnostics.Result, error)
		})
		if !ok || !c.Capabilities.HostProfile || task.NodeDiagnostics == nil {
			err = errors.New("agent: host profile capability is not configured")
		} else {
			value, checkErr := checker.CheckHostProfile(ctx, *task.NodeDiagnostics)
			result.NodeDiagnostics, err = &value, checkErr
		}
	case xrayrecovery.InspectKind:
		manager, ok := c.Executor.(interface {
			InspectXrayConfiguration(context.Context, xrayrecovery.Task) (xrayrecovery.Result, error)
		})
		if !ok || task.XrayRecovery == nil {
			err = errors.New("agent: Xray configuration recovery is unavailable")
		} else {
			value, recoveryErr := manager.InspectXrayConfiguration(ctx, *task.XrayRecovery)
			result.XrayRecovery, err = &value, recoveryErr
		}
	case xrayrecovery.ApplyKind:
		manager, ok := c.Executor.(interface {
			ApplyXrayConfigurationRecovery(context.Context, xrayrecovery.Task) (xrayrecovery.Result, error)
		})
		if !ok || task.XrayRecovery == nil {
			err = errors.New("agent: Xray configuration recovery is unavailable")
		} else {
			value, recoveryErr := manager.ApplyXrayConfigurationRecovery(ctx, *task.XrayRecovery)
			result.XrayRecovery, err = &value, recoveryErr
		}
	case "application.apply":
		if task.RequiredRuntimeGeneration < 0 || task.RequiredRuntimeGeneration > platform.ApplicationRuntimeGeneration {
			err = fmt.Errorf("agent: application task requires runtime generation %d, executor is generation %d", task.RequiredRuntimeGeneration, platform.ApplicationRuntimeGeneration)
		} else if c.Executor == nil {
			err = errors.New("agent: application capability is not configured")
		} else if task.AppKey != komariKey && task.AppKey != pulse.AgentKey && !c.Capabilities.Docker {
			err = errors.New("agent: Docker capability is not configured")
		} else {
			landingState, landingErr := store.prepareLandingXrayRuntimeMigration(ctx, task)
			if landingErr != nil {
				err = landingErr
			} else {
				result, err = c.Executor.Deploy(ctx, task)
				err = store.finishLandingXrayRuntimeMigration(ctx, landingState, err)
			}
		}
	case "application.command":
		// Commands may restart the panel or replace its database. Do not let
		// them race a landing checkpoint, route write, or container restart.
		store.landingMutationMu.Lock()
		defer store.landingMutationMu.Unlock()
		commands := 0
		if task.PulseEnrollment != nil {
			commands++
		}
		if task.ProtocolCommand != nil {
			commands++
		}
		if task.ApplicationCommand != nil {
			commands++
		}
		if task.SubscriptionCommand != nil {
			commands++
		}
		if task.ClientCommand != nil {
			commands++
		}
		if task.NodeCommand != nil {
			commands++
		}
		if task.ControllerCommand != nil {
			commands++
		}
		if task.MeridianRuntime != nil {
			commands++
		}
		if task.MeridianLegacyExport != nil {
			commands++
		}
		if task.MeridianLegacyRetire != nil {
			commands++
		}
		if !c.Capabilities.Docker || commands != 1 {
			err = errors.New("agent: application command received without Docker capability")
		} else if task.MeridianLegacyExport != nil {
			var exportResult meridianruntime.LegacyExportResult
			exportResult, err = exportLegacyMeridianState(ctx, store, *task.MeridianLegacyExport)
			if err == nil {
				result.MeridianLegacyExport = &exportResult
			}
		} else if task.MeridianRuntime != nil {
			executor, ok := c.Executor.(interface {
				ApplyMeridianRuntime(context.Context, meridianruntime.Task) (meridianruntime.Result, error)
			})
			if !ok {
				err = errors.New("agent: Meridian runtime capability is not configured")
			} else {
				var runtimeResult meridianruntime.Result
				runtimeResult, err = executor.ApplyMeridianRuntime(ctx, *task.MeridianRuntime)
				if err == nil {
					result.MeridianRuntime = &runtimeResult
				}
			}
		} else if task.MeridianLegacyRetire != nil {
			executor, ok := c.Executor.(interface {
				RetireLegacyMeridianInstallation(context.Context, meridianruntime.LegacyRetireTask) (meridianruntime.LegacyRetireResult, error)
			})
			if !ok {
				err = errors.New("agent: Meridian legacy retirement capability is not configured")
			} else {
				var retireResult meridianruntime.LegacyRetireResult
				retireResult, err = executor.RetireLegacyMeridianInstallation(ctx, *task.MeridianLegacyRetire)
				if err == nil {
					result.MeridianLegacyRetire = &retireResult
				}
			}
		} else if task.PulseEnrollment != nil {
			executor, ok := c.Executor.(interface {
				EnrollPulse(context.Context, pulse.EnrollmentTask) (pulse.EnrollmentResult, error)
			})
			if !ok {
				err = errors.New("agent: Pulse enrollment capability is not configured")
			} else {
				var enrollment pulse.EnrollmentResult
				enrollment, err = executor.EnrollPulse(ctx, *task.PulseEnrollment)
				if err == nil {
					result.PulseEnrollment = &enrollment
				}
			}
		} else if task.ProtocolCommand != nil {
			var commandResult nodeprotocol.Result
			commandResult, err = c.applyNodeProtocols(ctx, store, *task.ProtocolCommand)
			if err == nil {
				result.ProtocolCommand = &commandResult
			}
		} else if task.ApplicationCommand != nil {
			var commandResult RealityCommandResult
			commandResult, err = applyRealityCommand(ctx, store, task.ID, task.Attempt, *task.ApplicationCommand)
			if err == nil && task.ApplicationCommand.Action == "remove" && task.ApplicationCommand.RemoveHY2 {
				executor, ok := c.Executor.(interface {
					ConfigureHY2Port(context.Context, *Store, string, bool) error
				})
				if !ok {
					err = uncertainTaskOutcome(errors.New("agent: protocol port cleanup is unavailable"))
				} else {
					err = executor.ConfigureHY2Port(ctx, store, task.ApplicationCommand.TargetApplicationID, false)
				}
			}
			// Preserve partial results in encrypted Center evidence even when a
			// later step failed. This does not mark the command successful.
			result.ApplicationCommand = &commandResult
		} else if task.SubscriptionCommand != nil {
			var commandResult SubscriptionCommandResult
			commandResult, err = applySubscriptionCommand(ctx, store, *task.SubscriptionCommand)
			if err == nil {
				result.SubscriptionCommand = &commandResult
			}
		} else if task.ClientCommand != nil {
			var commandResult ThreeXUIClientCommandResult
			commandResult, err = applyThreeXUIClientCommand(ctx, store, *task.ClientCommand)
			if err == nil {
				result.ClientCommand = &commandResult
			}
		} else if task.NodeCommand != nil {
			var commandResult ThreeXUINodeCommandResult
			commandResult, err = applyThreeXUINodeCommand(ctx, store, *task.NodeCommand)
			if err == nil {
				result.NodeCommand = &commandResult
			}
		} else {
			var commandResult ThreeXUIControllerCommandResult
			commandResult, err = c.applyThreeXUIControllerCommand(ctx, store, task.ID, *task.ControllerCommand)
			if err == nil {
				result.ControllerCommand = &commandResult
			}
		}
	case "gateway.routes.apply":
		if task.GatewayState == nil || !c.Capabilities.Gateway {
			err = errors.New("agent: gateway task received without gateway capability")
		} else {
			err = applyGatewayDesiredState(ctx, store, c.GatewayDriver, *task.GatewayState, task.GatewayCertificates)
		}
	case "gateway.component.apply":
		if c.GatewayProvisioner == nil || !c.Capabilities.Gateway {
			err = errors.New("agent: gateway provisioning capability is not configured")
		} else {
			store.gatewayMutationMu.Lock()
			if task.Operation == "running" {
				err = c.GatewayProvisioner.Ensure(ctx)
				if err == nil {
					err = waitForGateway(ctx, c.GatewayDriver)
				}
			} else if task.Operation == "stopped" {
				err = c.GatewayProvisioner.Remove(ctx)
				if err == nil {
					err = store.ClearGatewayState(ctx)
				}
			} else {
				err = errors.New("agent: invalid gateway component operation")
			}
			store.gatewayMutationMu.Unlock()
		}
	case "landing.server.apply":
		err = c.applyLandingServerTask(ctx, store, task)
		if err == nil && task.LandingServerState.Plan != nil {
			var peer landing.PeerIdentity
			peer, err = store.linkChecker.SelfIdentity(ctx, task.LandingServerState.Plan.Address)
			if err == nil {
				result.LandingPeer = &peer
			}
		}
	case "landing.proxy.apply":
		if !c.Capabilities.Docker || task.LandingProxyState == nil || task.Revision <= 0 || uint64(task.Revision) != task.LandingProxyState.Revision {
			err = errors.New("agent: invalid landing proxy task")
		} else {
			err = store.applyLandingProxy(ctx, *task.LandingProxyState)
		}
	case "node.listener.apply":
		if task.NodeListenerState == nil || !c.Capabilities.Docker {
			err = errors.New("agent: node listener task received without Docker capability")
		} else {
			coordinator, _ := c.GatewayDriver.(NodeListenerCoordinator)
			err = applyNodeListenerState(ctx, store, c.NodeListener, coordinator, *task.NodeListenerState)
		}
	case "tunnel.state.apply":
		if c.TunnelProvisioner == nil || !c.Capabilities.Tunnel || task.TunnelState == nil {
			err = errors.New("agent: tunnel task received without tunnel capability")
		} else {
			err = c.TunnelProvisioner.Apply(ctx, *task.TunnelState)
		}
	case "agent.decommission":
		if c.Decommissioner == nil {
			err = errors.New("agent: host decommission capability is not configured")
		} else {
			var callbackURL string
			callbackURL, err = normalizeHostDecommissionCallbackURL(task.DecommissionCallbackURL, task.ID)
			if err == nil && strings.TrimSpace(task.DecommissionCallbackToken) == "" {
				err = errors.New("agent: host decommission callback token is missing")
			}
			var connection Connection
			if err == nil {
				connection, err = store.Connection(ctx)
			}
			if err == nil {
				err = c.executionTransition(ctx, store, "step", "handoff", false, "")
				if err == nil {
					err = c.Decommissioner.ScheduleFinalRemoval(ctx, HostDecommissionRequest{ExecutionID: c.execution.ID, SessionID: c.executionSession, TaskID: task.ID, Attempt: task.Attempt, DeleteData: task.DeleteData, CallbackURL: callbackURL, CallbackToken: task.DecommissionCallbackToken, Connection: connection})
				}
				decommissionHandedOff = err == nil
			}
		}
	case "agent.update":
		if c.Updater == nil {
			err = errors.New("agent: host update capability is not configured")
		} else if strings.TrimSpace(task.TargetVersion) == "" {
			err = errors.New("agent: update target version is missing")
		} else {
			var connection Connection
			connection, err = store.Connection(ctx)
			if err == nil {
				err = c.executionTransition(ctx, store, "step", "handoff", false, "")
				if err == nil {
					err = c.Updater.ScheduleUpdate(ctx, HostUpdateRequest{ExecutionID: c.execution.ID, SessionID: c.executionSession, TaskID: task.ID, Attempt: task.Attempt, TargetVersion: task.TargetVersion, Connection: connection})
				}
				updateHandedOff = err == nil
			}
		}
	default:
		err = errors.New("agent: unsupported task kind")
	}
	if decommissionHandedOff {
		// The persistent host helper owns the terminal result. A successful
		// schedule is not evidence that host cleanup itself has completed.
		return nil
	}
	if updateHandedOff {
		// The persistent updater owns binary replacement, rollback, restart, and
		// terminal reporting. A successful schedule is not an update result.
		return nil
	}
	if task.Kind == "application.apply" && task.Operation != "uninstall" && len(result.GeneratedSecrets) != 0 {
		merged, mergeErr := mergeGeneratedSecrets(task.Secrets, result.GeneratedSecrets)
		if mergeErr != nil {
			err = errors.Join(err, mergeErr)
		} else {
			task.Secrets = merged
		}
	}
	committedProxyRuntime := task.Kind == "application.apply" && task.Operation != "uninstall" && proxyRuntimeApp(task.AppKey) && strings.TrimSpace(result.GeneratedSecrets["api_token"]) != ""
	if err != nil && committedProxyRuntime {
		err = uncertainTaskOutcome(err)
	}
	if err == nil && task.Kind == "application.apply" && task.Operation != "uninstall" {
		err = c.executionTransition(ctx, store, "step", "persist", false, "")
		if err == nil {
			_, err = store.RecordApplied(ctx, AppliedInstallation{InstanceID: task.ID, ApplicationID: task.ApplicationID, AppKey: task.AppKey, Version: task.Manifest.Version, Config: task.Config, Secrets: task.Secrets, ServiceAddress: task.ServiceAddress, Manifest: task.Manifest, ApplicationRole: task.ApplicationRole})
		}
		if err != nil && committedProxyRuntime {
			err = uncertainTaskOutcome(err)
		}
	}
	if err == nil && task.Kind == "application.apply" && task.Operation == "uninstall" {
		err = c.executionTransition(ctx, store, "step", "persist", false, "")
		if err == nil {
			err = store.RemoveApplied(ctx, task.AppKey)
		}
	}
	reconciliationRequired := ctx.Err() != nil || taskOutcomeIsUncertain(err)
	completion := taskCompletion{TaskID: task.ID, Attempt: task.Attempt, Result: result, Error: safeTaskError(err), ReconciliationRequired: reconciliationRequired}
	if task.Kind == "application.apply" {
		completion.ApplicationRuntimeGeneration = platform.ApplicationRuntimeGeneration
	}
	// One bounded report attempt is observation, not permission for another
	// mutation. No result queue or automatic replay survives this execution.
	completionContext, completionCancel := freshTaskControlContext(ctx)
	completeErr := c.sendTaskCompletion(completionContext, store, completion)
	completionCancel()
	if completeErr != nil {
		if report != nil {
			report(completeErr)
		}
	}
	if err != nil && report != nil {
		report(errors.New(safeTaskError(err)))
	}
	return completeErr
}

func terminalTaskAuthorityConflict(err error) bool {
	var response *centerResponseError
	return errors.As(err, &response) && response.status == http.StatusConflict
}

func (c Client) sendTaskCompletion(ctx context.Context, store *Store, completion taskCompletion) error {
	var deploymentErr error
	if completion.Error != "" {
		deploymentErr = errors.New(completion.Error)
	}
	return c.completeTask(ctx, store, completion.TaskID, completion.Attempt, completion.Result, deploymentErr, completion.ReconciliationRequired, completion.ApplicationRuntimeGeneration)
}

func safeTaskError(err error) string {
	if err == nil {
		return ""
	}
	return controlplane.SafeError(err.Error())
}

func mergeGeneratedSecrets(raw json.RawMessage, generated map[string]string) (json.RawMessage, error) {
	values := map[string]any{}
	if len(raw) != 0 && json.Unmarshal(raw, &values) != nil {
		return nil, errors.New("agent: stored task secrets are invalid")
	}
	for key, value := range generated {
		values[key] = value
	}
	return json.Marshal(values)
}

func waitForGateway(ctx context.Context, driver GatewayDriver) error {
	if driver == nil {
		return errors.New("agent: gateway driver is not configured")
	}
	readyContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := driver.Health(readyContext); err == nil {
			return nil
		}
		select {
		case <-readyContext.Done():
			return errors.New("agent: Caddy gateway did not become healthy")
		case <-ticker.C:
		}
	}
}

func (c Client) claimNextTask(ctx context.Context, store *Store, wait time.Duration) (*DeploymentTask, error) {
	connection, err := store.Connection(ctx)
	if err != nil {
		return nil, err
	}
	connection, err = c.ensureConnectionPinned(ctx, store, connection)
	if err != nil {
		return nil, err
	}
	var response struct {
		Task *struct {
			Authorization controlplane.ExecutionAuthorization `json:"authorization"`
			ID            string                              `json:"id"`
			Attempt       int64                               `json:"attempt"`
			Envelope      controlplane.Envelope               `json:"envelope"`
		} `json:"task"`
	}
	endpoint := connection.CenterURL + "/api/v1/agents/" + url.PathEscape(connection.AgentID) + "/tasks/next?wait=" + url.QueryEscape(wait.String())
	if err := c.get(ctx, endpoint, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, &response); err != nil {
		return nil, err
	}
	if response.Task == nil {
		return nil, nil
	}
	aad := controlplane.TaskAdditionalData(connection.AgentID, response.Task.ID, response.Task.Attempt)
	plaintext, err := controlplane.Open(connection.PrivateKey, response.Task.Envelope, aad)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(plaintext)
	if response.Task.Authorization.Protocol != controlplane.ExecutionProtocol || response.Task.Authorization.ID == "" || response.Task.Authorization.Digest != hex.EncodeToString(digest[:]) {
		return nil, errors.New("agent: invalid execution authorization or content digest")
	}
	var task DeploymentTask
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&task); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("agent: Center returned an invalid encrypted task")
	}
	if task.ID != response.Task.ID || task.Attempt != response.Task.Attempt || task.ID == "" || task.Attempt <= 0 {
		return nil, errors.New("agent: encrypted task identity does not match its envelope")
	}
	task.Authorization = response.Task.Authorization
	return &task, nil
}

func (c Client) completeTask(ctx context.Context, store *Store, taskID string, attempt int64, result ApplicationTaskResult, deploymentErr error, reconciliationRequired bool, applicationRuntimeGeneration int) error {
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	connection, err = c.ensureConnectionPinned(ctx, store, connection)
	if err != nil {
		return err
	}
	payload := map[string]any{"attempt": attempt, "succeeded": deploymentErr == nil, "error": "", "result": result, "reconciliationRequired": reconciliationRequired, "applicationRuntimeGeneration": applicationRuntimeGeneration}
	payload["executionId"] = c.execution.ID
	payload["sessionId"] = c.executionSession
	if deploymentErr != nil {
		payload["error"] = deploymentErr.Error()
	}
	return c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/tasks/"+url.PathEscape(taskID)+"/result", payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, nil)
}

func (c Client) renewTaskLease(ctx context.Context, store *Store, taskID string, attempt int64) error {
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	connection, err = c.ensureConnectionPinned(ctx, store, connection)
	if err != nil {
		return err
	}
	payload := map[string]any{"attempt": attempt}
	return c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/tasks/"+url.PathEscape(taskID)+"/lease", payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, nil)
}
