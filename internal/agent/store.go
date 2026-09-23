// Package agent persists the last successfully applied startup configuration on
// a host. It deliberately has no access to Center secrets after a job has been
// applied and never uploads runtime application data.
package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

var errApplicationNotInstalled = errors.New("agent: application is not installed")
var errNoAppliedGatewayState = errors.New("agent: no applied gateway state")
var errNoAppliedNodeListenerState = errors.New("agent: no applied node listener state")

type Store struct {
	executionMu                sync.Mutex
	activeExecution            *activeExecution
	linkChecker                *landing.LinkChecker
	db                         *sql.DB
	key                        []byte
	dataDir                    string
	now                        func() time.Time
	gatewayMutationMu          sync.Mutex
	landingMutationMu          sync.Mutex
	landingCancel              context.CancelFunc
	landingDone                chan struct{}
	landingStatusMu            sync.RWMutex
	landingStatus              landing.MonitorStatus
	landingPeerStatuses        map[string]landingPeerStatus
	meridianPeerStatuses       map[string]meridianruntime.PeerObservation
	meridianMonitorRevision    uint64
	meridianMonitorSHA256      string
	meridianMonitorSource      *landing.PeerIdentity
	landingSubscriptionMu      sync.RWMutex
	landingSubscriptionAddress string
	landingLatencyMu           sync.Mutex
	landingLatencyTargets      []landing.LatencyTarget
	landingLatencyChanged      chan struct{}
	xrayWorkerMu               sync.Mutex
	xrayWorkerStateMu          sync.Mutex
	xrayWorkerServer           *http.Server
	xrayWorkerListener         net.Listener
	xrayWorkerCancel           context.CancelFunc
	xrayWorkerDone             chan struct{}
	runtimeRecoveryMu          sync.RWMutex
	runtimeRecoveryApps        map[string]controlplane.RecoveryApplication
}

type landingPeerStatus struct {
	Peer   landing.PeerIdentity
	Status landing.MonitorStatus
}

type AppliedInstallation struct {
	InstanceID      string              `json:"instanceId"`
	ApplicationID   string              `json:"applicationId"`
	AppKey          string              `json:"appKey"`
	Version         string              `json:"version"`
	Config          json.RawMessage     `json:"config"`
	Secrets         json.RawMessage     `json:"-"`
	ServiceAddress  string              `json:"serviceAddress"`
	ConfigHash      string              `json:"configHash"`
	AppliedAt       time.Time           `json:"appliedAt"`
	Manifest        catalog.AppManifest `json:"-"`
	ApplicationRole string              `json:"-"`
}

type sealedApplicationState struct {
	ApplicationID   string              `json:"applicationId"`
	Config          json.RawMessage     `json:"config"`
	Secrets         json.RawMessage     `json:"secrets"`
	ServiceAddress  string              `json:"serviceAddress"`
	Manifest        catalog.AppManifest `json:"manifest"`
	ApplicationRole string              `json:"applicationRole"`
}

type InstallationStatus struct {
	InstanceID string    `json:"instanceId"`
	AppKey     string    `json:"appKey"`
	Version    string    `json:"version"`
	ConfigHash string    `json:"configHash"`
	AppliedAt  time.Time `json:"appliedAt"`
}

type Connection struct {
	AgentID          string `json:"agentId"`
	Name             string `json:"name"`
	CenterURL        string `json:"centerUrl"`
	Credential       string `json:"-"`
	PrivateKey       []byte `json:"-"`
	CAFingerprint    string `json:"-"`
	CACertificatePEM string `json:"-"`
}

const agentSchemaVersion = 21

// CurrentSchemaVersion is the highest Agent database schema this executable
// can open. The persistent host updater records it before a candidate can
// migrate production state so reboot recovery never selects an older binary.
func CurrentSchemaVersion() int {
	return agentSchemaVersion
}

func (s *Store) Close() error {
	s.stopActiveExecution(errors.New("agent: store is closing"))
	s.linkChecker.Close()
	xrayContext, xrayCancel := context.WithTimeout(context.Background(), 5*time.Second)
	xrayErr := s.stopXrayWorkerAPI(xrayContext, false)
	xrayCancel()
	s.landingMutationMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	err := s.stopLandingMonitor(ctx)
	cancel()
	s.landingMutationMu.Unlock()
	return errors.Join(xrayErr, err, s.db.Close())
}

// RecordApplied stores a configuration only after a deployment executor has
// completed validation, image pull, typed deployment, and health checks. The executor is
// intentionally a separate future component so cache writes cannot claim a
// deployment succeeded by themselves.
