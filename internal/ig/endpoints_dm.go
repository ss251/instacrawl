package ig

import "net/url"

// ==========================================================================
// DM endpoint builders. LIVE-VALIDATED 2026-07-09 (schema-only probe against a
// logged-in session): the inbox and thread paths return the expected shapes —
// inbox → {seq_id, inbox:{threads[], has_older, oldest_cursor}}, thread →
// {thread:{thread_id, thread_title, is_group, users[], admin_user_ids, items[],
// has_older, oldest_cursor, last_activity_at}}, item → {item_id, user_id,
// timestamp, item_type, text}. The media/voice/share/reaction/reply-ref
// sub-field names still await a media-bearing sample. Builders take PRIMITIVES
// (not dm cursor types) so this package stays free of any cycle with internal/dm.
// ==========================================================================

// InboxRequest builds a direct inbox page request. cursor/seqID are empty on the
// first page and threaded through subsequent pages.
func InboxRequest(cursor, seqID string) Request {
	q := url.Values{}
	q.Set("visual_message_return_type", "unseen")
	q.Set("thread_message_limit", "20")
	q.Set("persistentBadging", "true")
	q.Set("limit", "20")
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if seqID != "" {
		q.Set("seq_id", seqID)
	}
	return Request{Method: "GET", Path: "/api/v1/direct_v2/inbox/", Query: q}
}

// PendingInboxRequest builds a message-requests (pending) inbox page request.
// LIVE-VALIDATED 2026-07-09: the web API serves message requests from the SAME
// /inbox/ path with folder=pending — the standalone /pending_inbox/ and
// /spam_inbox/ paths return 404 on www.instagram.com (they are mobile-API paths).
func PendingInboxRequest(cursor string) Request {
	q := url.Values{}
	q.Set("visual_message_return_type", "unseen")
	q.Set("thread_message_limit", "20")
	q.Set("folder", "pending")
	q.Set("limit", "20")
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return Request{Method: "GET", Path: "/api/v1/direct_v2/inbox/", Query: q}
}

// ThreadRequest builds one backward page of a thread's messages. oldestCursor is
// empty on the first (newest) page and set to the prior page's oldest_cursor to
// page toward the start of the thread.
func ThreadRequest(threadID, oldestCursor string) Request {
	q := url.Values{}
	q.Set("visual_message_return_type", "unseen")
	q.Set("direction", "older")
	q.Set("limit", "20")
	if oldestCursor != "" {
		q.Set("cursor", oldestCursor)
	}
	return Request{Method: "GET", Path: "/api/v1/direct_v2/threads/" + url.PathEscape(threadID) + "/", Query: q}
}
