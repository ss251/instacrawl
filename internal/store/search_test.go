package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ss251/instacrawl/internal/dm"
	"github.com/ss251/instacrawl/internal/feed"
)

func seedDMPage(t *testing.T, st *Store, page *dm.ThreadPage) {
	t.Helper()
	if err := st.CommitThreadPage(context.Background(), page, dm.ThreadCursor{}); err != nil {
		t.Fatal(err)
	}
}

func seedDM(t *testing.T, st *Store, threadID, itemID string, ts int64, text string) {
	t.Helper()
	seedDMPage(t, st, &dm.ThreadPage{
		ThreadID: threadID,
		Messages: []dm.Message{{ItemID: itemID, ThreadID: threadID, TimestampUS: ts, ItemType: dm.ItemText, Known: true, Text: text}},
	})
}

func seedMediaCaption(t *testing.T, st *Store, pk, code, caption string, files []feed.MediaFile) {
	t.Helper()
	page := &feed.FeedPage{Media: []feed.Media{{
		PK: pk, Code: code, MediaType: 1, Known: true, CaptionText: caption, TakenAtUS: 200, Files: files,
	}}}
	if err := st.CommitFeedPage(context.Background(), page, feed.FeedCursor{}, FeedEntityOwn, "", FeedKindOwn); err != nil {
		t.Fatal(err)
	}
}

// --- The §7 gate: search returns hits across DMs AND posts. ---

func TestSearchCrossEntity(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	seedDM(t, st, "t1", "i1", 100, "the aurora borealis over the fjord")
	seedMediaCaption(t, st, "m1", "ABC123", "sunset and aurora colors", nil)

	hits, err := st.Search(ctx, "aurora", "all", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("a term in both a DM and a caption must return 2 hits, got %d: %+v", len(hits), hits)
	}
	var gotDM, gotMedia bool
	for _, h := range hits {
		switch h.Entity {
		case "dm":
			gotDM = h.Key == "i1" && h.ThreadID == "t1"
		case "media":
			gotMedia = h.Key == "m1" && h.Code == "ABC123"
		}
		if !strings.Contains(h.Snippet, "[aurora]") {
			t.Fatalf("snippet must bracket the match: %q", h.Snippet)
		}
	}
	if !gotDM || !gotMedia {
		t.Fatalf("missing an entity: dm=%v media=%v (%+v)", gotDM, gotMedia, hits)
	}

	// Words unique to one entity return only that entity.
	if h, _ := st.Search(ctx, "fjord", "all", 20); len(h) != 1 || h[0].Entity != "dm" {
		t.Fatalf("a DM-only word must return 1 dm hit: %+v", h)
	}
	if h, _ := st.Search(ctx, "colors", "all", 20); len(h) != 1 || h[0].Entity != "media" {
		t.Fatalf("a caption-only word must return 1 media hit: %+v", h)
	}

	// --type filter.
	if h, _ := st.Search(ctx, "aurora", "dm", 20); len(h) != 1 || h[0].Entity != "dm" {
		t.Fatalf("--type dm must return only dm: %+v", h)
	}
	if h, _ := st.Search(ctx, "aurora", "media", 20); len(h) != 1 || h[0].Entity != "media" {
		t.Fatalf("--type media must return only media: %+v", h)
	}
}

// --- A DM hit carries its author's @handle (joined from dm_user). ---

func TestSearchIncludesSender(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	seedDMPage(t, st, &dm.ThreadPage{
		ThreadID: "t1",
		Thread: &dm.Thread{ThreadID: "t1", Source: "web", Participants: []dm.Participant{
			{UserID: "42", Username: "alex", FieldCount: 2, Source: "web"},
		}},
		Messages: []dm.Message{{ItemID: "i1", ThreadID: "t1", UserID: "42", TimestampUS: 100, ItemType: dm.ItemText, Known: true, Text: "aurora over kyoto"}},
	})
	hits, err := st.Search(ctx, "aurora", "dm", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Sender != "alex" {
		t.Fatalf("a dm hit must carry the sender @handle: %+v", hits)
	}

	// A message whose author is not in dm_user resolves to an empty sender — the
	// left join must not drop the hit or error.
	seedDM(t, st, "t2", "i2", 100, "aurora with no known sender")
	got, err := st.Search(ctx, "aurora", "dm", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("both hits must return even when one sender is unknown: %+v", got)
	}
}

// --- Ranking: a stronger match sorts first; the snippet brackets the term. ---

func TestSearchRanking(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	// i2 mentions the term three times in a short message → higher bm25.
	seedDM(t, st, "t1", "i1", 100, "aurora once amid much other unrelated padding text here and there")
	seedDM(t, st, "t1", "i2", 200, "aurora aurora aurora")

	hits, err := st.Search(ctx, "aurora", "dm", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if hits[0].Key != "i2" {
		t.Fatalf("the stronger match must rank first, got %q first", hits[0].Key)
	}
}

// --- Unsent messages stay searchable, flagged. ---

func TestSearchUnsentRetained(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	// Page 1: message X exists in window [100,100].
	seedDMPage(t, st, &dm.ThreadPage{
		ThreadID: "t1", MinTS: 100, MaxTS: 100, ItemIDs: []string{"x"},
		Messages: []dm.Message{{ItemID: "x", ThreadID: "t1", TimestampUS: 100, ItemType: dm.ItemText, Known: true, Text: "supersecretword lives here"}},
	})
	// Page 2: same window re-fetched WITHOUT X → X is detected as unsent (flagged, not deleted).
	seedDMPage(t, st, &dm.ThreadPage{
		ThreadID: "t1", MinTS: 100, MaxTS: 100, ItemIDs: []string{},
	})

	hits, err := st.Search(ctx, "supersecretword", "dm", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !hits[0].Unsent {
		t.Fatalf("an unsent message must remain searchable and be flagged unsent: %+v", hits)
	}
}

// --- Tiering cannot corrupt the index; integrity-check passes after churn. ---

func TestSearchTieringIntegrity(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	// A: rich-ish (code+caption, fc=2), caption "alpha".
	seedMediaCaption(t, st, "m1", "C", "alpha", nil)
	// B: leaner (caption only, fc=1) → rejected by the tiering WHERE → "alpha" stays.
	seedMediaCaption(t, st, "m1", "", "beta", nil)
	if h, _ := st.Search(ctx, "alpha", "media", 20); len(h) != 1 {
		t.Fatalf("a rejected leaner upsert must leave the index on the richer caption: %+v", h)
	}
	if h, _ := st.Search(ctx, "beta", "media", 20); len(h) != 0 {
		t.Fatalf("a rejected upsert's caption must NOT be indexed: %+v", h)
	}
	// C: richer (code+caption+files, fc=3) → wins on richness → caption "gamma".
	seedMediaCaption(t, st, "m1", "C", "gamma", []feed.MediaFile{{Kind: "image", URL: "u"}})
	if h, _ := st.Search(ctx, "gamma", "media", 20); len(h) != 1 {
		t.Fatalf("a richer upsert must re-index to the new caption: %+v", h)
	}
	if h, _ := st.Search(ctx, "alpha", "media", 20); len(h) != 0 {
		t.Fatalf("the superseded caption must be gone from the index: %+v", h)
	}
	// The corruption oracle: the two-arg integrity-check must pass.
	for _, table := range []string{"dm_message_fts", "media_fts"} {
		if _, err := st.DB().ExecContext(ctx, "insert into "+table+"("+table+", rank) values('integrity-check', 1)"); err != nil {
			t.Fatalf("%s integrity-check failed after tiering churn: %v", table, err)
		}
	}
}

// --- Hostile input is neutralized, never injected, never a false match. ---

func TestSearchInjectionSafe(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	seedDM(t, st, "t1", "i1", 100, "aurora")
	seedMediaCaption(t, st, "m1", "C", "aurora", nil)

	for _, q := range []string{
		`"aurora" OR item_id:* NEAR/2 (`,
		`'; drop table dm_message; --`,
		`col:val*`,
		`aurora AND (`,
	} {
		hits, err := st.Search(ctx, q, "all", 20)
		if err != nil {
			t.Fatalf("hostile query %q must not error (it is sanitized): %v", q, err)
		}
		_ = hits // matching or not is fine; not erroring / not injecting is the point
	}
	// The drop-table attempt did nothing: the table is intact and queryable.
	if _, err := st.CountMessages(ctx); err != nil {
		t.Fatalf("dm_message table damaged by an injection attempt: %v", err)
	}
}

// --- Empty and no-match queries. ---

func TestSearchEmptyAndNoMatch(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	seedDM(t, st, "t1", "i1", 100, "hello world")

	if _, err := st.Search(ctx, "", "all", 20); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("empty query must be ErrEmptyQuery, got %v", err)
	}
	if _, err := st.Search(ctx, "!!!  ...  ???", "all", 20); !errors.Is(err, ErrEmptyQuery) {
		t.Fatalf("a punctuation-only query must reduce to ErrEmptyQuery, got %v", err)
	}
	hits, err := st.Search(ctx, "zzzunfindableterm", "all", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("a no-match query must return an empty slice, got %+v", hits)
	}
}

// --- Upgrading a pre-search archive backfills the index (offline migration proof). ---

func TestSearchMigrationRebuild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedDM(t, st, "t1", "i1", 100, "aurora in a message")
	seedMediaCaption(t, st, "m1", "C", "aurora in a caption", nil)

	// Simulate an archive written by a pre-search (v3) binary: the base rows exist
	// but there is no FTS layer, and the version is stamped 3.
	for _, ddl := range []string{
		"drop trigger if exists dm_message_fts_ai", "drop trigger if exists dm_message_fts_ad", "drop trigger if exists dm_message_fts_au",
		"drop trigger if exists media_fts_ai", "drop trigger if exists media_fts_ad", "drop trigger if exists media_fts_au",
		"drop table if exists dm_message_fts", "drop table if exists media_fts",
		"delete from schema_migrations", "insert into schema_migrations(version) values(3)",
	} {
		if _, err := st.DB().ExecContext(ctx, ddl); err != nil {
			t.Fatalf("simulate v3: %q: %v", ddl, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen with the current binary: Open reads version 3 < 4 and rebuilds.
	st2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen (should migrate + rebuild): %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	hits, err := st2.Search(ctx, "aurora", "all", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("migration rebuild must index the pre-existing rows, got %d hits", len(hits))
	}
	for _, table := range []string{"dm_message_fts", "media_fts"} {
		if _, err := st2.DB().ExecContext(ctx, "insert into "+table+"("+table+", rank) values('integrity-check', 1)"); err != nil {
			t.Fatalf("%s integrity-check failed after rebuild: %v", table, err)
		}
	}
}

// --- Optimize is callable after traffic. ---

func TestOptimizeSearch(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	seedDM(t, st, "t1", "i1", 100, "aurora")
	seedMediaCaption(t, st, "m1", "C", "aurora", nil)
	if err := st.OptimizeSearch(ctx); err != nil {
		t.Fatalf("OptimizeSearch after traffic: %v", err)
	}
}
