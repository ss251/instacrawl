package feed

import (
	"fmt"

	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/reliability"
)

// FeedResult is the classified outcome of a user-feed, saved-feed, collection-
// feed, or liked-feed page.
type FeedResult struct {
	Verdict      reliability.Verdict
	Page         *FeedPage // non-nil on OK; also returned on unknown-type drift so callers can retain/count
	ChallengeURL string
	RawBody      string // retained on SCHEMA·DRIFT
	// Unavailable is set by ClassifyLiked only: true when Instagram restricts
	// the endpoint (HTTP 400), a known and permanent outcome that is neither
	// drift nor an error.
	Unavailable bool
}

// StoryResult is the classified outcome of a user's active-stories page.
type StoryResult struct {
	Verdict      reliability.Verdict
	Set          *StorySet // non-nil on OK; also returned on unknown-type drift so callers can retain/count
	ChallengeURL string
	RawBody      string
}

// CollectionsResult is the classified outcome of a collections-list page.
// Not named in the original classifier list, but added for symmetry: every
// other parser here has a matching classifier, and /api/v1/collections/list/
// is no exception to "every fetched response passes the reliability gate
// before being trusted."
type CollectionsResult struct {
	Verdict      reliability.Verdict
	Page         *CollectionsPage // non-nil only on StateOK
	ChallengeURL string
	RawBody      string // retained on SCHEMA·DRIFT
}

// storyMedia flattens a StorySet's stories to their underlying Media, so
// UnknownMediaTypes can scan a story page the same way it scans a feed page.
func storyMedia(stories []Story) []Media {
	out := make([]Media, len(stories))
	for i, s := range stories {
		out[i] = s.Media
	}
	return out
}

// ClassifyUserFeed runs the shared envelope gate, then parses a user's own
// posts/reels page. An unknown media_type is drift (counted, never skipped);
// the parsed page is still returned so the caller can retain the raw and
// report the counts.
func ClassifyUserFeed(resp ig.RawResponse, floor reliability.Floor) FeedResult {
	v, challengeURL, terminal := ig.ClassifyEnvelope(resp)
	if terminal {
		out := FeedResult{Verdict: v, ChallengeURL: challengeURL}
		if v.State == reliability.StateSchemaDrift {
			out.RawBody = string(resp.Body)
		}
		return out
	}
	page, err := ParseUserFeed(resp.Body)
	if err != nil {
		return FeedResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift, "user feed: "+err.Error()),
			RawBody: string(resp.Body),
		}
	}
	if unknown := UnknownMediaTypes(page.Media); len(unknown) > 0 {
		return FeedResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift,
				fmt.Sprintf("user feed: %d unknown media_type(s) %v — retaining raw, not skipping",
					len(unknown), unknown)),
			Page:    page,
			RawBody: string(resp.Body),
		}
	}
	return FeedResult{Verdict: floor.Apply(reliability.OK(len(page.Media))), Page: page}
}

// ClassifySavedFeed runs the shared envelope gate, then parses the saved
// posts page. An unknown media_type is drift; the parsed page is still
// returned so the caller can retain the raw and report the counts.
func ClassifySavedFeed(resp ig.RawResponse, floor reliability.Floor) FeedResult {
	v, challengeURL, terminal := ig.ClassifyEnvelope(resp)
	if terminal {
		out := FeedResult{Verdict: v, ChallengeURL: challengeURL}
		if v.State == reliability.StateSchemaDrift {
			out.RawBody = string(resp.Body)
		}
		return out
	}
	page, err := ParseSavedFeed(resp.Body)
	if err != nil {
		return FeedResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift, "saved feed: "+err.Error()),
			RawBody: string(resp.Body),
		}
	}
	if unknown := UnknownMediaTypes(page.Media); len(unknown) > 0 {
		return FeedResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift,
				fmt.Sprintf("saved feed: %d unknown media_type(s) %v — retaining raw, not skipping",
					len(unknown), unknown)),
			Page:    page,
			RawBody: string(resp.Body),
		}
	}
	return FeedResult{Verdict: floor.Apply(reliability.OK(len(page.Media))), Page: page}
}

// ClassifyCollectionFeed runs the shared envelope gate, then parses one
// collection's feed page. collectionID (from the request URL) is stamped
// onto every resulting SavedItem — see ParseCollectionFeed.
func ClassifyCollectionFeed(resp ig.RawResponse, floor reliability.Floor, collectionID string) FeedResult {
	v, challengeURL, terminal := ig.ClassifyEnvelope(resp)
	if terminal {
		out := FeedResult{Verdict: v, ChallengeURL: challengeURL}
		if v.State == reliability.StateSchemaDrift {
			out.RawBody = string(resp.Body)
		}
		return out
	}
	page, err := ParseCollectionFeed(resp.Body, collectionID)
	if err != nil {
		return FeedResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift, "collection feed: "+err.Error()),
			RawBody: string(resp.Body),
		}
	}
	if unknown := UnknownMediaTypes(page.Media); len(unknown) > 0 {
		return FeedResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift,
				fmt.Sprintf("collection %s feed: %d unknown media_type(s) %v — retaining raw, not skipping",
					collectionID, len(unknown), unknown)),
			Page:    page,
			RawBody: string(resp.Body),
		}
	}
	return FeedResult{Verdict: floor.Apply(reliability.OK(len(page.Media))), Page: page}
}

// ClassifyCollectionsList runs the shared envelope gate, then parses the
// collections list page.
func ClassifyCollectionsList(resp ig.RawResponse, floor reliability.Floor) CollectionsResult {
	v, challengeURL, terminal := ig.ClassifyEnvelope(resp)
	if terminal {
		out := CollectionsResult{Verdict: v, ChallengeURL: challengeURL}
		if v.State == reliability.StateSchemaDrift {
			out.RawBody = string(resp.Body)
		}
		return out
	}
	page, err := ParseCollectionsList(resp.Body)
	if err != nil {
		return CollectionsResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift, "collections list: "+err.Error()),
			RawBody: string(resp.Body),
		}
	}
	return CollectionsResult{Verdict: floor.Apply(reliability.OK(len(page.Collections))), Page: page}
}

// ClassifyStory runs the shared envelope gate, then parses a user's active
// stories. A null/absent reel parses to an empty StorySet, a normal outcome:
// floor.Apply already knows not to penalize a resource with no known history
// for legitimately coming back empty, so no special-casing is needed here.
func ClassifyStory(resp ig.RawResponse, floor reliability.Floor, userID string) StoryResult {
	v, challengeURL, terminal := ig.ClassifyEnvelope(resp)
	if terminal {
		out := StoryResult{Verdict: v, ChallengeURL: challengeURL}
		if v.State == reliability.StateSchemaDrift {
			out.RawBody = string(resp.Body)
		}
		return out
	}
	set, err := ParseUserStory(resp.Body, userID)
	if err != nil {
		return StoryResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift, "user story: "+err.Error()),
			RawBody: string(resp.Body),
		}
	}
	if unknown := UnknownMediaTypes(storyMedia(set.Stories)); len(unknown) > 0 {
		return StoryResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift,
				fmt.Sprintf("user %s story: %d unknown media_type(s) %v — retaining raw, not skipping",
					userID, len(unknown), unknown)),
			Set:     set,
			RawBody: string(resp.Body),
		}
	}
	return StoryResult{Verdict: floor.Apply(reliability.OK(len(set.Stories))), Set: set}
}

// ClassifyLiked runs the shared envelope gate, then parses the (usually
// restricted) liked feed. Instagram serves HTTP 400 for this endpoint on
// nearly every account (LIVE-VALIDATED 2026-07-09) — a known, permanent
// restriction, not a schema drift and not an error — so it is caught here
// directly, before the envelope gate, and reported as a distinct best-effort
// outcome: Unavailable is true and Verdict stays OK(0), bypassing floor
// entirely (an unavailable resource is not the same thing as an empty one,
// so it must never be escalated to drift no matter how many runs in a row
// see it). Any other status runs the normal envelope+parse+floor path.
func ClassifyLiked(resp ig.RawResponse, floor reliability.Floor) FeedResult {
	if resp.Status == 400 {
		return FeedResult{Verdict: reliability.OK(0), Unavailable: true}
	}
	v, challengeURL, terminal := ig.ClassifyEnvelope(resp)
	if terminal {
		out := FeedResult{Verdict: v, ChallengeURL: challengeURL}
		if v.State == reliability.StateSchemaDrift {
			out.RawBody = string(resp.Body)
		}
		return out
	}
	page, err := ParseLikedFeed(resp.Body)
	if err != nil {
		return FeedResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift, "liked feed: "+err.Error()),
			RawBody: string(resp.Body),
		}
	}
	if unknown := UnknownMediaTypes(page.Media); len(unknown) > 0 {
		return FeedResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift,
				fmt.Sprintf("liked feed: %d unknown media_type(s) %v — retaining raw, not skipping",
					len(unknown), unknown)),
			Page:    page,
			RawBody: string(resp.Body),
		}
	}
	return FeedResult{Verdict: floor.Apply(reliability.OK(len(page.Media))), Page: page}
}
