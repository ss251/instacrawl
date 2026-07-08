// Package carve is the pure domain of the cache-carve lane: the zero-network
// source that recovers media bytes Instagram's signed CDN URLs have since
// expired, by reading the client's own local cache instead of the network.
//
// It is deliberately dependency-light — reliability + the standard library, no
// store, no net/http, no os. The concrete disk readers live in internal/carvefs
// (the walker, plus the deferred Chromium/IndexedDB extractors); the store
// backfill and the run loop live in internal/store and internal/carvesync. This
// package owns only the seam (Source), the attribution key (CanonicalKey /
// KeyFromName), and the content sniff — the parts that are pure and 100%
// fixture-testable.
//
// Two reconciliation facts shape the types (see DECISIONS.md §3):
//   - A carved artifact is attributed to a stored media_file by its CDN
//     *filename*, not by a content hash — an expired (403) row has no stored
//     hash to match against. Hence FileRef.OriginalURL and the basename key.
//   - Carve recovers *bytes*, never metadata richness. An Item carries no
//     caption/type/owner; it only fills an empty byte slot. So there is no
//     "carve media row" — only a byte backfill.
package carve

import (
	"context"
	"io"
	"time"

	"github.com/ss251/instacrawl/internal/reliability"
)

// Item is one candidate media artifact recovered from local disk. A Source
// emits Items; carvesync attributes each to a bytes-less media_file (by URL or
// filename), sniffs its bytes to confirm the media kind, and streams it into
// the content-addressed blob store.
type Item struct {
	// URL is the full resource URL when the source knows it (a future extractor
	// that reads cache index metadata), else "". When set it is the strongest
	// attribution key.
	URL string
	// Name is the cache-local filename hint — for the walker, the basename on
	// disk. Used for attribution when URL is empty.
	Name string
	// MediaPK is the stable media id when the source knows it, else "". Reserved
	// for a future extractor that reads structured cache records; the walker
	// never sets it.
	MediaPK string
	// Kind is "image" or "video" when the source already knows it, else "" — in
	// which case carvesync sniffs the bytes centrally.
	Kind string
	// CapturedAt is when the CLIENT captured these bytes (file mtime / cache
	// entry time), NOT when this run read them. It becomes the row's recency, so
	// a stale cache cannot out-rank a fresher network fetch of the same media.
	CapturedAt time.Time
	// Size is the artifact's byte size when known (lets Put verify the stream),
	// or -1 when unknown.
	Size int64
	// Open yields exactly the media bytes. A closure, not a path, so a future
	// extractor can hand back an io.SectionReader into a container file with no
	// temp-file staging and no change to this seam.
	Open func() (io.ReadCloser, error)
}

// FileRef is one bytes-less media_file row eligible for carve backfill: a media
// whose bytes never landed (expired 403, or a transport error). OriginalURL is
// the CDN URL whose basename is the attribution key.
type FileRef struct {
	MediaPK     string
	Kind        string
	Position    int
	OriginalURL string
}

// Source is one local-cache reader. Scan streams candidate Items to emit and
// returns a reliability.Verdict describing the read, reusing the four-state gate
// vocabulary rather than a parallel error model:
//
//   - OK(n): the source read cleanly and emitted n items.
//   - Fail(StateSchemaDrift, reason): the source recognized its target format
//     but could not parse it — the on-disk format changed. Isolated per-source,
//     like a drifted feed resource; other sources still run.
//
// A returned error is the GateError analog: OUR bug or an I/O failure reading
// the cache. It is fail-open — logged and isolated — never a drift verdict.
// A carve Source never constructs StateAuthCheckpoint or StateRateLimited:
// there is no network and no auth in this lane.
type Source interface {
	Name() string
	Scan(ctx context.Context, emit func(Item) error) (reliability.Verdict, error)
}
