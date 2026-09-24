package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/secret"
)

// Xray counters belong to a process generation, not to the container's name
// or the desired configuration revision. The encrypted journal records every
// accepted high-water mark before a report is sent to Center.
type meridianUsageState struct {
	LedgerID      string                          `json:"ledgerId"`
	Sequence      uint64                          `json:"sequence"`
	Generation    string                          `json:"generation"`
	ActiveUsers   []string                        `json:"activeUsers"`
	AffectedUsers []string                        `json:"affectedUsers"`
	Counters      map[string]meridianUsageCounter `json:"counters"`
}

type meridianUsageCounter struct {
	Raw   int64 `json:"raw"`
	Total int64 `json:"total"`
}

func meridianRuntimeGeneration(containerID, startedAt string) (string, error) {
	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if strings.TrimSpace(containerID) == "" || err != nil || started.IsZero() {
		return "", errors.New("agent: Meridian Xray process generation is unavailable")
	}
	return containerID + "/" + startedAt, nil
}

func parseMeridianRawStats(raw []byte) (map[string]int64, error) {
	if len(raw) == 0 || len(raw) > 4<<20 || !json.Valid(raw) {
		return nil, errors.New("agent: invalid Meridian Xray statistics")
	}
	var payload struct {
		Stats []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"stat"`
	}
	if json.NewDecoder(bytes.NewReader(raw)).Decode(&payload) != nil || len(payload.Stats) > 65536 {
		return nil, errors.New("agent: invalid Meridian Xray statistics")
	}
	values := make(map[string]int64, len(payload.Stats))
	for _, stat := range payload.Stats {
		if meridianCounterUser(stat.Name) == "" || len(stat.Name) > 1024 {
			return nil, errors.New("agent: invalid Meridian Xray counter name")
		}
		if _, exists := values[stat.Name]; exists {
			return nil, errors.New("agent: duplicate Meridian Xray counter")
		}
		valueText := string(stat.Value)
		if len(valueText) > 0 && valueText[0] == '"' {
			if json.Unmarshal(stat.Value, &valueText) != nil {
				return nil, errors.New("agent: invalid Meridian Xray counter")
			}
		}
		value, err := strconv.ParseInt(valueText, 10, 64)
		if err != nil || value < 0 {
			return nil, errors.New("agent: invalid Meridian Xray counter")
		}
		values[stat.Name] = value
	}
	return values, nil
}

func meridianCounterUser(name string) string {
	if !strings.HasPrefix(name, "user>>>") {
		return ""
	}
	for _, suffix := range []string{">>>traffic>>>uplink", ">>>traffic>>>downlink"} {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(strings.TrimPrefix(name, "user>>>"), suffix)
		}
	}
	return ""
}

func validMeridianUsageUsers(users []string) bool {
	if len(users) > 65536 || !slices.IsSorted(users) {
		return false
	}
	for index, user := range users {
		if strings.TrimSpace(user) == "" || len(user) > 512 || index > 0 && users[index-1] == user {
			return false
		}
	}
	return true
}

func mergeMeridianUsageUsers(left, right []string) []string {
	users := append(slices.Clone(left), right...)
	slices.Sort(users)
	return slices.Compact(users)
}

// The hashed Xray config, rather than StatsService, identifies users that had
// access before a process gap even when they had not yet generated a counter.
func meridianActiveUsageUsers(config []byte) ([]string, error) {
	var payload struct {
		Inbounds []struct {
			Settings struct {
				Clients []struct {
					Email string `json:"email"`
				} `json:"clients"`
				Users []struct {
					Email string `json:"email"`
				} `json:"users"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if json.Unmarshal(config, &payload) != nil {
		return nil, errors.New("agent: invalid Meridian usage configuration")
	}
	users := []string{}
	for _, inbound := range payload.Inbounds {
		for _, client := range inbound.Settings.Clients {
			users = append(users, client.Email)
		}
		for _, user := range inbound.Settings.Users {
			users = append(users, user.Email)
		}
	}
	slices.Sort(users)
	users = slices.Compact(users)
	if !validMeridianUsageUsers(users) {
		return nil, errors.New("agent: invalid Meridian active usage users")
	}
	return users, nil
}

func (s *Store) loadMeridianUsageState(ctx context.Context, applicationID string) (meridianUsageState, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM meridian_usage_state WHERE application_id=?`, applicationID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return meridianUsageState{Counters: map[string]meridianUsageCounter{}}, nil
	}
	if err != nil {
		return meridianUsageState{}, err
	}
	encoded, err := secret.Open(s.key, sealed, []byte("agent-meridian-usage:"+applicationID))
	if err != nil {
		return meridianUsageState{}, errors.New("agent: Meridian usage journal does not match the local key")
	}
	var state meridianUsageState
	if json.Unmarshal(encoded, &state) != nil {
		return meridianUsageState{}, errors.New("agent: invalid durable Meridian usage journal")
	}
	id, idErr := hex.DecodeString(state.LedgerID)
	if idErr != nil || len(id) != 16 || hex.EncodeToString(id) != state.LedgerID || state.Sequence == 0 || state.Sequence > math.MaxInt64 || state.Generation == "" || state.Counters == nil || len(state.Counters) > 65536 || !validMeridianUsageUsers(state.ActiveUsers) || !validMeridianUsageUsers(state.AffectedUsers) {
		return meridianUsageState{}, errors.New("agent: invalid durable Meridian usage journal")
	}
	for name, counter := range state.Counters {
		if meridianCounterUser(name) == "" || counter.Raw < 0 || counter.Total < counter.Raw {
			return meridianUsageState{}, errors.New("agent: invalid durable Meridian usage counter")
		}
	}
	return state, nil
}

func (s *Store) saveMeridianUsageState(ctx context.Context, applicationID string, state meridianUsageState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(s.key, encoded, []byte("agent-meridian-usage:"+applicationID))
	if err != nil {
		return fmt.Errorf("agent: encrypt Meridian usage journal: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meridian_usage_state(application_id,sealed_state) VALUES(?,?) ON CONFLICT(application_id) DO UPDATE SET sealed_state=excluded.sealed_state`, applicationID, sealed)
	return err
}

// The caller holds meridianUsageMu across the Xray query and this update.
func (s *Store) accumulateMeridianUsage(ctx context.Context, applicationID, generation string, activeUsers []string, raw []byte) (*meridianruntime.UsageLedgerReport, error) {
	if strings.TrimSpace(applicationID) == "" || generation == "" || !validMeridianUsageUsers(activeUsers) {
		return nil, errors.New("agent: invalid Meridian usage observation identity")
	}
	values, err := parseMeridianRawStats(raw)
	if err != nil {
		return nil, err
	}
	state, err := s.loadMeridianUsageState(ctx, applicationID)
	if err != nil {
		return nil, err
	}
	if state.LedgerID == "" {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return nil, err
		}
		state.LedgerID = hex.EncodeToString(id)
	}
	if state.Generation != "" && state.Generation != generation {
		state.AffectedUsers = mergeMeridianUsageUsers(state.AffectedUsers, state.ActiveUsers)
		state.Generation = generation
		for name, counter := range state.Counters {
			counter.Raw = 0
			state.Counters[name] = counter
		}
	} else if state.Generation == "" {
		state.Generation = generation
	}
	state.ActiveUsers = slices.Clone(activeUsers)
	for name, counter := range state.Counters {
		if _, present := values[name]; !present && counter.Raw > 0 {
			state.AffectedUsers = mergeMeridianUsageUsers(state.AffectedUsers, []string{meridianCounterUser(name)})
		}
	}
	for name, value := range values {
		counter := state.Counters[name]
		if value < counter.Raw {
			state.AffectedUsers = mergeMeridianUsageUsers(state.AffectedUsers, []string{meridianCounterUser(name)})
			continue
		}
		delta := value - counter.Raw
		if delta > math.MaxInt64-counter.Total {
			return nil, errors.New("agent: Meridian usage counter overflow")
		}
		counter.Total += delta
		counter.Raw = value
		state.Counters[name] = counter
	}
	names := make([]string, 0, len(state.Counters))
	for name := range state.Counters {
		names = append(names, name)
	}
	slices.Sort(names)
	stats := make([]struct {
		Name  string `json:"name"`
		Value int64  `json:"value"`
	}, 0, len(names))
	for _, name := range names {
		stats = append(stats, struct {
			Name  string `json:"name"`
			Value int64  `json:"value"`
		}{Name: name, Value: state.Counters[name].Total})
	}
	encoded, err := json.Marshal(struct {
		Stats any `json:"stat"`
	}{Stats: stats})
	if err != nil || len(encoded) > 4<<20 {
		return nil, errors.New("agent: accumulated Meridian statistics exceed the accepted size")
	}
	if state.Sequence >= math.MaxInt64 {
		return nil, errors.New("agent: Meridian usage report sequence overflow")
	}
	state.Sequence++
	if err := s.saveMeridianUsageState(ctx, applicationID, state); err != nil {
		return nil, err
	}
	report := &meridianruntime.UsageLedgerReport{ID: state.LedgerID, Sequence: state.Sequence, Stats: encoded, AffectedUsers: slices.Clone(state.AffectedUsers)}
	if err := report.Validate(); err != nil {
		return nil, err
	}
	return report, nil
}
