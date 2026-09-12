package catalog

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	tufconfig "github.com/theupdateframework/go-tuf/v2/metadata/config"
	"github.com/theupdateframework/go-tuf/v2/metadata/updater"
)

// OfficialFetchState is trusted local state, never supplied by a remote source.
// Store it outside the source lifecycle. Metadata may advance independently
// through authenticated checkpoints; acceptance only changes after all checks.
type OfficialFetchState struct {
	Metadata   map[string][]byte
	Acceptance OfficialAcceptance
}

type OfficialFetchResult struct {
	Catalog Catalog
	Target  []byte
	State   OfficialFetchState
	// A verified TUF update may revoke keys even if later target retrieval or
	// executor validation fails. Persist this independently of catalog acceptance.
	Checkpoint *OfficialTrustCheckpoint
}

type OfficialTrustCheckpoint struct {
	Metadata   map[string][]byte
	ObservedAt time.Time
}

type officialTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t officialTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(r.Clone(t.ctx))
}

// FetchOfficial uses an isolated disposable TUF cache. No accepted state is
// mutated on failure. The caller must atomically persist the returned state.
func FetchOfficial(ctx context.Context, origin, channel string, bootstrapRoot []byte, previous OfficialFetchState) (OfficialFetchResult, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	return fetchOfficial(ctx, origin, channel, bootstrapRoot, previous, transport)
}

func fetchOfficial(ctx context.Context, origin, channel string, bootstrapRoot []byte, previous OfficialFetchState, transport http.RoundTripper) (result OfficialFetchResult, resultErr error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !identifierPattern.MatchString(channel) {
		return result, errors.New("catalog: invalid official origin or channel")
	}
	now := time.Now().UTC()
	if now.Before(previous.Acceptance.ObservedAt) {
		return result, errors.New("catalog: clock moved behind accepted metadata")
	}
	root := bootstrapRoot
	if previous.Acceptance.Revision != 0 || len(previous.Metadata) != 0 {
		if previous.Acceptance.Channel != channel {
			return result, errors.New("catalog: accepted channel cannot be replaced")
		}
		root = previous.Metadata["root"]
		if len(root) == 0 {
			return result, errors.New("catalog: accepted trust state is incomplete")
		}
		if previous.Acceptance.Revision != 0 {
			for _, role := range []string{"timestamp", "snapshot", "targets"} {
				if len(previous.Metadata[role]) == 0 {
					return result, errors.New("catalog: accepted trust state is incomplete")
				}
			}
		}
	}
	if len(root) == 0 {
		return result, errors.New("catalog: official trust root is not configured")
	}
	dir, err := os.MkdirTemp("", "vastora-catalog-tuf-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	for _, role := range []string{"timestamp", "snapshot", "targets"} {
		if raw := previous.Metadata[role]; len(raw) > 0 {
			if err := os.WriteFile(filepath.Join(dir, role+".json"), raw, 0600); err != nil {
				return result, err
			}
		}
	}
	cfg, err := tufconfig.New(origin, root)
	if err != nil {
		return result, err
	}
	cfg.LocalMetadataDir, cfg.LocalTargetsDir = dir, dir
	cfg.MaxDelegations = 1 // official catalogs are direct top-level targets
	client := &http.Client{Transport: officialTransport{ctx, transport}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("catalog: official metadata redirects are not permitted")
	}}
	if err := cfg.SetDefaultFetcherHTTPClient(client); err != nil {
		return result, err
	}
	tuf, err := updater.New(cfg)
	if err != nil {
		return result, err
	}
	// go-tuf atomically writes each authenticated metadata role as it advances,
	// including every authorized root rotation. Preserve those writes even when
	// a later step fails; reverting to previous keys would undo revocation.
	defer func() {
		checkpoint := &OfficialTrustCheckpoint{Metadata: make(map[string][]byte), ObservedAt: time.Now().UTC()}
		if checkpoint.ObservedAt.Before(now) {
			checkpoint.ObservedAt = now
		}
		for _, role := range []string{"root", "timestamp", "snapshot", "targets"} {
			raw, err := os.ReadFile(filepath.Join(dir, role+".json"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				result = OfficialFetchResult{}
				resultErr = errors.Join(resultErr, errors.New("catalog: verified metadata could not be preserved"))
				return
			}
			checkpoint.Metadata[role] = raw
		}
		if len(checkpoint.Metadata["root"]) > 0 {
			result.Checkpoint = checkpoint
		}
	}()
	if err := tuf.Refresh(); err != nil {
		return result, err
	}
	target, ok := tuf.GetTopLevelTargets()[channel+".json"]
	if !ok || target.Length <= 0 || target.Length > MaxEnvelopeBytes {
		return result, errors.New("catalog: missing or oversized official target")
	}
	_, raw, err := tuf.DownloadTarget(target, filepath.Join(dir, "catalog.json"), "")
	if err != nil {
		return result, err
	}
	completedAt := time.Now().UTC()
	if completedAt.Before(now) {
		return result, errors.New("catalog: clock moved backward during refresh")
	}
	value, acceptance, err := ValidateOfficialTarget(raw, channel, previous.Acceptance, completedAt)
	if err != nil {
		return result, err
	}
	trusted := tuf.GetTrustedMetadataSet()
	for _, expiry := range []time.Time{trusted.Root.Signed.Expires, trusted.Timestamp.Signed.Expires, trusted.Snapshot.Signed.Expires, trusted.Targets["targets"].Signed.Expires} {
		if expiry.Before(acceptance.ExpiresAt) {
			acceptance.ExpiresAt = expiry
		}
	}
	if !acceptance.ExpiresAt.After(completedAt) {
		return result, errors.New("catalog: trusted metadata expired during refresh")
	}
	metadata := make(map[string][]byte)
	for _, role := range []string{"root", "timestamp", "snapshot", "targets"} {
		metadata[role], err = os.ReadFile(filepath.Join(dir, role+".json"))
		if err != nil {
			return result, err
		}
	}
	return OfficialFetchResult{Catalog: value, Target: raw, State: OfficialFetchState{Metadata: metadata, Acceptance: acceptance}}, nil
}
