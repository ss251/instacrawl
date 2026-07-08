package store

import (
	"context"
	"strings"
	"testing"

	"github.com/ss251/instacrawl/internal/carve"
	"github.com/ss251/instacrawl/internal/feed"
)

// Two valid RFC3339 UTC timestamps, t1 < t2 both lexically and chronologically —
// the recency comparison in the reconciler is a TEXT comparison, so this ordering
// is the thing under test.
const (
	t1 = "2026-01-01T00:00:00Z"
	t2 = "2026-02-01T00:00:00Z"
)

func putMedia(t *testing.T, st *Store, m feed.Media, ts string) {
	t.Helper()
	ctx := context.Background()
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertMedia(ctx, tx, m, ts); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func mediaRow(t *testing.T, st *Store, pk string) (code, source, owner string) {
	t.Helper()
	err := st.DB().QueryRowContext(context.Background(),
		`select coalesce(code,''), source, coalesce(owner_id,'') from media where pk = ?`, pk).Scan(&code, &source, &owner)
	if err != nil {
		t.Fatal(err)
	}
	return code, source, owner
}

// mediaFC builds a Media of a chosen richness (field_count 0..3). owner_id is set
// to marker as an unambiguous winner discriminator: MediaFieldCount ignores
// owner_id, so it never perturbs the tie-break, yet reading it back tells us
// exactly which row won.
func mediaFC(pk, marker, source string, fc int) feed.Media {
	m := feed.Media{PK: pk, OwnerID: marker, Source: source}
	if fc >= 1 {
		m.Code = marker + "C"
	}
	if fc >= 2 {
		m.CaptionText = marker + "cap"
	}
	if fc >= 3 {
		m.Files = []feed.MediaFile{{Kind: "image", URL: "u"}}
	}
	return m
}

// TestMediaTierSQLMatchesGo is the guard that keeps the reconciler's SQL WHERE
// clause and feed.MediaTier (the pure spec) from silently drifting apart. It
// enumerates every (source, field_count, recency) pair for an existing row and
// an incoming upsert, then asserts the row the DB kept is exactly the row
// MediaTier would pick.
func TestMediaTierSQLMatchesGo(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	sources := []string{"web", "mobile", "carve", "other"}
	recencies := []struct {
		name     string
		tsE, tsI string
	}{
		{"incoming-newer", t1, t2},
		{"equal", t1, t1},
		{"incoming-older", t2, t1},
	}

	for _, sE := range sources {
		for _, sI := range sources {
			for fcE := 0; fcE <= 3; fcE++ {
				for fcI := 0; fcI <= 3; fcI++ {
					for _, rc := range recencies {
						if _, err := st.DB().ExecContext(ctx, `delete from media`); err != nil {
							t.Fatal(err)
						}
						existing := mediaFC("x", "EXIST", sE, fcE)
						incoming := mediaFC("x", "INCOM", sI, fcI)
						putMedia(t, st, existing, rc.tsE)
						putMedia(t, st, incoming, rc.tsI)

						_, _, gotOwner := mediaRow(t, st, "x")
						cmp := strings.Compare(rc.tsI, rc.tsE)
						want := feed.MediaTier(existing, incoming, cmp).OwnerID
						if gotOwner != want {
							t.Fatalf("drift: existing(src=%s fc=%d) incoming(src=%s fc=%d) recency=%s → SQL kept %q, MediaTier wants %q",
								sE, fcE, sI, fcI, rc.name, gotOwner, want)
						}
					}
				}
			}
		}
	}
}

// TestCarveStubNeverClobbersRicher is the gate clause "a carved stub never
// overwrites a richer API row" — and its mirror.
func TestCarveStubNeverClobbersRicher(t *testing.T) {
	st := openTemp(t)

	// Rich web row, then a bare carve stub arrives later → web row survives whole.
	rich := feed.Media{PK: "m", OwnerID: "own", Code: "ABC", CaptionText: "hi",
		Files: []feed.MediaFile{{Kind: "image", URL: "u"}}, Source: "web"}
	putMedia(t, st, rich, t1)
	putMedia(t, st, feed.Media{PK: "m", Source: "carve"}, t2) // fc=0, newer, but leaner
	if code, src, _ := mediaRow(t, st, "m"); code != "ABC" || src != "web" {
		t.Fatalf("carve stub clobbered a richer web row: code=%q source=%q", code, src)
	}

	// Mirror: a rich carve row must survive a later web stub (richness, not lane,
	// decides — the gate's "vice-versa").
	richCarve := feed.Media{PK: "n", OwnerID: "own", Code: "XYZ", CaptionText: "yo",
		Files: []feed.MediaFile{{Kind: "video", URL: "v"}}, Source: "carve"}
	putMedia(t, st, richCarve, t1)
	putMedia(t, st, feed.Media{PK: "n", Source: "web"}, t2)
	if code, src, _ := mediaRow(t, st, "n"); code != "XYZ" || src != "carve" {
		t.Fatalf("web stub clobbered a richer carve row: code=%q source=%q", code, src)
	}
}

// TestEqualRichnessRecencyWins documents that the merge is by richness/recency,
// NOT a fixed source ladder: an equal-richness carve row with a newer capture
// beats a web row, and recency is consulted before lane.
func TestEqualRichnessRecencyWins(t *testing.T) {
	st := openTemp(t)

	// Equal richness (fc=1 each), carve captured LATER → carve wins over web.
	putMedia(t, st, mediaFC("a", "WEB", "web", 1), t1)
	putMedia(t, st, mediaFC("a", "CARVE", "carve", 1), t2)
	if _, src, owner := mediaRow(t, st, "a"); owner != "CARVE" || src != "carve" {
		t.Fatalf("equal richness + newer carve should win: source=%q owner=%q", src, owner)
	}

	// Equal richness, SAME capture time → higher lane (web) wins over carve.
	putMedia(t, st, mediaFC("b", "CARVE", "carve", 1), t1)
	putMedia(t, st, mediaFC("b", "WEB", "web", 1), t1)
	if _, src, owner := mediaRow(t, st, "b"); owner != "WEB" || src != "web" {
		t.Fatalf("equal richness + same time should fall to lane order (web): source=%q owner=%q", src, owner)
	}
}

// TestBackfillMediaFile proves the bytes-plane reconciler: fill-only-empty,
// carve byte-provenance, and idempotency.
func TestBackfillMediaFile(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)

	// Seed one media with one EXPIRED file (downloaded=0, missing=1, no bytes)
	// via the production commit path.
	page := &feed.FeedPage{Media: []feed.Media{{
		PK: "m1", Code: "c1", MediaType: 1, Known: true, TakenAtUS: 100,
		Files: []feed.MediaFile{{Kind: "image", Position: 0, URL: "https://cdn/x_1_n.jpg", Missing: true}},
	}}}
	if err := st.CommitFeedPage(ctx, page, feed.FeedCursor{}, FeedEntityOwn, "", FeedKindOwn); err != nil {
		t.Fatal(err)
	}

	ref := carve.FileRef{MediaPK: "m1", Kind: "image", Position: 0, OriginalURL: "https://cdn/x_1_n.jpg"}
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	filled, err := st.BackfillMediaFile(ctx, ref, hash, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !filled {
		t.Fatal("first backfill of an undownloaded row must report filled=true")
	}

	// media_file now carries the bytes; media metadata is untouched.
	var dl, miss int
	var bh, code string
	if err := st.DB().QueryRowContext(ctx,
		`select downloaded, missing, coalesce(blob_hash,'') from media_file where media_pk='m1' and kind='image' and position=0`).
		Scan(&dl, &miss, &bh); err != nil {
		t.Fatal(err)
	}
	if dl != 1 || miss != 0 || bh != hash {
		t.Fatalf("backfill did not fill the row: downloaded=%d missing=%d blob_hash=%q", dl, miss, bh)
	}
	if err := st.DB().QueryRowContext(ctx, `select code from media where pk='m1'`).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if code != "c1" {
		t.Fatalf("backfill must not touch media metadata: code=%q", code)
	}

	// Byte provenance is 'carve', and byte_size was recorded.
	if src, ok, err := st.blobSource(ctx, hash); err != nil || !ok || src != "carve" {
		t.Fatalf("carve blob provenance wrong: src=%q ok=%v err=%v", src, ok, err)
	}
	var size int64
	if err := st.DB().QueryRowContext(ctx, `select byte_size from media_blob where hash=?`, hash).Scan(&size); err != nil {
		t.Fatal(err)
	}
	if size != 42 {
		t.Fatalf("byte_size not recorded: got %d", size)
	}

	// Idempotent: a second backfill of the same row is a no-op.
	filled2, err := st.BackfillMediaFile(ctx, ref, hash, 42)
	if err != nil {
		t.Fatal(err)
	}
	if filled2 {
		t.Fatal("second backfill of an already-downloaded row must report filled=false")
	}
}

// TestUndownloadedFiles proves eligibility is downloaded=0 with a url, covering
// both the expired (missing=1) and transport-error (missing=0) shapes, and
// excluding url-less rows.
func TestUndownloadedFiles(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)

	page := &feed.FeedPage{Media: []feed.Media{{
		PK: "m1", Code: "c", MediaType: 8, Known: true,
		Files: []feed.MediaFile{
			{Kind: "image", Position: 0, URL: "https://cdn/expired_1_n.jpg", Missing: true},            // eligible
			{Kind: "image", Position: 1, URL: "https://cdn/errored_2_n.jpg", Missing: false},           // eligible (transport err shape)
			{Kind: "image", Position: 2, URL: "https://cdn/have_3_n.jpg", Hash: "h", Downloaded: true}, // NOT eligible
			{Kind: "image", Position: 3, URL: "", Missing: true},                                       // NOT eligible (no url)
		},
	}}}
	if err := st.CommitFeedPage(ctx, page, feed.FeedCursor{}, FeedEntityOwn, "", FeedKindOwn); err != nil {
		t.Fatal(err)
	}

	refs, err := st.UndownloadedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 eligible refs (expired + errored, not downloaded, not url-less), got %d: %+v", len(refs), refs)
	}
	for _, r := range refs {
		if r.OriginalURL == "" {
			t.Fatal("a url-less row must never be eligible")
		}
	}
}
