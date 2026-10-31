package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/secret"
)

type caddySiteJournal struct {
	ApplicationID string                   `json:"applicationId"`
	State         string                   `json:"state"`
	Site          *CaddySite               `json:"site,omitempty"`
	Previous      *CaddySite               `json:"previous,omitempty"`
	Handover      *daylightHandoverJournal `json:"handover,omitempty"`
}

func (s *Store) ConfigureSharedCaddy(driver *CaddyGatewayDriver) error {
	s.sharedCaddy = driver
	if driver == nil {
		return nil
	}
	driver.Sites = s.SharedCaddySites
	s.reloadSharedCaddy = driver.ReloadSites
	runtime := driver.SiteRuntime
	if runtime != nil {
		driver.SiteRuntime = func(ctx context.Context, desired gateway.DesiredState, sites []CaddySite) error {
			listener, err := s.NodeListenerState(ctx)
			if err != nil && !errors.Is(err, errNoAppliedNodeListenerState) {
				return err
			}
			if err == nil && len(listener.Desired.Listener.Routes) != 0 {
				for _, site := range sites {
					if site.TLSPort == 0 || site.PublicListen == "" {
						continue
					}
					address, port, _ := net.SplitHostPort(site.PublicListen)
					viaReality := false
					for _, route := range listener.Desired.Listener.Routes {
						viaReality = viaReality || route.ManagedReality && route.Hostname == site.Hostname
					}
					if listener.Desired.Listener.Address == address && strconv.Itoa(listener.Desired.Listener.Port) == port && !viaReality && (listener.Desired.Listener.RejectUnmatched || listener.Desired.Listener.CaddyAddress != dockerruntime.CaddyAlias || listener.Desired.Listener.CaddyPort != 443) {
						return errors.New("agent: website public socket has no owned Caddy fallback")
					}
				}
				desired.SharedHTTPS = &listener.Desired.Listener
			}
			return runtime(ctx, desired, sites)
		}
	}
	// This is a read-only base snapshot for the next authorized combined load,
	// not an observation or a startup replay of gateway work.
	state, err := s.GatewayState(context.Background())
	if errors.Is(err, errNoAppliedGatewayState) {
		return nil
	}
	if err != nil {
		return err
	}
	driver.mu.Lock()
	driver.state, driver.certificates = state.Desired, state.Certificates
	driver.mu.Unlock()
	return nil
}

// Lock order matches gateway updates: Store, managed driver, Caddy driver.
// Loading a persisted snapshot here does not apply it to the running service.
func (s *Store) ConfigureManagedCaddy(driver *ManagedGatewayDriver) error {
	if driver == nil || driver.Caddy == nil {
		return errors.New("agent: managed Caddy is unavailable")
	}
	if err := s.ConfigureSharedCaddy(driver.Caddy); err != nil {
		return err
	}
	state, certificates := driver.Caddy.CurrentConfiguration()
	listener, err := s.NodeListenerState(context.Background())
	if err != nil && !errors.Is(err, errNoAppliedNodeListenerState) {
		return err
	}
	driver.mu.Lock()
	driver.state, driver.certificates = state, certificates
	driver.nodeListener = err == nil && len(listener.Desired.Listener.Routes) != 0
	driver.mu.Unlock()
	s.reloadSharedCaddy = driver.ReloadWebsites
	return nil
}

func (s *Store) readCaddySite(ctx context.Context, id string) (caddySiteJournal, error) {
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM shared_caddy_sites WHERE application_id=?`, id).Scan(&sealed); err != nil {
		return caddySiteJournal{}, err
	}
	raw, err := secret.Open(s.key, sealed, []byte("shared-caddy-site:"+id))
	if err != nil {
		return caddySiteJournal{}, err
	}
	var state caddySiteJournal
	if len(raw) > 6<<20 || json.Unmarshal(raw, &state) != nil || state.ApplicationID != id || (state.State != "applied" && state.State != "applying" && state.State != "recovering") {
		return state, errors.New("agent: invalid shared website journal")
	}
	for _, site := range []*CaddySite{state.Site, state.Previous} {
		if site != nil && (site.ApplicationID != id || site.validate() != nil) {
			return state, errors.New("agent: invalid shared website ownership")
		}
	}
	if state.Handover != nil {
		if err := state.Handover.validate(id); err != nil {
			return state, err
		}
	}
	return state, nil
}

func (s *Store) saveCaddySite(ctx context.Context, state caddySiteJournal) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(s.key, raw, []byte("shared-caddy-site:"+state.ApplicationID))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO shared_caddy_sites(application_id,sealed_state) VALUES(?,?) ON CONFLICT(application_id) DO UPDATE SET sealed_state=excluded.sealed_state`, state.ApplicationID, sealed)
	return err
}

func (s *Store) sharedCaddyJournals(ctx context.Context) ([]caddySiteJournal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT application_id FROM shared_caddy_sites ORDER BY application_id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var result []caddySiteJournal
	for _, id := range ids {
		state, err := s.readCaddySite(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, state)
	}
	return result, nil
}

func (s *Store) SharedCaddySites(ctx context.Context) ([]CaddySite, error) {
	states, err := s.sharedCaddyJournals(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]CaddySite, 0, len(states))
	for _, state := range states {
		if state.Site != nil {
			result = append(result, *state.Site)
		}
	}
	return result, nil
}

// updateCaddySite is the complete website transaction. Gateway reconciliation
// uses the same lock, and /load remains owned by the single Caddy driver.
func (s *Store) updateCaddySite(ctx context.Context, id string, candidate *CaddySite, verify func(context.Context) error) error {
	s.gatewayMutationMu.Lock()
	defer s.gatewayMutationMu.Unlock()
	if s.sharedCaddy == nil {
		return errors.New("agent: shared Caddy is not configured")
	}
	if candidate != nil && (candidate.ApplicationID != id || candidate.validate() != nil) {
		return errors.New("agent: invalid website update identity")
	}
	states, err := s.sharedCaddyJournals(ctx)
	if err != nil {
		return err
	}
	for _, state := range states {
		if state.State != "applied" {
			return errors.New("agent: recover the interrupted shared website update first")
		}
	}
	previous, err := s.readCaddySite(ctx, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	old, _ := json.Marshal(previous.Site)
	next, _ := json.Marshal(candidate)
	if string(old) == string(next) {
		if verify != nil {
			return verify(ctx)
		}
		return nil
	}
	state := caddySiteJournal{ApplicationID: id, State: "applying", Site: candidate, Previous: previous.Site, Handover: previous.Handover}
	if err := s.saveCaddySite(ctx, state); err != nil {
		return err
	}
	err = s.reloadSharedCaddy(ctx)
	if err == nil && verify != nil {
		err = verify(ctx)
	}
	if err == nil {
		state.State = "applied"
		err = s.saveCaddySite(ctx, state)
	}
	if err == nil {
		return nil
	}
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	state.Site, state.Previous, state.State = previous.Site, nil, "recovering"
	if recoveryErr := s.saveCaddySite(recovery, state); recoveryErr != nil {
		return errors.Join(err, recoveryErr)
	}
	if recoveryErr := s.reloadSharedCaddy(recovery); recoveryErr != nil {
		return errors.Join(err, recoveryErr)
	}
	state.State = "applied"
	return errors.Join(err, s.saveCaddySite(recovery, state))
}

func (s *Store) restoreSharedCaddySites(ctx context.Context) error {
	s.gatewayMutationMu.Lock()
	defer s.gatewayMutationMu.Unlock()
	states, err := s.sharedCaddyJournals(ctx)
	if err != nil || len(states) == 0 {
		return err
	}
	interrupted := false
	for _, state := range states {
		interrupted = interrupted || state.State != "applied"
	}
	if !interrupted {
		return nil
	} // Caddy resumes its durable config itself.
	if s.sharedCaddy == nil {
		return errors.New("agent: shared website runtime is unavailable during recovery")
	}
	for i := range states {
		if states[i].State == "applying" {
			states[i].Site, states[i].Previous, states[i].State = states[i].Previous, nil, "recovering"
		}
		if states[i].State == "recovering" {
			if err := s.saveCaddySite(ctx, states[i]); err != nil {
				return err
			}
		}
	}
	if err := s.reloadSharedCaddy(ctx); err != nil {
		return err
	}
	for _, state := range states {
		if state.State == "recovering" {
			state.State = "applied"
			if err := s.saveCaddySite(ctx, state); err != nil {
				return err
			}
		}
	}
	return nil
}
