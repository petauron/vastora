package meridianruntime

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/petauron/glivela"
)

// ReadPilotReceipt reads the controller's atomic, private node-state snapshot.
// The deployment must mount its directory read-only from the independently
// authenticated Glivela controller; never accept a path supplied by a node.
// Pinning the enrolled certificate prevents another controller node from
// becoming the legacy ledger's source. Desired credentials are not decoded.
func ReadPilotReceipt(path, entryID, certificateID, digest string, revision uint64, now time.Time) (PilotReceipt, error) {
	invalid := errors.New("meridianruntime: scoped controller receipt unavailable or inconsistent")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !glivela.ValidIdentifier(entryID) ||
		!glivela.ValidIdentity(certificateID) || !glivela.ValidIdentity(digest) || revision == 0 {
		return PilotReceipt{}, invalid
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return PilotReceipt{}, invalid
		}
		if parent == string(filepath.Separator) {
			break
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16<<20 {
		return PilotReceipt{}, invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return PilotReceipt{}, invalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return PilotReceipt{}, invalid
	}
	raw, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		return PilotReceipt{}, invalid
	}
	defer clear(raw)
	var nodes []struct {
		ID            string `json:"id"`
		CertificateID string `json:"certificateId"`
		Desired       struct {
			Revision uint64 `json:"revision"`
			Digest   string `json:"configSha256"`
		} `json:"desired"`
		ReportedAt time.Time `json:"reportedAt"`
		Receipt    *struct {
			glivela.AppliedReceipt
			Health  *PilotHealth `json:"entryHealth"`
			Traffic *struct {
				StartedAt time.Time `json:"startedAt"`
				SampledAt time.Time `json:"sampledAt"`
				Counters  map[string]struct {
					Upload   int64 `json:"upload,string"`
					Download int64 `json:"download,string"`
				} `json:"counters"`
			} `json:"traffic"`
		} `json:"receipt"`
	}
	if json.Unmarshal(raw, &nodes) != nil || len(nodes) > 4096 {
		return PilotReceipt{}, invalid
	}
	var result PilotReceipt
	found := false
	for _, node := range nodes {
		if node.ID != entryID && node.CertificateID != certificateID {
			continue
		}
		if found || node.ID != entryID || node.CertificateID != certificateID ||
			node.Desired.Revision != revision || node.Desired.Digest != digest ||
			node.Receipt == nil || node.Receipt.Traffic == nil || !node.Receipt.RuntimeReady ||
			node.Receipt.Revision != revision || node.Receipt.ConfigSHA256 != digest ||
			node.ReportedAt.After(now) || now.Sub(node.ReportedAt) > 30*time.Second {
			return PilotReceipt{}, invalid
		}
		traffic := node.Receipt.Traffic
		if traffic.StartedAt.IsZero() || traffic.SampledAt.Before(traffic.StartedAt) ||
			traffic.SampledAt.After(node.ReportedAt) || now.Sub(traffic.SampledAt) > 30*time.Second ||
			len(traffic.Counters) == 0 || len(traffic.Counters) > 65536 {
			return PilotReceipt{}, invalid
		}
		found = true
		result = PilotReceipt{EntryID: entryID, ConfigSHA256: digest, Ready: true,
			StartedAt: traffic.StartedAt, SampledAt: traffic.SampledAt, Health: node.Receipt.Health}
		for id, counter := range traffic.Counters {
			if !glivela.ValidIdentifier(id) || counter.Upload < 0 || counter.Download < 0 {
				return PilotReceipt{}, invalid
			}
			result.Counters = append(result.Counters, glivela.CounterSnapshot{
				CredentialID: id, UpBytes: counter.Upload, DownBytes: counter.Download})
		}
	}
	if !found {
		return PilotReceipt{}, invalid
	}
	slices.SortFunc(result.Counters, func(a, b glivela.CounterSnapshot) int {
		return strings.Compare(a.CredentialID, b.CredentialID)
	})
	return result, nil
}
