package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ss251/instacrawl/internal/appmeta"
	"github.com/ss251/instacrawl/internal/carve"
	"github.com/ss251/instacrawl/internal/carvefs"
	"github.com/ss251/instacrawl/internal/carvesync"
	"github.com/ss251/instacrawl/internal/media"
	"github.com/ss251/instacrawl/internal/store"
)

// carveRunFn is the injectable orchestrator seam (tests point it at a fake).
var carveRunFn = carvesync.Run

// multiFlag collects a repeatable string flag (e.g. --dir a --dir b).
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ", ") }
func (m *multiFlag) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("empty --dir")
	}
	*m = append(*m, v)
	return nil
}

func cmdCarve(args []string) error {
	fs := newAuthFlags("carve")
	var dirs multiFlag
	fs.Var(&dirs, "dir", "local cache directory to scan for recoverable media (repeatable)")
	dryRun := fs.Bool("dry-run", false, "report what would be recovered without writing anything")
	format := fs.String("format", "walker", "carve source: walker|chromium|indexeddb")
	if err := fs.Parse(args); err != nil {
		return usageError{msg: err.Error()}
	}
	if fs.NArg() > 0 {
		return usageError{msg: fmt.Sprintf("carve takes no positional arguments, got %v", fs.Args())}
	}
	if len(dirs) == 0 {
		return usageError{msg: "carve needs at least one --dir (a local browser/app cache directory to scan)"}
	}

	src, err := carveSource(*format, dirs)
	if err != nil {
		return err
	}

	bs := media.NewBlobStore(appmeta.MediaDir())

	// Never scan the archive's own media store: a --dir that overlaps it (either
	// direction) would let carve re-ingest already-archived bytes. Checked before
	// opening the archive so a bad invocation fails without creating any files.
	for _, d := range dirs {
		if pathsOverlap(d, bs.Dir()) {
			return usageError{msg: fmt.Sprintf(
				"--dir %q overlaps the archive's own media store (%s); scan a browser/app cache, not the archive", d, bs.Dir())}
		}
	}

	ctx := context.Background()
	st, err := store.Open(ctx, appmeta.DatabasePath())
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = st.Close() }()

	sum, err := carveRunFn(ctx, carvesync.Deps{
		Store: st, Blobs: bs, Sources: []carve.Source{src}, DryRun: *dryRun,
	})
	if err != nil {
		return fmt.Errorf("carve: %w", err)
	}

	printCarveSummary(sum)
	return nil
}

// carveSource builds the chosen carve source. The walker is the live, default
// source; chromium/indexeddb are honest stubs until Phase 4b.
func carveSource(format string, dirs []string) (carve.Source, error) {
	switch strings.TrimSpace(format) {
	case "", "walker":
		return carvefs.NewWalker(dirs), nil
	case "chromium":
		return carvefs.NewChromiumCache(dirs), nil
	case "indexeddb":
		return carvefs.NewIndexedDB(dirs), nil
	default:
		return nil, usageError{msg: fmt.Sprintf("unknown --format %q (want walker|chromium|indexeddb)", format)}
	}
}

func printCarveSummary(sum carvesync.Summary) {
	prefix := ""
	if sum.DryRun {
		prefix = "[dry-run] "
	}
	fmt.Printf("%sCarve — %d eligible, %d recovered, %d still missing (%d unattributed, %d kind-mismatch); %d new blob(s).\n",
		prefix, sum.Eligible, sum.Recovered, sum.StillMissing, sum.SkippedUnattributed, sum.KindMismatch, sum.BlobsNew)
	for _, s := range sum.Sources {
		line := fmt.Sprintf("  %s: %s", s.Name, s.State)
		if s.Reason != "" {
			line += " — " + s.Reason
		}
		fmt.Println(line)
	}
}

// pathsOverlap reports whether a and b are the same directory or one contains
// the other, after resolving to absolute (and, where they exist, symlink-free)
// paths. Used to keep carve from scanning the archive's own blob store.
func pathsOverlap(a, b string) bool {
	aa := resolvePath(a)
	bb := resolvePath(b)
	return isAncestorOrEqual(aa, bb) || isAncestorOrEqual(bb, aa)
}

func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return abs
}

func isAncestorOrEqual(anc, desc string) bool {
	rel, err := filepath.Rel(anc, desc)
	if err != nil {
		return false // different volumes → no overlap
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
