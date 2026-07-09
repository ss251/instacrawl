package dm

import "testing"

func TestResolveResume(t *testing.T) {
	// No valid cursor → full re-crawl.
	if got := ResolveResume(ThreadCursor{}, false, 100); got != Invalidated {
		t.Fatalf("no cursor → %s, want invalidated", got)
	}
	// Thread activity not newer than what we stored → skip.
	prev := ThreadCursor{SchemaVersion: CursorSchemaVersion, NewestTimestampUS: 500}
	if got := ResolveResume(prev, true, 500); got != Unchanged {
		t.Fatalf("no new activity → %s, want unchanged", got)
	}
	// Thread has newer activity → fetch the tail.
	if got := ResolveResume(prev, true, 900); got != Grew {
		t.Fatalf("newer activity → %s, want grew", got)
	}
}

func TestThreadCursorSchemaMismatchInvalidates(t *testing.T) {
	// A cursor encoded at a different schema version must decode ok=false.
	stale := ThreadCursor{SchemaVersion: 0, OldestCursor: "x"}
	if _, ok := DecodeThreadCursor(stale.Encode()); ok {
		t.Fatal("stale-version cursor must decode ok=false (→ invalidated)")
	}
	cur := ThreadCursor{SchemaVersion: CursorSchemaVersion, OldestCursor: "abc", NewestTimestampUS: 7}
	got, ok := DecodeThreadCursor(cur.Encode())
	if !ok || got.OldestCursor != "abc" || got.NewestTimestampUS != 7 {
		t.Fatalf("round-trip failed: ok=%v got=%+v", ok, got)
	}
	if _, ok := DecodeThreadCursor(""); ok {
		t.Fatal("empty cursor must decode ok=false")
	}
}

func TestTierNeverLetsStubClobberRichProfile(t *testing.T) {
	rich := Participant{UserID: "1", Username: "u", FullName: "Full", ProfilePicURL: "p", FieldCount: 3, Source: "web"}
	stub := Participant{UserID: "1", FieldCount: 0, Source: "web"}
	if got := Tier(rich, stub, true); got.FieldCount != 3 {
		t.Fatal("a numeric-ID stub must never clobber a richer profile, even if newer")
	}
	if got := Tier(stub, rich, true); got.FieldCount != 3 {
		t.Fatal("a richer profile must win over a stub")
	}
	// Equal field count → newer wins.
	a := Participant{Username: "a", FieldCount: 1, Source: "web"}
	b := Participant{Username: "b", FieldCount: 1, Source: "web"}
	if got := Tier(a, b, true); got.Username != "b" {
		t.Fatal("equal field count → newer capture wins")
	}
}

func TestFieldCount(t *testing.T) {
	if FieldCount("", "", "") != 0 {
		t.Fatal("empty profile → 0")
	}
	if FieldCount("u", "Full", "pic") != 3 {
		t.Fatal("full profile → 3")
	}
}

func TestParseThreadSurfacesUnknownTypesReactionsReplies(t *testing.T) {
	body := []byte(`{"thread":{"thread_id":"340282","thread_title":"squad","is_group":true,
	  "last_activity_at":1700000000000000,"has_older":true,"oldest_cursor":"CUR2",
	  "admin_user_ids":[11],
	  "users":[{"pk":11,"username":"alice","full_name":"Alice A","profile_pic_url":"http://x/a.jpg"},
	           {"pk":22,"username":"bob"}],
	  "items":[
	    {"item_id":"m1","user_id":11,"timestamp":1700000000000000,"item_type":"text","text":"hi",
	     "reactions":{"likes":[{"sender_id":22,"timestamp":1700000000000001}],"emojis":[{"sender_id":22,"emoji":"🔥"}]}},
	    {"item_id":"m2","user_id":22,"timestamp":1700000000000002,"item_type":"text","text":"reply",
	     "replied_to_message":{"item_id":"m1"}},
	    {"item_id":"m3","user_id":11,"timestamp":1700000000000003,"item_type":"brand_new_type"},
	    {"item_id":"m4","user_id":11,"timestamp":1700000000000004,"item_type":"action_log"}
	  ]}}`)
	page, err := ParseThread(body)
	if err != nil {
		t.Fatal(err)
	}
	if page.ThreadID != "340282" || page.OldestCursor != "CUR2" {
		t.Fatalf("thread meta wrong: id=%q cursor=%q", page.ThreadID, page.OldestCursor)
	}
	if len(page.Messages) != 4 || len(page.ItemIDs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(page.Messages))
	}
	// Unknown type surfaced, not skipped.
	if len(page.UnknownTypes) != 1 || page.UnknownTypes[0] != "brand_new_type" {
		t.Fatalf("unknown type not surfaced: %v", page.UnknownTypes)
	}
	// Reactions captured (1 like + 1 emoji on m1).
	if len(page.Messages[0].Reactions) != 2 {
		t.Fatalf("reactions not captured: %+v", page.Messages[0].Reactions)
	}
	// Reply ref captured.
	if page.Messages[1].ReplyTo == nil || page.Messages[1].ReplyTo.ParentItemID != "m1" {
		t.Fatalf("reply ref not captured: %+v", page.Messages[1].ReplyTo)
	}
	// action_log projected to a thread event.
	if len(page.Events) != 1 || page.Events[0].EventID != "m4" {
		t.Fatalf("action_log not projected to event: %+v", page.Events)
	}
	// Participant field counts (birdclaw tiering input): alice=3, bob=1.
	if page.Participants[0].FieldCount != 3 || page.Participants[1].FieldCount != 1 {
		t.Fatalf("participant field counts wrong: %+v", page.Participants)
	}
	// Every row tagged source='web'.
	for _, m := range page.Messages {
		if m.Source != "web" {
			t.Fatalf("message not tagged source=web: %+v", m)
		}
	}
	// Timestamp window for unsend detection.
	if page.MinTS != 1700000000000000 || page.MaxTS != 1700000000000004 {
		t.Fatalf("ts window wrong: min=%d max=%d", page.MinTS, page.MaxTS)
	}
}

func TestParseNewShareAndMediaTypes(t *testing.T) {
	body := []byte(`{"thread":{"thread_id":"T","has_older":false,
	  "users":[{"pk":1,"username":"a"}],
	  "items":[
	    {"item_id":"r1","user_id":1,"timestamp":100,"item_type":"raven_media","raven_media":{"view_mode":"once"}},
	    {"item_id":"p1","user_id":1,"timestamp":90,"item_type":"profile","profile":{"pk":"9","username":"shared"}},
	    {"item_id":"c1","user_id":1,"timestamp":80,"item_type":"clip","clip":{"code":"z"}},
	    {"item_id":"vc","user_id":1,"timestamp":70,"item_type":"video_call_event","video_call_event":{"action":"started"}},
	    {"item_id":"u1","user_id":1,"timestamp":60,"item_type":"still_unknown"}
	  ]}}`)
	page, err := ParseThread(body)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Message{}
	for _, m := range page.Messages {
		byID[m.ItemID] = m
	}
	if r := byID["r1"]; !r.Known || r.MediaJSON == "" {
		t.Fatalf("raven_media should be recognized with a media payload: %+v", r)
	}
	if p := byID["p1"]; !p.Known || p.SharedJSON == "" {
		t.Fatalf("profile should be recognized with a shared payload: %+v", p)
	}
	if c := byID["c1"]; !c.Known || c.SharedJSON == "" {
		t.Fatalf("clip should be recognized with a shared payload: %+v", c)
	}
	if v := byID["vc"]; !v.Known {
		t.Fatalf("video_call_event should be recognized: %+v", v)
	}
	// ONLY the genuinely unknown type is surfaced as drift.
	if len(page.UnknownTypes) != 1 || page.UnknownTypes[0] != "still_unknown" {
		t.Fatalf("unknown types wrong: %v", page.UnknownTypes)
	}
	// video_call_event projects to a thread event, like action_log.
	foundVC := false
	for _, e := range page.Events {
		if e.EventID == "vc" && e.Kind == "video_call_event" {
			foundVC = true
		}
	}
	if !foundVC {
		t.Fatalf("video_call_event not projected to a thread event: %+v", page.Events)
	}
}

// TestParseItemReparse exercises the §4B re-parse path: one stored item's raw
// JSON decoded back through the parser.
func TestParseItemReparse(t *testing.T) {
	raw := []byte(`{"item_id":"r1","user_id":1,"timestamp":100,"item_type":"raven_media","raven_media":{"view_mode":"once"}}`)
	m, ev, err := ParseItem(raw, "T", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Known || m.ItemID != "r1" || m.ThreadID != "T" || m.MediaJSON == "" || m.Source != "web" {
		t.Fatalf("reparsed item wrong: %+v", m)
	}
	if ev != nil {
		t.Fatalf("raven_media must not project a thread event: %+v", ev)
	}
}

func TestParseInboxAndPending(t *testing.T) {
	body := []byte(`{"seq_id":98765,"inbox":{"has_older":true,"oldest_cursor":"NEXT",
	  "threads":[{"thread_id":"1","thread_title":"a","is_group":false,"last_activity_at":123,
	              "users":[{"pk":9,"username":"x"}]}]}}`)
	page, err := ParseInbox(body)
	if err != nil {
		t.Fatal(err)
	}
	if page.SeqID != "98765" || page.NextCursor != "NEXT" || len(page.Threads) != 1 {
		t.Fatalf("inbox parse wrong: %+v", page)
	}
	if page.Threads[0].IsPending {
		t.Fatal("regular inbox thread must not be pending")
	}
	pend, err := ParsePendingInbox(body)
	if err != nil {
		t.Fatal(err)
	}
	if !pend.Threads[0].IsPending {
		t.Fatal("pending inbox thread must be marked IsPending")
	}
}

func TestParseInboxNoOlderHasEmptyCursor(t *testing.T) {
	body := []byte(`{"seq_id":1,"inbox":{"has_older":false,"oldest_cursor":"IGNORED","threads":[]}}`)
	page, err := ParseInbox(body)
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != "" {
		t.Fatalf("has_older=false must yield empty NextCursor, got %q", page.NextCursor)
	}
}
