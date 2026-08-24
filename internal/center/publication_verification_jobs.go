package center

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

const (
	publicationVerificationAttempts = 8
	publicationVerificationTimeout  = 15 * time.Second
)

type publicationVerificationJob struct {
	revision   int64
	generation uint64
	wake       chan struct{}
}

type publicationVerificationTarget struct {
	id       string
	revision int64
}

func defaultPublicationVerificationBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	delay := time.Second << min(attempt-1, 4)
	if delay > 15*time.Second {
		return 15 * time.Second
	}
	return delay
}

// schedulePublicationVerification starts or wakes the single verifier for a
// publication. The monotonically increasing generation makes scheduling at
// the end of an attempt cycle durable: the existing worker either observes it
// and runs another cycle, or removes itself under the same mutex before a new
// worker is installed. Revisions are therefore also verified serially.
func (s *Store) schedulePublicationVerification(id string, revision int64) {
	if strings.TrimSpace(id) == "" || revision <= 0 {
		return
	}
	s.publicationVerificationMu.Lock()
	current := s.publicationVerificationJobs[id]
	if current != nil {
		if revision > current.revision {
			current.revision = revision
		}
		current.generation++
		select {
		case current.wake <- struct{}{}:
		default:
		}
		s.publicationVerificationMu.Unlock()
		return
	}
	job := &publicationVerificationJob{revision: revision, generation: 1, wake: make(chan struct{}, 1)}
	s.publicationVerificationJobs[id] = job
	s.publicationVerificationMu.Unlock()

	if !s.startBackground(func() { s.runPublicationVerification(id, job) }) {
		s.publicationVerificationMu.Lock()
		if s.publicationVerificationJobs[id] == job {
			delete(s.publicationVerificationJobs, id)
		}
		s.publicationVerificationMu.Unlock()
	}
}

func (s *Store) runPublicationVerification(id string, job *publicationVerificationJob) {
	defer func() {
		s.publicationVerificationMu.Lock()
		if s.publicationVerificationJobs[id] == job {
			delete(s.publicationVerificationJobs, id)
		}
		s.publicationVerificationMu.Unlock()
	}()
	for {
		revision, generation, current := s.publicationVerificationJobSnapshot(id, job)
		if !current {
			return
		}
		if s.runPublicationVerificationCycle(id, job, revision) {
			return
		}

		s.publicationVerificationMu.Lock()
		if s.publicationVerificationJobs[id] != job {
			s.publicationVerificationMu.Unlock()
			return
		}
		if job.generation == generation {
			delete(s.publicationVerificationJobs, id)
			s.publicationVerificationMu.Unlock()
			return
		}
		s.publicationVerificationMu.Unlock()
	}
}

// runPublicationVerificationCycle returns true only when the publication or
// Store has become terminal. A normal completion returns to the generation
// check in runPublicationVerification so a concurrent schedule cannot be lost.
func (s *Store) runPublicationVerificationCycle(id string, job *publicationVerificationJob, revision int64) bool {
	lastMessage := "publication health check did not pass"
	terminalStatus := "degraded"
	for attempt := 0; attempt < publicationVerificationAttempts; attempt++ {
		if attempt > 0 {
			delay := defaultPublicationVerificationBackoff(attempt)
			if s.publicationVerificationBackoff != nil {
				delay = s.publicationVerificationBackoff(attempt)
			}
			timer := time.NewTimer(delay)
			select {
			case <-s.backgroundCtx.Done():
				timer.Stop()
				return true
			case <-job.wake:
				timer.Stop()
			case <-timer.C:
			}
		}
		if !s.publicationVerificationJobCurrent(id, job, revision) {
			return false
		}
		verifyCtx, cancel := context.WithTimeout(s.backgroundCtx, publicationVerificationTimeout)
		publication, err := s.verifyPublication(verifyCtx, id, revision)
		cancel()
		if err != nil {
			lastMessage = strings.TrimSpace(err.Error())
			terminalStatus = "failed"
			continue
		}
		terminalStatus = "degraded"
		if publication.Status == "stopped" {
			return true
		}
		if publication.DesiredRevision != revision {
			s.schedulePublicationVerification(id, publication.DesiredRevision)
			return false
		}
		if publication.Status == "ready" {
			return false
		}
		if publication.LastError != "" {
			lastMessage = publication.LastError
		}
		if publication.Status == "failed" {
			terminalStatus = "failed"
		}
	}
	_ = s.finishPublicationVerification(s.backgroundCtx, id, revision, terminalStatus, lastMessage)
	return false
}

func (s *Store) publicationVerificationJobSnapshot(id string, job *publicationVerificationJob) (int64, uint64, bool) {
	s.publicationVerificationMu.Lock()
	defer s.publicationVerificationMu.Unlock()
	if s.publicationVerificationJobs[id] != job {
		return 0, 0, false
	}
	return job.revision, job.generation, true
}

func (s *Store) publicationVerificationJobCurrent(id string, job *publicationVerificationJob, revision int64) bool {
	s.publicationVerificationMu.Lock()
	defer s.publicationVerificationMu.Unlock()
	return s.publicationVerificationJobs[id] == job && job.revision == revision
}

func (s *Store) finishPublicationVerification(ctx context.Context, id string, revision int64, status, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "publication health check did not pass"
	}
	if len(message) > 1024 {
		message = message[:1024]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE publications SET status = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND desired_revision = ? AND status NOT IN ('ready', 'stopped')`, status, message, s.now().UTC().Format(time.RFC3339Nano), id, revision)
	return err
}

func (s *Store) publicationVerificationTargetsForGateway(ctx context.Context, tx *sql.Tx, gatewayID string) ([]publicationVerificationTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, desired_revision FROM publications
		WHERE gateway_node_id = ? AND kind IN ('public_direct', 'public_shared_443') AND status <> 'stopped'`, gatewayID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []publicationVerificationTarget{}
	for rows.Next() {
		var target publicationVerificationTarget
		if err := rows.Scan(&target.id, &target.revision); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (s *Store) resumePublicationVerifications(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT p.id, p.desired_revision FROM publications p
		WHERE p.kind IN ('public_direct', 'public_shared_443', 'cloudflare_tunnel') AND (
			p.status IN ('pending', 'applying', 'degraded') OR
			(p.status = 'failed' AND p.dns_provider <> 'manual' AND ((p.kind = 'cloudflare_tunnel' AND p.dns_record_id = '') OR
				p.applied_revision = p.desired_revision OR
				(p.kind = 'public_direct' AND NOT EXISTS (SELECT 1 FROM routes r WHERE r.publication_id = p.id))
			))
		)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	targets := []publicationVerificationTarget{}
	for rows.Next() {
		var value publicationVerificationTarget
		if err := rows.Scan(&value.id, &value.revision); err != nil {
			return err
		}
		targets = append(targets, value)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, value := range targets {
		s.schedulePublicationVerification(value.id, value.revision)
	}
	return nil
}

// StartPublicationVerifications resumes persisted public entry checks for a
// serving Center. Offline commands such as backup deliberately do not call it,
// so opening the database alone never performs external health checks.
func (s *Store) StartPublicationVerifications(ctx context.Context) error {
	if err := s.resumeSucceededRealityPublications(ctx); err != nil {
		return err
	}
	return s.resumePublicationVerifications(ctx)
}
