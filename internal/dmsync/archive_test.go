package dmsync

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/pace"
	"github.com/ss251/instacrawl/internal/session"
	"github.com/ss251/instacrawl/internal/store"
)

// replayFetcher serves canned responses computed from the request, and counts
// calls by endpoint so tests can assert that an unchanged thread is not fetched.
type replayFetcher struct {
	respond     func(req ig.Request) ig.RawResponse
	inboxCalls  int
	threadCalls int
	pendCalls   int
}

func (f *replayFetcher) FetchSelfProfile(context.Context, *session.WebBundle) (ig.RawResponse, error) {
	return ig.RawResponse{}, nil
}
func (f *replayFetcher) Fetch(_ context.Context, _ *session.WebBundle, req ig.Request) (ig.RawResponse, error) {
	switch {
	case req.Query.Get("folder") == "pending":
		f.pendCalls++
	case strings.Contains(req.Path, "/inbox/"):
		f.inboxCalls++
	case strings.Contains(req.Path, "/threads/"):
		f.threadCalls++
	}
	return f.respond(req), nil
}
func (f *replayFetcher) Name() string { return "replay" }

func jsonResp(body string) ig.RawResponse {
	return ig.RawResponse{Status: 200, ContentType: "application/json", Body: []byte(body)}
}

func tempStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func bundle() *session.WebBundle {
	b := &session.WebBundle{SessionID: "1%3Afake", DSUserID: "1"}
	b.Normalize()
	return b
}

func deps(f ig.Fetcher, st *store.Store) Deps {
	return Deps{Fetcher: f, Store: st, Bundle: bundle(), Pacer: pace.NewForTest(), IncludePending: false}
}

// --- The Phase 2 gate: two runs, run-2 fetches 0 new for unchanged threads. ---

func TestIdempotencyGate(t *testing.T) {
	// Inbox: one thread T1, last activity 100. Thread: 2 messages, fully
	// backfilled, newest ts 100.
	inbox := `{"seq_id":10,"inbox":{"has_older":false,"threads":[
	  {"thread_id":"T1","is_group":false,"last_activity_at":100,"users":[{"pk":5,"username":"bob","full_name":"Bob"}]}]}}`
	thread := `{"thread":{"thread_id":"T1","is_group":false,"has_older":false,
	  "users":[{"pk":5,"username":"bob","full_name":"Bob"}],
	  "items":[
	    {"item_id":"i2","user_id":5,"timestamp":100,"item_type":"text","text":"hey"},
	    {"item_id":"i1","user_id":5,"timestamp":90,"item_type":"text","text":"hi"}]}}`
	f := &replayFetcher{respond: func(req ig.Request) ig.RawResponse {
		if strings.Contains(req.Path, "/threads/") {
			return jsonResp(thread)
		}
		return jsonResp(inbox)
	}}
	st := tempStore(t)
	ctx := context.Background()

	// Run 1: fetches the thread, stores 2 messages.
	s1, err := Archive(ctx, deps(f, st))
	if err != nil {
		t.Fatal(err)
	}
	if s1.State != "OK" || s1.ThreadsFetched != 1 || s1.MessagesNew != 2 {
		t.Fatalf("run1: %+v", s1)
	}
	firstThreadCalls := f.threadCalls

	// Run 2: T1 is unchanged (last activity 100 == stored newest) → skipped,
	// ZERO thread fetches, ZERO new messages.
	s2, err := Archive(ctx, deps(f, st))
	if err != nil {
		t.Fatal(err)
	}
	if s2.ThreadsSkipped != 1 || s2.ThreadsFetched != 0 {
		t.Fatalf("run2 should skip the unchanged thread: %+v", s2)
	}
	if s2.MessagesNew != 0 {
		t.Fatalf("run2 must fetch 0 new messages, got %d", s2.MessagesNew)
	}
	if f.threadCalls != firstThreadCalls {
		t.Fatalf("run2 must make 0 thread fetches (was %d, now %d)", firstThreadCalls, f.threadCalls)
	}

	// Every stored message is tagged source='web'.
	n, _ := st.CountMessages(ctx)
	if n != 2 {
		t.Fatalf("expected 2 messages total, got %d", n)
	}
}

// --- A page-capped thread resumes its backfill on the next run (not skipped,
//     and the deep frontier is not clobbered). ---

func TestBackfillResumesAcrossRuns(t *testing.T) {
	inbox := `{"seq_id":10,"inbox":{"has_older":false,"threads":[
	  {"thread_id":"T1","last_activity_at":100,"users":[{"pk":5,"username":"bob"}]}]}}`
	// A three-page thread: head → C1 → C2 → start. (The page guard's minimum of
	// 1 still admits the first transition, so a three-page thread is what actually
	// leaves the crawl mid-backfill under a 1-page cap.)
	page1 := `{"thread":{"thread_id":"T1","has_older":true,"oldest_cursor":"C1",
	  "users":[{"pk":5,"username":"bob"}],
	  "items":[
	    {"item_id":"i4","user_id":5,"timestamp":100,"item_type":"text","text":"d"},
	    {"item_id":"i3","user_id":5,"timestamp":90,"item_type":"text","text":"c"}]}}`
	page2 := `{"thread":{"thread_id":"T1","has_older":true,"oldest_cursor":"C2",
	  "users":[{"pk":5,"username":"bob"}],
	  "items":[
	    {"item_id":"i2","user_id":5,"timestamp":80,"item_type":"text","text":"b"},
	    {"item_id":"i1","user_id":5,"timestamp":70,"item_type":"text","text":"a"}]}}`
	page3 := `{"thread":{"thread_id":"T1","has_older":false,
	  "users":[{"pk":5,"username":"bob"}],
	  "items":[{"item_id":"i0","user_id":5,"timestamp":60,"item_type":"text","text":"oldest"}]}}`
	f := &replayFetcher{respond: func(req ig.Request) ig.RawResponse {
		if strings.Contains(req.Path, "/threads/") {
			switch req.Query.Get("cursor") {
			case "C1":
				return jsonResp(page2)
			case "C2":
				return jsonResp(page3)
			default:
				return jsonResp(page1)
			}
		}
		return jsonResp(inbox)
	}}
	st := tempStore(t)
	ctx := context.Background()

	// Run 1: 1-page cap admits page1 + page2 then stops at C2 → 4 messages, NOT
	// fully backfilled (frontier parked at C2).
	d1 := deps(f, st)
	d1.MaxThreadPages = 1
	if _, err := Archive(ctx, d1); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountMessages(ctx); n != 4 {
		t.Fatalf("run1 should store 4 messages, got %d", n)
	}

	// Run 2: the head is unchanged (100), but the thread is not backfilled — it
	// must RESUME from C2 and fetch the final page, not skip.
	s2, err := Archive(ctx, deps(f, st))
	if err != nil {
		t.Fatal(err)
	}
	if s2.ThreadsSkipped != 0 {
		t.Fatalf("a partially-backfilled thread must not be skipped: %+v", s2)
	}
	if s2.MessagesNew != 1 {
		t.Fatalf("run2 should backfill exactly the 1 remaining message: %+v", s2)
	}
	if n, _ := st.CountMessages(ctx); n != 5 {
		t.Fatalf("expected 5 messages after backfill, got %d", n)
	}

	// Run 3: now fully backfilled AND unchanged → zero fetches.
	calls := f.threadCalls
	s3, err := Archive(ctx, deps(f, st))
	if err != nil {
		t.Fatal(err)
	}
	if s3.ThreadsSkipped != 1 || f.threadCalls != calls {
		t.Fatalf("run3 (backfilled + unchanged) must skip with 0 fetches: %+v (calls %d→%d)", s3, calls, f.threadCalls)
	}
}

// --- Terminal states halt the whole run. ---

func TestCheckpointHaltsRun(t *testing.T) {
	f := &replayFetcher{respond: func(ig.Request) ig.RawResponse {
		return jsonResp(`{"message":"checkpoint_required","checkpoint_url":"https://www.instagram.com/challenge/AB/CD/","status":"fail"}`)
	}}
	s, err := Archive(context.Background(), deps(f, tempStore(t)))
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "AUTH·CHECKPOINT" || !strings.Contains(s.ChallengeURL, "/challenge/") {
		t.Fatalf("checkpoint must halt with a challenge URL: %+v", s)
	}
}

// --- An unknown item_type is flagged as drift, but the message is RETAINED
//     (committed leniently with item_type_known=0 + raw_json), never lost. ---

func TestUnknownItemTypeRetainsAndFlags(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	inbox := `{"seq_id":1,"inbox":{"has_older":false,"threads":[
	  {"thread_id":"T1","last_activity_at":200,"users":[{"pk":5,"username":"bob"}]}]}}`
	// A page mixing a known text item and a brand-new type.
	thread := `{"thread":{"thread_id":"T1","has_older":false,"users":[{"pk":5,"username":"bob"}],
	  "items":[
	    {"item_id":"i2","user_id":5,"timestamp":200,"item_type":"a_brand_new_type"},
	    {"item_id":"i1","user_id":5,"timestamp":190,"item_type":"text","text":"hi"}]}}`
	f := &replayFetcher{respond: func(req ig.Request) ig.RawResponse {
		if strings.Contains(req.Path, "/threads/") {
			return jsonResp(thread)
		}
		return jsonResp(inbox)
	}}
	s, err := Archive(ctx, deps(f, st))
	if err != nil {
		t.Fatal(err)
	}
	// The thread is flagged as drift (so the new type gets surfaced)…
	if s.State != "SCHEMA·DRIFT" || len(s.DriftedThreads) != 1 || s.DriftedThreads[0] != "T1" {
		t.Fatalf("unknown item_type should flag the thread as drift: %+v", s)
	}
	// …but BOTH messages are retained, not dropped.
	if s.MessagesNew != 2 {
		t.Fatalf("an unknown-type page must be committed, not dropped: MessagesNew=%d", s.MessagesNew)
	}
	var known int
	if err := st.DB().QueryRowContext(ctx, `select item_type_known from dm_message where item_id='i2'`).Scan(&known); err != nil {
		t.Fatalf("the unknown-type message must be stored: %v", err)
	}
	if known != 0 {
		t.Fatal("the unknown-type message must be stored with item_type_known=0")
	}
}

// --- Unsend detection: a previously-captured item absent on re-crawl is flagged, not deleted. ---

func TestUnsendDetection(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)

	inbox1 := `{"seq_id":1,"inbox":{"has_older":false,"threads":[
	  {"thread_id":"T1","last_activity_at":110,"users":[{"pk":5,"username":"bob"}]}]}}`
	thread1 := `{"thread":{"thread_id":"T1","has_older":false,"users":[{"pk":5,"username":"bob"}],
	  "items":[
	    {"item_id":"i3","user_id":5,"timestamp":110,"item_type":"text","text":"c"},
	    {"item_id":"i2","user_id":5,"timestamp":100,"item_type":"text","text":"b"},
	    {"item_id":"i1","user_id":5,"timestamp":90,"item_type":"text","text":"a"}]}}`
	f1 := &replayFetcher{respond: func(req ig.Request) ig.RawResponse {
		if strings.Contains(req.Path, "/threads/") {
			return jsonResp(thread1)
		}
		return jsonResp(inbox1)
	}}
	if _, err := Archive(ctx, deps(f1, st)); err != nil {
		t.Fatal(err)
	}

	// Re-crawl (full): i2 is gone from the thread but its timestamp is inside
	// the re-fetched window [90,110], so it must be flagged unsent — not deleted.
	thread2 := `{"thread":{"thread_id":"T1","has_older":false,"users":[{"pk":5,"username":"bob"}],
	  "items":[
	    {"item_id":"i3","user_id":5,"timestamp":110,"item_type":"text","text":"c"},
	    {"item_id":"i1","user_id":5,"timestamp":90,"item_type":"text","text":"a"}]}}`
	f2 := &replayFetcher{respond: func(req ig.Request) ig.RawResponse {
		if strings.Contains(req.Path, "/threads/") {
			return jsonResp(thread2)
		}
		return jsonResp(inbox1)
	}}
	d := deps(f2, st)
	d.FullRecrawl = true
	if _, err := Archive(ctx, d); err != nil {
		t.Fatal(err)
	}

	unsent, err := st.UnsentObserved(ctx, "i2")
	if err != nil {
		t.Fatal(err)
	}
	if !unsent {
		t.Fatal("i2 should be flagged unsent_observed after disappearing within the re-fetched window")
	}
	// Not deleted: all 3 messages still present.
	if n, _ := st.CountMessages(ctx); n != 3 {
		t.Fatalf("unsend must flag, not delete: expected 3 messages, got %d", n)
	}
}

// --- Pending inbox is included when requested. ---

func TestPendingInboxIncluded(t *testing.T) {
	regular := `{"seq_id":1,"inbox":{"has_older":false,"threads":[]}}`
	pending := `{"seq_id":2,"inbox":{"has_older":false,"threads":[
	  {"thread_id":"P1","is_pending":true,"last_activity_at":50,"users":[{"pk":9,"username":"req"}]}]}}`
	thread := `{"thread":{"thread_id":"P1","has_older":false,"users":[{"pk":9,"username":"req"}],
	  "items":[{"item_id":"p1","user_id":9,"timestamp":50,"item_type":"text","text":"let me in"}]}}`
	f := &replayFetcher{respond: func(req ig.Request) ig.RawResponse {
		switch {
		case req.Query.Get("folder") == "pending":
			return jsonResp(pending)
		case strings.Contains(req.Path, "/threads/"):
			return jsonResp(thread)
		default:
			return jsonResp(regular)
		}
	}}
	d := deps(f, tempStore(t))
	d.IncludePending = true
	s, err := Archive(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if s.InboxThreads != 1 || s.ThreadsFetched != 1 || s.MessagesNew != 1 {
		t.Fatalf("pending thread should be archived: %+v", s)
	}
	if f.pendCalls == 0 {
		t.Fatal("pending inbox endpoint must be called when IncludePending is set")
	}
}
