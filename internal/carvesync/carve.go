// Package carvesync is the cache-carve orchestrator: it drives one or more
// carve.Source readers over the store's list of bytes-less media_files and
// backfills whatever the local cache can still supply. It is the carve lane's
// equivalent of feedsync — the top of the pure-domain ← store ← orchestrator
// triangle, importing both the domain (carve) and the store.
//
// The whole lane is zero-network: this package imports store, media, and carve,
// but never net/http.
package carvesync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ss251/instacrawl/internal/carve"
	"github.com/ss251/instacrawl/internal/media"
	"github.com/ss251/instacrawl/internal/store"
)

// Deps is everything Run needs. Sources are tried in order; a failing source is
// isolated and the next still runs.
type Deps struct {
	Store   *store.Store
	Blobs   *media.BlobStore
	Sources []carve.Source
	DryRun  bool
}

// SourceReport is one source's outcome, for honest per-source reporting (the
// walker is active; the Chromium/IndexedDB stubs report their refusal here
// rather than silently contributing nothing).
type SourceReport struct {
	Name   string
	State  string // "OK", a reliability state, or "ERROR"
	Reason string // empty on clean OK; a path-free explanation otherwise
	Items  int    // artifacts this source emitted
}

// Summary is the run outcome. Counts are deliberately content-free (no paths, no
// URLs, no basenames — a basename can identify content; DECISIONS.md §5).
type Summary struct {
	Eligible            int // bytes-less media_files with a url to attribute against
	Recovered           int // refs whose bytes were backfilled (would-be, in DryRun)
	StillMissing        int // eligible refs left without bytes
	SkippedUnattributed int // artifacts with no key / matching no eligible ref
	KindMismatch        int // artifacts whose sniffed kind disagreed with the ref
	BlobsNew            int // distinct blobs newly stored this run
	DryRun              bool
	Sources             []SourceReport
}

// Run recovers expired/missing media bytes from local cache. It never fetches:
// every byte comes from a carve.Source reading disk. Returns an error only for a
// context cancellation or a store failure; a source that errors is isolated into
// its SourceReport and the run continues. Kept a free function to mirror
// feedsync.Archive.
func Run(ctx context.Context, d Deps) (Summary, error) {
	sum := Summary{DryRun: d.DryRun}

	refs, err := d.Store.UndownloadedFiles(ctx)
	if err != nil {
		return sum, fmt.Errorf("carve: list undownloaded media: %w", err)
	}
	sum.Eligible = len(refs)
	if len(refs) == 0 {
		// Nothing to attribute against — do not scan any source. This is also the
		// privacy-cheap path: with no work list, no cache file is ever opened.
		return sum, nil
	}

	idx := carve.BuildIndex(refs)
	seen := map[string]bool{}      // hashes stored this run (BlobsNew)
	recovered := map[string]bool{} // ref identity → filled, for StillMissing accounting

	for _, src := range d.Sources {
		rep := SourceReport{Name: src.Name()}
		emit := d.itemHandler(ctx, idx, seen, recovered, &sum, &rep)

		verdict, serr := src.Scan(ctx, emit)
		switch {
		case serr != nil && (errors.Is(serr, context.Canceled) || errors.Is(serr, context.DeadlineExceeded)):
			sum.Sources = append(sum.Sources, rep)
			sum.finalize(recovered)
			return sum, serr
		case serr != nil:
			// Fail-open: log the source's refusal/error, keep going. The message is
			// path-free by construction (see the walker / stub error strings).
			rep.State = "ERROR"
			rep.Reason = serr.Error()
		default:
			rep.State = verdict.State.String()
			rep.Reason = verdict.Reason
		}
		sum.Sources = append(sum.Sources, rep)
	}

	sum.finalize(recovered)
	return sum, nil
}

// itemHandler builds the per-artifact emit closure for one source. The closure
// runs sequentially (WalkDir is single-goroutine, sources run in series), so the
// shared counters need no locking.
func (d Deps) itemHandler(
	ctx context.Context,
	idx map[string][]carve.FileRef,
	seen, recovered map[string]bool,
	sum *Summary,
	rep *SourceReport,
) func(carve.Item) error {
	return func(it carve.Item) error {
		rep.Items++

		key := attributionKey(it)
		if key == "" {
			sum.SkippedUnattributed++
			return nil
		}
		refs := idx[key]
		if len(refs) == 0 {
			// Either unknown content, or content already recovered earlier this run
			// (its key was deleted). Not attributable → never opened past here.
			sum.SkippedUnattributed++
			return nil
		}

		rc, err := it.Open()
		if err != nil {
			//nolint:nilerr // fail-open: a cache file that vanished between walk and open stays missing, not a hard failure
			return nil
		}

		prefix := make([]byte, carve.SniffPrefixLen)
		n, _ := io.ReadFull(rc, prefix)
		prefix = prefix[:n]
		kind := carve.SniffKind(prefix)

		matched := matchKind(refs, kind)
		if len(matched) == 0 {
			// Unrecognized bytes, or a filename collision whose real kind differs
			// from every eligible ref under this key.
			sum.KindMismatch++
			_ = rc.Close()
			return nil
		}

		if d.DryRun {
			// Steps 1–4 only: report what WOULD be recovered, write nothing.
			for range matched {
				sum.Recovered++
			}
			for _, r := range matched {
				recovered[refID(r)] = true
			}
			delete(idx, key)
			_ = rc.Close()
			return nil
		}

		hash, size, _, err := d.Blobs.Put(io.MultiReader(bytes.NewReader(prefix), rc), it.Size)
		_ = rc.Close()
		if err != nil {
			// A byte-store I/O error is fail-open: the ref stays missing, counted
			// at the end; do not abort the whole run for one bad artifact.
			//nolint:nilerr // deliberate fail-open (see comment above)
			return nil
		}
		if !seen[hash] {
			seen[hash] = true
			sum.BlobsNew++
		}
		for _, r := range matched {
			filled, berr := d.Store.BackfillMediaFile(ctx, r, hash, size)
			if berr != nil {
				//nolint:nilerr // fail-open: one ref's backfill error leaves it missing, does not abort the run
				return nil
			}
			if filled {
				sum.Recovered++
			}
			recovered[refID(r)] = true
		}
		// This content is done; a duplicate cache entry for it is now a no-op.
		delete(idx, key)
		return nil
	}
}

// finalize computes StillMissing from the eligible set minus what was recovered.
func (s *Summary) finalize(recovered map[string]bool) {
	s.StillMissing = s.Eligible - len(recovered)
	if s.StillMissing < 0 {
		s.StillMissing = 0
	}
}

// attributionKey derives the match key for an item: its URL when a source knows
// it, else its on-disk filename.
func attributionKey(it carve.Item) string {
	if it.URL != "" {
		return carve.CanonicalKey(it.URL)
	}
	return carve.KeyFromName(it.Name)
}

// matchKind keeps the refs under a key whose stored kind equals the sniffed
// kind. An empty sniff (unrecognized bytes) matches nothing.
func matchKind(refs []carve.FileRef, kind string) []carve.FileRef {
	if kind == "" {
		return nil
	}
	out := refs[:0:0]
	for _, r := range refs {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// refID is the stable identity of a media_file ref, for the recovered set.
func refID(r carve.FileRef) string {
	return r.MediaPK + "\x00" + r.Kind + "\x00" + fmt.Sprint(r.Position)
}
