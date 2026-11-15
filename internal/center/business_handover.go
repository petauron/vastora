package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type businessHandoverProof struct {
	Version           int    `json:"version"`
	SnapshotID        string `json:"snapshotId"`
	SourceSHA256      string `json:"sourceSha256"`
	CaptureSHA256     string `json:"captureSha256"`
	DestinationSHA256 string `json:"destinationSha256"`
	PreviousWriterID  string `json:"previousWriterId"`
	CapturedAt        string `json:"capturedAt"`
}

// A private receipt is emitted only by the offline adopter after authenticating
// stopped-writer data and committing both owned business and existing nodes.
func (s *Store) inspectBusinessHandover(ctx context.Context) (*businessHandoverProof, error) {
	p := filepath.Join(s.dataDir, "glivela-business-handover.json")
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	invalid := errors.New("center: invalid private business handover receipt")
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return nil, invalid
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var proof businessHandoverProof
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var extra any
	hash := func(s string) bool {
		b, e := hex.DecodeString(s)
		return e == nil && len(b) == 32 && s == strings.ToLower(s)
	}
	if dec.Decode(&proof) != nil || dec.Decode(&extra) != io.EOF || proof.Version != 1 || proof.SnapshotID == "" || len(proof.SnapshotID) > 128 || !hash(proof.SourceSHA256) || !hash(proof.CaptureSHA256) || !hash(proof.DestinationSHA256) || !hash(proof.PreviousWriterID) {
		return nil, invalid
	}
	if _, err := time.Parse(time.RFC3339Nano, proof.CapturedAt); err != nil {
		return nil, invalid
	}
	standalone := true
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(filepath.Join(s.dataDir, "center.db") + suffix); !errors.Is(err, os.ErrNotExist) {
			standalone = false
		}
	}
	f, err := os.Open(filepath.Join(s.dataDir, "center.db"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (1<<30)+1))
	if err != nil || n > 1<<30 {
		return nil, errors.New("center: business handover source differs from final capture")
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='retired_proxy_business')").Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		var stored []byte
		err := s.db.QueryRowContext(ctx, "SELECT proof_json FROM retired_proxy_business WHERE id=1").Scan(&stored)
		if err == nil {
			var old businessHandoverProof
			if json.Unmarshal(stored, &old) != nil || old != proof {
				return nil, invalid
			}
			return &proof, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}

	if !standalone || hex.EncodeToString(h.Sum(nil)) != proof.SourceSHA256 {
		return nil, errors.New("center: business handover source differs or is not standalone")
	}
	return &proof, nil
}

func (s *Store) applyBusinessHandover(ctx context.Context, proof *businessHandoverProof) error {
	if proof == nil {
		return nil
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO retired_proxy_business(id,proof_json) VALUES(1,?) ON CONFLICT(id) DO NOTHING", raw); err != nil {
		return err
	}
	var stored []byte
	if err := tx.QueryRowContext(ctx, "SELECT proof_json FROM retired_proxy_business WHERE id=1").Scan(&stored); err != nil {
		return err
	}
	var old businessHandoverProof
	if json.Unmarshal(stored, &old) != nil || old != *proof {
		return errors.New("center: recorded business handover differs")
	}
	return tx.Commit()
}

func (s *Store) RequireBusinessHandover(ctx context.Context) error {
	var needed, retired bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM meridian_accounts) OR EXISTS(SELECT 1 FROM meridian_endpoints),EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1)").Scan(&needed, &retired); err != nil {
		return err
	}
	if needed && !retired {
		return errors.New("center: complete offline Glivela business handover before serving this database")
	}
	return nil
}
