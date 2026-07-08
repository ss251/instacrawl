package store

import (
	"context"
	"testing"
)

// TestFTS5AvailableAndTriggersPopulate is the load-bearing availability proof:
// it confirms the pure-Go modernc.org/sqlite driver instacrawl links is built
// with FTS5, that the external-content virtual tables + AFTER-write triggers in
// searchSchema populate on a base-table insert, and that snippet()/bm25() and
// the two-argument integrity-check all work. If FTS5 were absent, store.Open
// would fail applying the schema and this test would never get to run.
func TestFTS5AvailableAndTriggersPopulate(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	db := st.DB()

	// Insert base rows directly — the triggers fire regardless of the writer.
	if _, err := db.ExecContext(ctx, `
insert into dm_message (item_id, thread_id, timestamp_us, item_type, text, fetched_at)
values ('i1', 't1', 100, 'text', 'the aurora borealis over the fjord', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert dm_message: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
insert into media (pk, media_type, caption_text, field_count, fetched_at)
values ('m1', 1, 'sunset and aurora colors', 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}

	// The AFTER INSERT triggers must have populated both FTS indexes.
	var dmHits, mediaHits int
	if err := db.QueryRowContext(ctx, `select count(*) from dm_message_fts where dm_message_fts match 'aurora'`).Scan(&dmHits); err != nil {
		t.Fatalf("dm fts match: %v", err)
	}
	if err := db.QueryRowContext(ctx, `select count(*) from media_fts where media_fts match 'aurora'`).Scan(&mediaHits); err != nil {
		t.Fatalf("media fts match: %v", err)
	}
	if dmHits != 1 || mediaHits != 1 {
		t.Fatalf("triggers did not populate FTS: dm=%d media=%d", dmHits, mediaHits)
	}

	// snippet() + bm25() must work (they back the search command's output/ranking).
	var snip string
	var score float64
	if err := db.QueryRowContext(ctx, `
select snippet(dm_message_fts, 0, '[', ']', '…', 10), bm25(dm_message_fts)
from dm_message_fts where dm_message_fts match 'aurora'`).Scan(&snip, &score); err != nil {
		t.Fatalf("snippet/bm25: %v", err)
	}
	if snip == "" {
		t.Fatal("empty snippet")
	}

	// The AFTER UPDATE trigger: changing the text must move the match.
	if _, err := db.ExecContext(ctx, `update dm_message set text = 'completely different words' where item_id = 'i1'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select count(*) from dm_message_fts where dm_message_fts match 'aurora'`).Scan(&dmHits); err != nil {
		t.Fatal(err)
	}
	if dmHits != 0 {
		t.Fatalf("AU trigger did not re-index on text change: still %d hits for the old term", dmHits)
	}

	// The two-argument integrity-check is the corruption oracle (the one-arg form
	// does not detect index/content skew).
	if _, err := db.ExecContext(ctx, `insert into dm_message_fts(dm_message_fts, rank) values('integrity-check', 1)`); err != nil {
		t.Fatalf("fts integrity-check(…,1) failed on a healthy index: %v", err)
	}
	if _, err := db.ExecContext(ctx, `insert into media_fts(media_fts, rank) values('integrity-check', 1)`); err != nil {
		t.Fatalf("media fts integrity-check(…,1) failed: %v", err)
	}
}
