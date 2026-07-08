package carvefs

import (
	"context"
	"errors"

	"github.com/ss251/instacrawl/internal/carve"
	"github.com/ss251/instacrawl/internal/reliability"
)

// ErrNotImplemented is returned by the format-specific carve sources that are
// deliberately deferred to Phase 4b. Their on-disk formats (Chromium's
// disk_cache / Cache Storage, LevelDB-backed IndexedDB) are version-specific and
// will NOT be reverse-engineered from a spec in memory — they are built only
// after the owner runs `carve --dry-run` against a copy of their real cache, the
// same live-probe discipline that validated the Phase 1–3 endpoints. Until then
// these sources refuse loudly rather than guess.
var ErrNotImplemented = errors.New(
	"format-specific carve extractor not implemented yet: the Chromium disk_cache / " +
		"IndexedDB on-disk formats are validated against a real cache in Phase 4b, not guessed at; " +
		"use the default walker over a directory of media files, or run `carve --dry-run` on a real " +
		"cache directory to gather the samples 4b needs")

// ChromiumCache is the (deferred) extractor for a Chromium HTTP disk_cache /
// Cache Storage directory — where a logged-in instagram.com web session's media
// response bodies live. Stub until 4b. It accepts roots for constructor symmetry
// with the live sources but stores nothing, since Scan refuses before reading.
type ChromiumCache struct{}

// NewChromiumCache constructs the stub. roots is accepted (and ignored) so the
// command can build any source uniformly from the same --dir list.
func NewChromiumCache(_ []string) *ChromiumCache { return &ChromiumCache{} }

// Name identifies this source.
func (c *ChromiumCache) Name() string { return "chromium" }

// Scan refuses with ErrNotImplemented — the fail-open error path, so a caller
// that selects this source is told exactly why nothing was recovered.
func (c *ChromiumCache) Scan(context.Context, func(carve.Item) error) (reliability.Verdict, error) {
	return reliability.Verdict{}, ErrNotImplemented
}

// IndexedDB is the (deferred) extractor for the Instagram web client's
// IndexedDB (LevelDB) store, which holds structured post/message records. Stub
// until 4b.
type IndexedDB struct{}

// NewIndexedDB constructs the stub. roots is accepted and ignored (see
// NewChromiumCache).
func NewIndexedDB(_ []string) *IndexedDB { return &IndexedDB{} }

// Name identifies this source.
func (i *IndexedDB) Name() string { return "indexeddb" }

// Scan refuses with ErrNotImplemented.
func (i *IndexedDB) Scan(context.Context, func(carve.Item) error) (reliability.Verdict, error) {
	return reliability.Verdict{}, ErrNotImplemented
}

// Compile-time proof that every carvefs source satisfies the seam.
var (
	_ carve.Source = (*Walker)(nil)
	_ carve.Source = (*ChromiumCache)(nil)
	_ carve.Source = (*IndexedDB)(nil)
)
