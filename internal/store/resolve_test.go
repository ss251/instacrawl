package store

import (
	"context"
	"testing"
)

// TestResolveRepliesLinksPresentParents proves the reply-resolution pass: a reply
// links to its parent only once the parent is in the archive, the pass is
// idempotent, a reply whose parent arrives later resolves on a re-run, and a
// reply whose parent is never archived stays unresolved (not an error — the
// parent may be older than the backfill frontier or unsent).
func TestResolveRepliesLinksPresentParents(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)

	seedReplyMsg(t, st, "p1", "")   // a parent, no reply pointer
	seedReplyMsg(t, st, "c1", "p1") // reply to present parent → resolvable
	seedReplyMsg(t, st, "c2", "px") // reply to absent parent → stays unresolved
	seedReplyMsg(t, st, "m1", "")   // plain message, no reply pointer

	resolved, err := st.ResolveReplies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != 1 {
		t.Fatalf("first pass should resolve exactly c1, got %d", resolved)
	}
	if got := replyResolved(t, st, "c1"); got != 1 {
		t.Fatalf("c1 should be resolved, got %d", got)
	}
	if got := replyResolved(t, st, "c2"); got != 0 {
		t.Fatalf("c2's parent is absent — must stay unresolved, got %d", got)
	}
	if n, _ := st.CountUnresolvedReplies(ctx); n != 1 {
		t.Fatalf("exactly c2 should be unresolved, got %d", n)
	}

	// Idempotent: nothing new to resolve on a second pass.
	if again, _ := st.ResolveReplies(ctx); again != 0 {
		t.Fatalf("second pass should resolve 0, got %d", again)
	}

	// Late-arriving parent: c2 resolves once px lands (a later backfill page).
	seedReplyMsg(t, st, "px", "")
	if late, _ := st.ResolveReplies(ctx); late != 1 {
		t.Fatalf("c2 should resolve after its parent arrives, got %d", late)
	}
	if n, _ := st.CountUnresolvedReplies(ctx); n != 0 {
		t.Fatalf("no unresolved replies should remain, got %d", n)
	}
}

func seedReplyMsg(t *testing.T, st *Store, itemID, replyTo string) {
	t.Helper()
	_, err := st.DB().ExecContext(context.Background(), `
insert into dm_message (item_id, thread_id, user_id, timestamp_us, item_type, item_type_known, reply_to_item_id, source, fetched_at)
values (?, 'T', 1, 100, 'text', 1, ?, 'web', '2026-01-01T00:00:00Z')`, itemID, replyTo)
	if err != nil {
		t.Fatal(err)
	}
}

func replyResolved(t *testing.T, st *Store, itemID string) int {
	t.Helper()
	var v int
	if err := st.DB().QueryRowContext(context.Background(),
		`select reply_resolved from dm_message where item_id=?`, itemID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
