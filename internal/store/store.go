// Package store is instacrawl's archive persistence, built on the crawlkit
// SQLite store (WAL, single-writer, 0600, schema-version stamp). It owns the
// Instagram schema; crawlkit owns the connection/pragma/versioning mechanics.
package store

import (
	"context"
	"database/sql"
	"fmt"

	crawlstore "github.com/openclaw/crawlkit/store"
)

// Store wraps a crawlkit store opened with the instacrawl schema.
type Store struct {
	s *crawlstore.Store
}

// Open opens (creating if absent) the archive DB at path, applies the schema,
// and stamps the schema version. The file is created 0600 by crawlkit.
func Open(ctx context.Context, path string) (*Store, error) {
	s, err := crawlstore.Open(ctx, crawlstore.Options{
		Path:          path,
		Schema:        Schema,
		SchemaVersion: SchemaVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("open archive %s: %w", path, err)
	}
	return &Store{s: s}, nil
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
