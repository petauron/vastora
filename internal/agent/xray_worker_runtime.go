package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type xrayWorkerApply func(context.Context, xrayWorkerState, xrayWorkerState) error
type xrayWorkerObserve func(context.Context, xrayWorkerState) (xrayWorkerState, error)

func (s *Store) startXrayWorkerAPI(state xrayWorkerState, apply xrayWorkerApply, observe xrayWorkerObserve) error {
	s.xrayWorkerMu.Lock()
	defer s.xrayWorkerMu.Unlock()
	target := net.JoinHostPort(state.Address, strconv.Itoa(state.PanelPort))
	if s.xrayWorkerServer != nil {
		if s.xrayWorkerListener != nil && s.xrayWorkerListener.Addr().String() == target {
			if s.xrayWorkerDone != nil {
				select {
				case <-s.xrayWorkerDone:
					reconcileContext, cancel := context.WithCancel(context.Background())
					done := make(chan struct{})
					s.xrayWorkerCancel, s.xrayWorkerDone = cancel, done
					go s.runXrayWorkerReconciler(reconcileContext, apply, observe, done)
				default:
				}
			}
			return nil
		}
		if s.xrayWorkerCancel != nil {
			s.xrayWorkerCancel()
			s.xrayWorkerCancel = nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.xrayWorkerServer.Shutdown(ctx)
		if s.xrayWorkerDone != nil {
			select {
			case <-s.xrayWorkerDone:
			case <-ctx.Done():
				err = errors.Join(err, ctx.Err())
			}
			s.xrayWorkerDone = nil
		}
		cancel()
		if err != nil {
			return err
		}
		s.xrayWorkerServer, s.xrayWorkerListener = nil, nil
	}
	listener, err := net.Listen("tcp", target)
	if err != nil {
		return fmt.Errorf("agent: bind Xray worker management API: %w", err)
	}
	server := &http.Server{Handler: s.xrayWorkerHandler(apply, observe), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	s.xrayWorkerListener, s.xrayWorkerServer = listener, server
	go func() { _ = server.Serve(listener) }()
	reconcileContext, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.xrayWorkerCancel, s.xrayWorkerDone = cancel, done
	go s.runXrayWorkerReconciler(reconcileContext, apply, observe, done)
	return nil
}

func (s *Store) stopXrayWorkerAPI(ctx context.Context, deleteData bool) error {
	s.xrayWorkerMu.Lock()
	defer s.xrayWorkerMu.Unlock()
	var stopErr error
	if s.xrayWorkerCancel != nil {
		s.xrayWorkerCancel()
		s.xrayWorkerCancel = nil
	}
	if s.xrayWorkerServer != nil {
		stopErr = s.xrayWorkerServer.Shutdown(ctx)
		s.xrayWorkerServer, s.xrayWorkerListener = nil, nil
	}
	if s.xrayWorkerDone != nil {
		select {
		case <-s.xrayWorkerDone:
		case <-ctx.Done():
			stopErr = errors.Join(stopErr, ctx.Err())
		}
		s.xrayWorkerDone = nil
	}
	if deleteData && stopErr == nil {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM xray_worker_state WHERE id=1`); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(s.dataDir, "xray-worker")); err != nil {
			return err
		}
	}
	return stopErr
}

func (s *Store) runXrayWorkerReconciler(ctx context.Context, apply xrayWorkerApply, observe xrayWorkerObserve, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.reconcileXrayWorkerRuntime(ctx, apply, observe) != nil {
				// Fail closed on the first control-plane error. The persisted
				// desired/applied revisions remain available to explicit recovery.
				return
			}
		}
	}
}

func (s *Store) reconcileXrayWorkerRuntime(ctx context.Context, apply xrayWorkerApply, observe xrayWorkerObserve) error {
	s.xrayWorkerStateMu.Lock()
	defer s.xrayWorkerStateMu.Unlock()
	previous, err := s.loadXrayWorkerState(ctx)
	if err != nil {
		return err
	}
	if previous.AppliedRevision != previous.Revision {
		return errors.New("Xray worker revision requires explicit recovery")
	}
	candidate := previous
	if observe != nil {
		candidate, err = observe(ctx, candidate)
		if err != nil {
			return err
		}
	}
	candidate = reconcileXrayWorkerAccounts(candidate, s.now().UnixMilli())
	changed, err := xrayWorkerConfigChanged(previous, candidate)
	if err != nil {
		return err
	}
	if !changed {
		if xrayWorkerStateEqual(previous, candidate) {
			return nil
		}
		return s.saveXrayWorkerState(ctx, candidate)
	}
	if apply == nil {
		return errors.New("worker runtime unavailable")
	}
	candidate.Revision = previous.Revision + 1
	candidate.AppliedRevision = previous.AppliedRevision
	if err := s.saveXrayWorkerState(ctx, candidate); err != nil {
		return err
	}
	if err := apply(ctx, previous, candidate); err != nil {
		return err
	}
	candidate.AppliedRevision = candidate.Revision
	return s.saveXrayWorkerState(ctx, candidate)
}

func (s *Store) retireXrayWorkerState(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM xray_worker_state WHERE id=1`); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.dataDir, "xray-worker"))
}
