package ig

import "net/url"

// ==========================================================================
// DM endpoint builders — part of the fragile surface (like endpoints.go). The
// paths and params are the Phase-2 best guess at Instagram's private direct_v2
// API, retuned in 2b against a real session. They take PRIMITIVES (not dm
// cursor types) so this package stays free of any import cycle with internal/dm.
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
func PendingInboxRequest(cursor string) Request {
	q := url.Values{}
	q.Set("visual_message_return_type", "unseen")
	q.Set("thread_message_limit", "20")
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return Request{Method: "GET", Path: "/api/v1/direct_v2/pending_inbox/", Query: q}
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
