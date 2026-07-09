package store

import (
	"context"
	"testing"
)

// TestReparseUpgradesRecognizableRows exercises DECISIONS §4B end to end: rows
// archived with item_type_known=0 whose type the current parser DOES recognize
// are upgraded from their retained raw_json, with no fetch; genuinely-unknown
// rows stay flagged.
func TestReparseUpgradesRecognizableRows(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)

	// Simulate an old archive: a raven_media message stored as unknown, with the
	// raw JSON the current parser now understands.
	seedUnknown(t, st, "r1", "raven_media",
		`{"item_id":"r1","user_id":1,"timestamp":100,"item_type":"raven_media","raven_media":{"view_mode":"once"}}`)
	// A genuinely-unknown row that must stay flagged.
	seedUnknown(t, st, "u1", "still_unknown",
		`{"item_id":"u1","user_id":1,"timestamp":90,"item_type":"still_unknown"}`)

	upgraded, eligible, err := st.ReparseUnknownMessages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if eligible != 2 || upgraded != 1 {
		t.Fatalf("expected 1 of 2 upgraded, got %d of %d", upgraded, eligible)
	}

	var known int
	var media string
	if err := st.DB().QueryRowContext(ctx,
		`select item_type_known, coalesce(media_json,'') from dm_message where item_id='r1'`).Scan(&known, &media); err != nil {
		t.Fatal(err)
	}
	if known != 1 || media == "" {
		t.Fatalf("r1 must be upgraded (known=1, media populated): known=%d media=%q", known, media)
	}
	if n, _ := st.CountUnknownMessages(ctx); n != 1 {
		t.Fatalf("exactly one unknown row should remain, got %d", n)
	}
	types, _ := st.UnknownMessageTypes(ctx)
	if types["still_unknown"] != 1 || len(types) != 1 {
		t.Fatalf("remaining unknown types wrong: %v", types)
	}
}

func seedUnknown(t *testing.T, st *Store, itemID, itemType, raw string) {
	t.Helper()
	_, err := st.DB().ExecContext(context.Background(), `
insert into dm_message (item_id, thread_id, user_id, timestamp_us, item_type, item_type_known, source, fetched_at, raw_json)
values (?, 'T', 1, 100, ?, 0, 'web', '2026-01-01T00:00:00Z', ?)`, itemID, itemType, raw)
	if err != nil {
		t.Fatal(err)
	}
}
