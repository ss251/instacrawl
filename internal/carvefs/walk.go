// Package carvefs holds the concrete carve.Source implementations that read
// local disk. Only the Walker is live in Phase 4a; the Chromium disk_cache and
// IndexedDB extractors are honest stubs (see chromium.go / indexeddb.go) until
// they can be validated against the owner's real cache in 4b.
//
// carvefs imports os / io / filepath — but deliberately NOT net/http. The carve
// lane is zero-network by construction; the absence of that import is a
// greppable, review-enforceable invariant (DECISIONS.md §7, Phase 4a).
package carvefs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ss251/instacrawl/internal/carve"
	"github.com/ss251/instacrawl/internal/reliability"
)

// DefaultMaxItems bounds a single Walker scan. It is a runaway guard: a
// mistaken `--dir /` must stop loudly, not walk the whole filesystem. The cap is
// generous because the name filter (only plausible CDN media names are emitted)
// already excludes the overwhelming majority of files without opening them.
const DefaultMaxItems = 250_000

// Walker is the format-agnostic carve source: it walks one or more directories
// and yields every regular file whose on-disk name is a plausible Instagram CDN
// media name (carve.KeyFromName). It does NOT parse any cache container format —
// it works when the client stored media under recognizable CDN basenames (a
// mobile app media cache keyed by URL, or a user-exported media folder). Raw
// Chromium-internal cache names are out of its reach by design; that is the
// deferred ChromiumCache extractor's job.
//
// The name filter is the load-bearing efficiency choice: it decides emit-or-skip
// from the directory entry alone, with no file open, so a huge cache directory
// costs a stat-walk, not N opens. Content sniffing (the authoritative
// media-kind check) happens once, later, in carvesync — only on the small set of
// name-attributed candidates.
type Walker struct {
	roots    []string
	maxItems int
}

// WalkerOption configures a Walker.
type WalkerOption func(*Walker)

// WithMaxItems overrides the runaway cap. Values < 1 are ignored.
func WithMaxItems(n int) WalkerOption {
	return func(w *Walker) {
		if n > 0 {
			w.maxItems = n
		}
	}
}

// NewWalker builds a Walker over roots.
func NewWalker(roots []string, opts ...WalkerOption) *Walker {
	w := &Walker{roots: roots, maxItems: DefaultMaxItems}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Name identifies this source in per-source reporting.
func (w *Walker) Name() string { return "walker" }

// errMaxItems stops a walk once the runaway cap is reached. It is surfaced to
// the caller (loud), but the items already emitted are kept (idempotent).
var errMaxItems = errors.New("carve walker: max items reached")

// Scan walks every root, emitting a carve.Item for each plausible-named regular
// file. Per-entry failures (a file evicted mid-walk, a permission hole) are
// skipped, never fatal — a live browser cache mutates under the walk. Context
// cancellation aborts. Hitting the item cap returns the items emitted so far
// plus a loud error.
func (w *Walker) Scan(ctx context.Context, emit func(carve.Item) error) (reliability.Verdict, error) {
	count := 0

	walkRoot := func(root string) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// Unreadable entry: skip its subtree if a dir, else skip the file.
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if d.IsDir() {
				return nil
			}
			// Only regular files: filepath.WalkDir does not follow symlinks, and
			// a symlink entry reports mode type Symlink here — skip it (and any
			// device/socket/fifo) so carve never chases a link out of the cache.
			if !d.Type().IsRegular() {
				return nil
			}
			name := d.Name()
			if carve.KeyFromName(name) == "" {
				return nil // not a plausible CDN media name — skip without opening
			}
			if count >= w.maxItems {
				return errMaxItems
			}
			info, ierr := d.Info()
			if ierr != nil {
				//nolint:nilerr // fail-open: a file that vanished between walk and stat is skipped, never fatal
				return nil
			}
			p := path
			count++
			return emit(carve.Item{
				Name:       name,
				CapturedAt: info.ModTime(), // client capture time, NOT run time
				Size:       info.Size(),
				Open:       func() (io.ReadCloser, error) { return os.Open(p) },
			})
		})
	}

	for _, root := range w.roots {
		if err := walkRoot(root); err != nil {
			switch {
			case errors.Is(err, errMaxItems):
				// Loud, but partial results (already emitted inline) are valid.
				return reliability.OK(count), fmt.Errorf(
					"carve walker stopped at the %d-item cap — directory too large, narrow the scan", w.maxItems)
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				return reliability.Verdict{}, err
			default:
				// An unexpected walk failure is fail-open: keep what we emitted,
				// surface the error for logging.
				return reliability.OK(count), fmt.Errorf("carve walk: %w", err)
			}
		}
	}
	return reliability.OK(count), nil
}
