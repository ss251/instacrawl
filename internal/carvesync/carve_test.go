package carvesync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/ss251/instacrawl/internal/carve"
	"github.com/ss251/instacrawl/internal/carvefs"
	"github.com/ss251/instacrawl/internal/feed"
	"github.com/ss251/instacrawl/internal/media"
	"github.com/ss251/instacrawl/internal/reliability"
	"github.com/ss251/instacrawl/internal/store"
)

// Realistic IG CDN basenames (pass the attribution guard).
const (
	nameA = "111111111_17862345678901234_n.jpg"
	nameB = "222222222_17862345678901299_n.jpg"
	nameZ = "999999999_17862345678901277_n.jpg"
)

func jpegBytes(payload string) []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte(payload)...)
}
func mp4Bytes(payload string) []byte {
	return append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, []byte(payload)...)
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func tempStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func blobs(t *testing.T) *media.BlobStore { return media.NewBlobStore(t.TempDir()) }

func cacheDir(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// seed commits a media whose files are given as (kind, url, downloaded, hash).
func seed(t *testing.T, st *store.Store, pk string, files ...feed.MediaFile) {
	t.Helper()
	page := &feed.FeedPage{Media: []feed.Media{{PK: pk, Code: "code-" + pk, MediaType: 1, Known: true, TakenAtUS: 1, Files: files}}}
	if err := st.CommitFeedPage(context.Background(), page, feed.FeedCursor{}, store.FeedEntityOwn, "", store.FeedKindOwn); err != nil {
		t.Fatal(err)
	}
}

func imageURL(name string) string {
	return "https://scontent.cdninstagram.com/v/t51/" + name + "?oe=abc"
}

func walker(dir string) carve.Source { return carvefs.NewWalker([]string{dir}) }

// --- Clause 1: an expired (403) media_file is recovered from cache. ---

func TestCarveGateRecoversExpired403(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	bs := blobs(t)
	seed(t, st, "m1", feed.MediaFile{Kind: "image", Position: 0, URL: imageURL(nameA), Missing: true})

	pix := jpegBytes("PIXELS-A")
	dir := cacheDir(t, map[string][]byte{nameA: pix})

	sum, err := Run(ctx, Deps{Store: st, Blobs: bs, Sources: []carve.Source{walker(dir)}})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Eligible != 1 || sum.Recovered != 1 || sum.StillMissing != 0 || sum.BlobsNew != 1 {
		t.Fatalf("expected 1 recovered from cache: %+v", sum)
	}

	// media_file now carries bytes; metadata untouched.
	var dl, miss int
	var bh, code string
	if err := st.DB().QueryRowContext(ctx,
		`select downloaded, missing, coalesce(blob_hash,'') from media_file where media_pk='m1' and position=0`).Scan(&dl, &miss, &bh); err != nil {
		t.Fatal(err)
	}
	want := sha(pix)
	if dl != 1 || miss != 0 || bh != want {
		t.Fatalf("row not recovered: downloaded=%d missing=%d blob_hash=%q want %q", dl, miss, bh, want)
	}
	if err := st.DB().QueryRowContext(ctx, `select code from media where pk='m1'`).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if code != "code-m1" {
		t.Fatalf("carve must not touch media metadata, got code=%q", code)
	}

	// The bytes are physically in the content-addressed store and byte-equal.
	shard := filepath.Join(bs.Dir(), want[:2], want[2:4], want)
	got, err := os.ReadFile(shard)
	if err != nil {
		t.Fatalf("blob not written to the content-addressed store: %v", err)
	}
	if string(got) != string(pix) {
		t.Fatalf("stored blob bytes differ from the cached artifact")
	}
}

// --- Clause 3: re-running is idempotent. ---

func TestCarveIdempotent(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	bs := blobs(t)
	seed(t, st, "m1", feed.MediaFile{Kind: "image", Position: 0, URL: imageURL(nameA), Missing: true})
	dir := cacheDir(t, map[string][]byte{nameA: jpegBytes("A")})

	if _, err := Run(ctx, Deps{Store: st, Blobs: bs, Sources: []carve.Source{walker(dir)}}); err != nil {
		t.Fatal(err)
	}
	blobCount1, _ := st.CountBlobs(ctx)

	sum2, err := Run(ctx, Deps{Store: st, Blobs: bs, Sources: []carve.Source{walker(dir)}})
	if err != nil {
		t.Fatal(err)
	}
	if sum2.Eligible != 0 || sum2.Recovered != 0 || sum2.BlobsNew != 0 {
		t.Fatalf("second run must be a no-op: %+v", sum2)
	}
	blobCount2, _ := st.CountBlobs(ctx)
	if blobCount1 != blobCount2 {
		t.Fatalf("blob count changed on idempotent re-run: %d → %d", blobCount1, blobCount2)
	}
}

// --- Clause 2 (bytes plane): carve never touches an already-downloaded row. ---

func TestCarveNeverTouchesDownloadedFiles(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	bs := blobs(t)
	const keepHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// pos0 eligible (expired); pos1 already downloaded with keepHash.
	seed(t, st, "m1",
		feed.MediaFile{Kind: "image", Position: 0, URL: imageURL(nameA), Missing: true},
		feed.MediaFile{Kind: "image", Position: 1, URL: imageURL(nameB), Hash: keepHash, Downloaded: true},
	)
	// Cache has bytes for BOTH basenames — including DIFFERENT bytes for the
	// already-downloaded one. The downloaded row must be left untouched.
	dir := cacheDir(t, map[string][]byte{
		nameA: jpegBytes("RECOVER-ME"),
		nameB: jpegBytes("DIFFERENT-BYTES-MUST-BE-IGNORED"),
	})

	sum, err := Run(ctx, Deps{Store: st, Blobs: bs, Sources: []carve.Source{walker(dir)}})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Eligible != 1 || sum.Recovered != 1 {
		t.Fatalf("only the expired row is eligible/recovered: %+v", sum)
	}
	var bh string
	if err := st.DB().QueryRowContext(ctx,
		`select coalesce(blob_hash,'') from media_file where media_pk='m1' and position=1`).Scan(&bh); err != nil {
		t.Fatal(err)
	}
	if bh != keepHash {
		t.Fatalf("carve replaced an already-downloaded row's bytes: blob_hash=%q want %q", bh, keepHash)
	}
}

// --- Attribution: a plausible-named but unreferenced artifact is skipped. ---

func TestCarveSkipsUnattributed(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	bs := blobs(t)
	seed(t, st, "m1", feed.MediaFile{Kind: "image", Position: 0, URL: imageURL(nameA), Missing: true})
	// Cache has NO file for nameA, but has an unrelated plausible file (nameZ)
	// and a generic-named file the walker filters out entirely.
	dir := cacheDir(t, map[string][]byte{
		nameZ:      jpegBytes("unrelated"),
		"logo.png": {0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A},
	})

	sum, err := Run(ctx, Deps{Store: st, Blobs: bs, Sources: []carve.Source{walker(dir)}})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Recovered != 0 || sum.StillMissing != 1 || sum.SkippedUnattributed != 1 || sum.BlobsNew != 0 {
		t.Fatalf("unreferenced artifact must be skipped, nothing recovered: %+v", sum)
	}
	if n, _ := st.CountBlobs(ctx); n != 0 {
		t.Fatalf("no blob should be stored, got %d", n)
	}
}

// --- Kind mismatch: bytes whose sniffed kind disagrees with the ref are skipped. ---

func TestCarveKindMismatchSkips(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	bs := blobs(t)
	seed(t, st, "m1", feed.MediaFile{Kind: "image", Position: 0, URL: imageURL(nameA), Missing: true})
	// The cached file under the image's basename is actually video bytes.
	dir := cacheDir(t, map[string][]byte{nameA: mp4Bytes("actually-a-video")})

	sum, err := Run(ctx, Deps{Store: st, Blobs: bs, Sources: []carve.Source{walker(dir)}})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Recovered != 0 || sum.KindMismatch != 1 || sum.StillMissing != 1 || sum.BlobsNew != 0 {
		t.Fatalf("kind mismatch must be skipped: %+v", sum)
	}
}

// --- Dry-run reports would-be recoveries but writes nothing. ---

func TestCarveDryRun(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	bs := blobs(t)
	seed(t, st, "m1", feed.MediaFile{Kind: "image", Position: 0, URL: imageURL(nameA), Missing: true})
	dir := cacheDir(t, map[string][]byte{nameA: jpegBytes("A")})

	sum, err := Run(ctx, Deps{Store: st, Blobs: bs, Sources: []carve.Source{walker(dir)}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !sum.DryRun || sum.Recovered != 1 {
		t.Fatalf("dry-run should report 1 would-be recovery: %+v", sum)
	}
	// ... but wrote nothing.
	var dl int
	if err := st.DB().QueryRowContext(ctx, `select downloaded from media_file where media_pk='m1' and position=0`).Scan(&dl); err != nil {
		t.Fatal(err)
	}
	if dl != 0 {
		t.Fatal("dry-run must not modify the store")
	}
	if n, _ := st.CountBlobs(ctx); n != 0 {
		t.Fatalf("dry-run must not write blobs, got %d", n)
	}
}

// driftSource is a carve.Source that recognized its format but couldn't parse it.
type driftSource struct{}

func (driftSource) Name() string { return "drift" }
func (driftSource) Scan(context.Context, func(carve.Item) error) (reliability.Verdict, error) {
	return reliability.Fail(reliability.StateSchemaDrift, "format changed"), nil
}

// --- A drifted / erroring source is isolated; other sources still run. ---

func TestCarveSourceIsolation(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	bs := blobs(t)
	seed(t, st, "m1", feed.MediaFile{Kind: "image", Position: 0, URL: imageURL(nameA), Missing: true})
	dir := cacheDir(t, map[string][]byte{nameA: jpegBytes("A")})

	sources := []carve.Source{driftSource{}, carvefs.NewChromiumCache(nil), walker(dir)}
	sum, err := Run(ctx, Deps{Store: st, Blobs: bs, Sources: sources})
	if err != nil {
		t.Fatalf("a drifted/stub source must not fail the whole run: %v", err)
	}
	if sum.Recovered != 1 {
		t.Fatalf("the healthy walker must still recover despite sibling source failures: %+v", sum)
	}
	byName := map[string]SourceReport{}
	for _, r := range sum.Sources {
		byName[r.Name] = r
	}
	if byName["drift"].State != "SCHEMA·DRIFT" {
		t.Fatalf("drift source misreported: %+v", byName["drift"])
	}
	if byName["chromium"].State != "ERROR" {
		t.Fatalf("stub source should report ERROR: %+v", byName["chromium"])
	}
	if byName["walker"].State != "OK" {
		t.Fatalf("walker should report OK: %+v", byName["walker"])
	}
}
