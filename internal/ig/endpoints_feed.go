package ig

import (
	"net/url"
	"strconv"
)

// ==========================================================================
// Feed endpoint builders. LIVE-VALIDATED 2026-07-09 (schema-only probe against
// a logged-in session): own feed /api/v1/feed/user/{id}/, saved
// /api/v1/feed/saved/posts/, collections /api/v1/collections/list/, per-collection
// /api/v1/feed/collection/{id}/posts/, and own stories /api/v1/feed/user/{id}/story/
// all return the expected shapes; /api/v1/feed/liked/ returns 400 (restricted).
// Pagination is uniform: max_id query param + response next_max_id + more_available.
// Builders take PRIMITIVES so this package stays free of any cycle with internal/feed.
// ==========================================================================

func withMaxID(q url.Values, maxID string) url.Values {
	if maxID != "" {
		q.Set("max_id", maxID)
	}
	return q
}

// UserFeedRequest builds one page of a user's own posts/reels.
func UserFeedRequest(dsUserID, maxID string, count int) Request {
	q := url.Values{}
	if count <= 0 {
		count = 12
	}
	q.Set("count", strconv.Itoa(count))
	return Request{Method: "GET", Path: "/api/v1/feed/user/" + url.PathEscape(dsUserID) + "/", Query: withMaxID(q, maxID)}
}

// SavedFeedRequest builds one page of the saved-posts feed.
func SavedFeedRequest(maxID string) Request {
	return Request{Method: "GET", Path: "/api/v1/feed/saved/posts/", Query: withMaxID(url.Values{}, maxID)}
}

// CollectionsListRequest builds one page of the saved-collections list.
func CollectionsListRequest(maxID string) Request {
	return Request{Method: "GET", Path: "/api/v1/collections/list/", Query: withMaxID(url.Values{}, maxID)}
}

// CollectionFeedRequest builds one page of a single collection's feed.
// LIVE-VALIDATED 2026-07-09: the working path is /posts/ (the bare
// /feed/collection/{id}/ returns 400, like /feed/saved/ did).
func CollectionFeedRequest(collectionID, maxID string) Request {
	return Request{Method: "GET", Path: "/api/v1/feed/collection/" + url.PathEscape(collectionID) + "/posts/", Query: withMaxID(url.Values{}, maxID)}
}

// LikedFeedRequest builds one page of the (usually restricted) liked feed.
func LikedFeedRequest(maxID string) Request {
	return Request{Method: "GET", Path: "/api/v1/feed/liked/", Query: withMaxID(url.Values{}, maxID)}
}

// UserStoryRequest builds the request for a user's currently-live stories (24h).
func UserStoryRequest(dsUserID string) Request {
	return Request{Method: "GET", Path: "/api/v1/feed/user/" + url.PathEscape(dsUserID) + "/story/", Query: url.Values{}}
}
