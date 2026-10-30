package center

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/petauron/vastora/internal/secret"
)

// Only lock files live on disk. Account keys, certificates and renewal metadata
// share Center's encrypted database and its existing consistent backup boundary.
type nodeSiteStorage struct{ store *Store }

var _ certmagic.Storage = nodeSiteStorage{}

func (s nodeSiteStorage) Lock(ctx context.Context, name string) error {
	return (&certmagic.FileStorage{Path: filepath.Join(s.store.dataDir, "node-site-certmagic-locks")}).Lock(ctx, name)
}
func (s nodeSiteStorage) Unlock(ctx context.Context, name string) error {
	return (&certmagic.FileStorage{Path: filepath.Join(s.store.dataDir, "node-site-certmagic-locks")}).Unlock(ctx, name)
}
func (s nodeSiteStorage) Store(ctx context.Context, key string, value []byte) error {
	sealed, err := secret.Seal(s.store.key, value, []byte("node-site-certmagic:"+key))
	if err != nil {
		return err
	}
	_, err = s.store.db.ExecContext(ctx, `INSERT INTO node_site_certmagic_storage(key,sealed,modified_at,size) VALUES(?,?,?,?) ON CONFLICT(key) DO UPDATE SET sealed=excluded.sealed,modified_at=excluded.modified_at,size=excluded.size`, key, sealed, s.store.now().UTC().Format(time.RFC3339Nano), len(value))
	return err
}
func (s nodeSiteStorage) Load(ctx context.Context, key string) ([]byte, error) {
	var sealed []byte
	err := s.store.db.QueryRowContext(ctx, `SELECT sealed FROM node_site_certmagic_storage WHERE key=?`, key).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fs.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	return secret.Open(s.store.key, sealed, []byte("node-site-certmagic:"+key))
}
func (s nodeSiteStorage) Delete(ctx context.Context, key string) error {
	prefix := strings.TrimSuffix(key, "/") + "/"
	if key == "" {
		prefix = ""
	}
	_, err := s.store.db.ExecContext(ctx, `DELETE FROM node_site_certmagic_storage WHERE key=? OR substr(key,1,?)=?`, key, len(prefix), prefix)
	return err
}
func (s nodeSiteStorage) Exists(ctx context.Context, key string) bool {
	_, err := s.Stat(ctx, key)
	return err == nil
}
func (s nodeSiteStorage) List(ctx context.Context, prefix string, recursive bool) ([]string, error) {
	prefix = strings.Trim(prefix, "/")
	base := ""
	if prefix != "" {
		base = prefix + "/"
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT key FROM node_site_certmagic_storage WHERE substr(key,1,?)=? ORDER BY key`, len(base), base)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		remaining := strings.TrimPrefix(key, base)
		parts := strings.Split(remaining, "/")
		path := prefix
		for _, part := range parts {
			if path != "" {
				path += "/"
			}
			path += part
			seen[path] = true
			if !recursive {
				break
			}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(seen) == 0 {
		return nil, fs.ErrNotExist
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}
func (s nodeSiteStorage) Stat(ctx context.Context, key string) (certmagic.KeyInfo, error) {
	prefix := strings.TrimSuffix(key, "/") + "/"
	if key == "" {
		prefix = ""
	}
	var actual, modified string
	var size int64
	err := s.store.db.QueryRowContext(ctx, `SELECT key,modified_at,size FROM node_site_certmagic_storage WHERE key=? OR substr(key,1,?)=? ORDER BY CASE WHEN key=? THEN 0 ELSE 1 END LIMIT 1`, key, len(prefix), prefix, key).Scan(&actual, &modified, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return certmagic.KeyInfo{}, fs.ErrNotExist
	}
	if err != nil {
		return certmagic.KeyInfo{}, err
	}
	at, err := time.Parse(time.RFC3339Nano, modified)
	if err != nil {
		return certmagic.KeyInfo{}, err
	}
	return certmagic.KeyInfo{Key: key, Modified: at, Size: size, IsTerminal: actual == key}, nil
}
