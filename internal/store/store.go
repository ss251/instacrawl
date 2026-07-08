// Package store is instacrawl's archive persistence, built on the crawlkit
// SQLite store (WAL, single-writer, 0600, schema-version stamp). It owns the
// Instagram schema; crawlkit owns the connection/pragma/versioning mechanics.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	crawlstore "github.com/openclaw/crawlkit/store"
)

// searchSchemaVersion is the schema version at which the FTS5 search indexes
// were introduced. Opening any older archive rebuilds the indexes once (see
// Open), because the new virtual tables are created empty over already-populated
// base tables and would otherwise return nothing until each row is next written.
const searchSchemaVersion = 4

// Store wraps a crawlkit store opened with the instacrawl schema.
type Store struct {
	s *crawlstore.Store
}

// Open opens (creating if absent) the archive DB at path, applies the schema,
// and stamps the schema version. The file is created 0600 by crawlkit. When it
// detects an upgrade from a pre-search schema, it backfills the FTS indexes once.
func Open(ctx context.Context, path string) (*Store, error) {
	// Read the stamped version BEFORE crawlkit applies the (additive) new DDL, so
	// we can tell an upgrade from a fresh archive. A fresh DB reads 0 and needs no
	// rebuild — its triggers populate the index live as rows are written.
	prevVersion := preexistingSchemaVersion(ctx, path)

	s, err := crawlstore.Open(ctx, crawlstore.Options{
		Path:          path,
		Schema:        Schema,
		SchemaVersion: SchemaVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("open archive %s: %w", path, err)
	}
	st := &Store{s: s}

	if prevVersion > 0 && prevVersion < searchSchemaVersion {
		if err := st.ReindexSearch(ctx); err != nil {
			_ = st.Close()
			return nil, fmt.Errorf("backfill search index on upgrade: %w", err)
		}
	}
	return st, nil
}

// OpenReadOnly opens an EXISTING archive read-only: it cannot create the file,
// apply DDL, or migrate. Used by status reporting so a read never mutates the
// archive or races a writer into a schema change.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	s, err := crawlstore.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("open archive read-only %s: %w", path, err)
	}
	return &Store{s: s}, nil
}

// preexistingSchemaVersion reads the stamped schema version of an existing archive
// without creating or migrating it. A missing file (fresh archive) or any read
// error returns 0 — "no rebuild needed", the safe default.
func preexistingSchemaVersion(ctx context.Context, path string) int {
	if _, err := os.Stat(path); err != nil {
		return 0
	}
	ro, err := crawlstore.OpenReadOnly(ctx, path)
	if err != nil {
		return 0
	}
	defer func() { _ = ro.Close() }()
	v, err := ro.SchemaVersion(ctx)
	if err != nil {
		return 0
	}
	return v
}

// Close closes the underlying store.
func (st *Store) Close() error {
	if st == nil || st.s == nil {
		return nil
	}
	return st.s.Close()
}

// DB exposes the underlying *sql.DB — the seam Phase 2 hands to
// crawlkit's state.NewCursor(db) for pagination cursors.
func (st *Store) DB() *sql.DB { return st.s.DB() }

// MaxFetchedAt returns the most recent fetched_at across DMs and media (the
// archive's effective last-sync time), or "" when the archive is empty. Because
// fetched_at is RFC3339 UTC text, the lexical max is the chronological max.
func (st *Store) MaxFetchedAt(ctx context.Context) (string, error) {
	var v sql.NullString
	err := st.s.DB().QueryRowContext(ctx, `
select max(f) from (
  select max(fetched_at) as f from dm_message
  union all
  select max(fetched_at) from media
)`).Scan(&v)
	if err != nil {
		return "", fmt.Errorf("max fetched_at: %w", err)
	}
	return v.String, nil
}
