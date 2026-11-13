package agent

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"reflect"
	"strconv"
	"time"

	"github.com/petauron/vastora/internal/nodesite"
)

// A migration companion is a second hostname on the same owned content and
// listeners. Its synthetic configuration identity never becomes an application,
// Unix account, service or independent certificate authority.
func (s *Store) migrationCompanion(ctx context.Context, primary *CaddySite) (*CaddySite, error) {
	j, err := s.readNodeSiteJournal(ctx, primary.ApplicationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if j.Task.Action != "prepare-migration" || j.State != "prepared" {
		return nil, nil
	}
	var old, next CaddySite
	if json.Unmarshal(j.Previous, &old) != nil || json.Unmarshal(j.Candidate, &next) != nil || old.ApplicationID != primary.ApplicationID || next.ApplicationID != primary.ApplicationID || old.Hostname != j.Task.PreviousHostname || next.Hostname != j.Task.Hostname || old.validate() != nil || next.validate() != nil {
		return nil, errors.New("agent: invalid prepared website migration ownership")
	}
	companion := next
	if primary.Hostname == next.Hostname {
		companion = old
	} else if primary.Hostname != old.Hostname {
		return nil, errors.New("agent: installed website escaped its prepared migration")
	}
	companion.ApplicationID = primary.ApplicationID + "-migration"
	companion.HTML, companion.CaddyPath, companion.CaddySHA256 = primary.HTML, primary.CaddyPath, primary.CaddySHA256
	return &companion, nil
}

func (s *Store) executeWebsiteMigration(ctx context.Context, task nodesite.Task, host nodeSiteHost) (nodesite.Result, error) {
	s.gatewayMutationMu.Lock()
	defer s.gatewayMutationMu.Unlock()
	result := nodesite.Result{Action: task.Action, Revision: task.Revision, Hostname: task.Hostname}
	if s.sharedCaddy == nil || s.reloadSharedCaddy == nil {
		return result, errors.New("agent: managed shared Caddy is required before website migration")
	}
	state, err := s.readCaddySite(ctx, task.ApplicationID)
	if err != nil || state.Site == nil || state.State != "applied" {
		return result, errors.New("agent: verify the installed shared website before migration")
	}
	j, err := s.readNodeSiteJournal(ctx, task.ApplicationID)
	if err != nil {
		return result, err
	}
	if j.CompletedMigration != nil && reflect.DeepEqual(*j.CompletedMigration, task) {
		expected := task.Hostname
		if task.Action == "cancel-migration" {
			expected = task.PreviousHostname
		}
		if host.site.Hostname != expected || state.Site.Hostname != expected {
			return result, errors.New("agent: completed website migration binding changed")
		}
		if err = s.reloadSharedCaddy(ctx); err != nil {
			return result, err
		}
		if err = probeCaddySite(ctx, *state.Site); err != nil {
			return result, err
		}
		result.State, result.Fingerprint, result.NotAfter = "applied", j.Fingerprint, j.NotAfter
		return result, nil
	}
	if task.Action == "cancel-migration" && j.Task.Action == "apply" && j.Task.Hostname == task.PreviousHostname && j.Task.Revision < task.Revision && host.site.Hostname == task.PreviousHostname && state.Site.Hostname == task.PreviousHostname {
		if err = probeCaddySite(ctx, *state.Site); err != nil {
			return result, err
		}
		j.Task.Revision = task.Revision
		completed := task
		j.CompletedMigration = &completed
		if err = s.saveNodeSiteJournal(ctx, j); err != nil {
			return result, err
		}
		result.State, result.Fingerprint, result.NotAfter = "applied", j.Fingerprint, j.NotAfter
		return result, nil
	}
	if task.Action == "prepare-migration" {
		if j.Task.Revision == task.Revision && j.Task.Action == task.Action && j.State == "prepared" && reflect.DeepEqual(j.Task, task) {
			var candidate CaddySite
			if json.Unmarshal(j.Candidate, &candidate) != nil {
				return result, errors.New("agent: invalid migration candidate")
			}
			if err = s.reloadSharedCaddy(ctx); err != nil {
				return result, err
			}
			if err = probeCaddySite(ctx, candidate); err != nil {
				return result, err
			}
			result.State, result.Fingerprint, result.NotAfter = "prepared", j.Fingerprint, j.NotAfter
			return result, nil
		}
		if host.site.Hostname != task.PreviousHostname || state.Site.Hostname != task.PreviousHostname || j.State != "applied" || j.Task.Revision >= task.Revision || j.Task.Action != "apply" {
			return result, errors.New("agent: website migration source changed")
		}
		certificate, err := nodeSiteCertificate(task.Hostname, task.CertificatePEM, task.PrivateKeyPEM)
		if err != nil {
			return result, err
		}
		candidate := *state.Site
		candidate.Hostname, candidate.TLSPort, candidate.Certificate = task.Hostname, task.Port, certificate
		if candidate.PublicListen == "" {
			desired, _ := s.sharedCaddy.CurrentConfiguration()
			for _, listener := range desired.Listeners {
				if listener.Kind == "public" {
					candidate.PublicListen = net.JoinHostPort(listener.Address, strconv.Itoa(listener.HTTPSPort))
				}
			}
		}
		if candidate.validate() != nil {
			return result, errors.New("agent: invalid prepared website binding")
		}
		previous, err := json.Marshal(j)
		if err != nil {
			return result, err
		}
		oldSite, _ := json.Marshal(state.Site)
		newSite, _ := json.Marshal(candidate)
		block, _ := pem.Decode([]byte(task.CertificatePEM))
		if block == nil {
			return result, errors.New("agent: missing migration certificate leaf")
		}
		parsed, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return result, err
		}
		next := nodeSiteJournal{Runtime: 2, Task: task, State: "prepared", Previous: oldSite, Candidate: newSite, MigrationPrevious: previous, Fingerprint: nodesite.Fingerprint(parsed.Raw), NotAfter: parsed.NotAfter.UTC().Format(time.RFC3339Nano)}
		if err = s.saveNodeSiteJournal(ctx, next); err != nil {
			return result, err
		}
		err = s.reloadSharedCaddy(ctx)
		if err == nil {
			err = probeCaddySite(ctx, candidate)
		}
		if err == nil {
			err = probeCaddySite(ctx, *state.Site)
		}
		if err != nil {
			recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			restore := s.saveNodeSiteJournal(recovery, j)
			if restore == nil {
				restore = s.reloadSharedCaddy(recovery)
			}
			return result, errors.Join(err, restore)
		}
		result.State, result.Fingerprint, result.NotAfter = "prepared", next.Fingerprint, next.NotAfter
		return result, nil
	}
	if j.Task.Action != "prepare-migration" || j.State != "prepared" || j.Task.Revision != task.Revision || j.Task.Hostname != task.Hostname || j.Task.PreviousHostname != task.PreviousHostname {
		return result, errors.New("agent: website migration revision is not prepared")
	}
	var candidate CaddySite
	if json.Unmarshal(j.Candidate, &candidate) != nil {
		return result, errors.New("agent: website migration candidate is unavailable")
	}
	if task.Action == "inspect" {
		if err = probeCaddySite(ctx, candidate); err != nil {
			return result, err
		}
		result.State, result.Fingerprint, result.NotAfter = "applied", j.Fingerprint, j.NotAfter
		return result, nil
	}
	if task.Action == "accept-migration" {
		if host.site.Hostname != task.Hostname || state.Site.Hostname != task.Hostname || state.Site.Certificate.Hostname != task.Hostname {
			return result, errors.New("agent: configure and verify the new website before acceptance")
		}
		if err = probeCaddySite(ctx, *state.Site); err != nil {
			return result, err
		}
		next := j
		next.Task.Action, next.Task.PreviousHostname, next.State = "apply", "", "applied"
		next.MigrationPrevious = nil
		next.Previous, _ = json.Marshal(state.Site)
		completed := task
		next.CompletedMigration = &completed
		if err = s.saveNodeSiteJournal(ctx, next); err != nil {
			return result, err
		}
		if err = s.reloadSharedCaddy(ctx); err != nil {
			return result, errors.Join(err, s.restoreWebsiteMigration(ctx, j))
		}
		result.State, result.Fingerprint, result.NotAfter = "applied", j.Fingerprint, j.NotAfter
		return result, nil
	}
	if task.Action == "cancel-migration" {
		if host.site.Hostname != task.PreviousHostname || state.Site.Hostname != task.PreviousHostname {
			return result, errors.New("agent: restore the original installed hostname before cancelling migration")
		}
		var previous nodeSiteJournal
		if json.Unmarshal(j.MigrationPrevious, &previous) != nil || previous.Task.ApplicationID != task.ApplicationID || previous.Task.Hostname != task.PreviousHostname || previous.Task.Validate() != nil {
			return result, errors.New("agent: original website certificate recovery is unavailable")
		}
		previous.Task.Revision = task.Revision
		completed := task
		previous.CompletedMigration = &completed
		if err = s.saveNodeSiteJournal(ctx, previous); err != nil {
			return result, err
		}
		if err = s.reloadSharedCaddy(ctx); err != nil {
			return result, errors.Join(err, s.restoreWebsiteMigration(ctx, j))
		}
		result.State, result.Fingerprint, result.NotAfter = "applied", previous.Fingerprint, previous.NotAfter
		return result, nil
	}
	return result, errors.New("agent: unsupported website migration action")
}

func (s *Store) restoreWebsiteMigration(ctx context.Context, journal nodeSiteJournal) error {
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.saveNodeSiteJournal(recovery, journal); err != nil {
		return err
	}
	return s.reloadSharedCaddy(recovery)
}
