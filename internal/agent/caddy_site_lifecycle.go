package agent

import (
	"context"
	"database/sql"
	"errors"
)

// Content refresh is part of the same authorized application operation. It
// reuses the certificate already bound to the instance and never issues one.
func (s *Store) syncSharedDaylight(ctx context.Context, installed AppliedInstallation) error {
	if !daylightKey(installed.AppKey) {
		return nil
	}
	host, err := s.nodeSiteInstallationHost(ctx, installed)
	if err != nil {
		return err
	}
	state, err := s.readCaddySite(ctx, installed.ApplicationID)
	if err == nil && state.Site != nil {
		if state.Site.Hostname != host.site.Hostname {
			return errors.New("agent: changing a website hostname requires an explicit site migration")
		}
		host.site.TLSPort, host.site.Certificate = state.Site.TLSPort, state.Site.Certificate
		return s.updateCaddySite(ctx, installed.ApplicationID, &host.site, func(ctx context.Context) error { return probeCaddySite(ctx, host.site) })
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	journal, err := s.readNodeSiteJournal(ctx, installed.ApplicationID)
	if err == nil {
		if journal.State != "applied" || journal.Task.Hostname != host.site.Hostname {
			return errors.New("agent: inspect the existing website certificate before migration")
		}
		if journal.Runtime == 2 {
			host.site.TLSPort = journal.Task.Port
			certificate, err := nodeSiteCertificate(host.site.Hostname, journal.Task.CertificatePEM, journal.Task.PrivateKeyPEM)
			if err != nil {
				return err
			}
			host.site.Certificate = certificate
			return s.updateCaddySite(ctx, installed.ApplicationID, &host.site, func(ctx context.Context) error { return probeCaddySite(ctx, host.site) })
		}
		_, err := s.executeNodeSiteOnHost(ctx, journal.Task, host)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return s.updateCaddySite(ctx, installed.ApplicationID, &host.site, func(ctx context.Context) error { return probeCaddySite(ctx, host.site) })
}

func (s *Store) removeSharedDaylight(ctx context.Context, applicationID string, deleteData bool) error {
	if _, err := s.readCaddySite(ctx, applicationID); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if err := s.updateCaddySite(ctx, applicationID, nil, nil); err != nil {
		return err
	}
	if !deleteData {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM shared_caddy_sites WHERE application_id=?`, applicationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM node_site_journal WHERE application_id=?`, applicationID); err != nil {
		return err
	}
	return tx.Commit()
}
