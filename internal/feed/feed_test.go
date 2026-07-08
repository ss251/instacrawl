package feed

import (
	"testing"

	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/reliability"
)

// TestParseUserFeedCarouselAndUnknownType covers a single user-feed page
// carrying a photo, a reel, a 2-child carousel, and a from-the-future
// media_type — all dummy data, no real Instagram content.
func TestParseUserFeedCarouselAndUnknownType(t *testing.T) {
	body := []byte(`{
	  "items": [
	    {"pk":"1001","code":"PHOTO1","media_type":1,"taken_at":1700000001,"product_type":"feed",
	     "caption":{"text":"a lovely photo","created_at":1700000000},"user":{"pk":"500"},
	     "image_versions2":{"candidates":[
	       {"url":"https://example.test/photo-small.jpg","width":320,"height":320},
	       {"url":"https://example.test/photo-large.jpg","width":1080,"height":1080}
	     ]}},
	    {"pk":"1002","code":"REEL1","media_type":2,"taken_at":1700000010,"product_type":"clips",
	     "caption":null,"user":{"pk":"500"},
	     "video_versions":[
	       {"url":"https://example.test/reel-360.mp4","width":360,"height":640},
	       {"url":"https://example.test/reel-1080.mp4","width":1080,"height":1920}
	     ]},
	    {"pk":"1003","code":"CAROUSEL1","media_type":8,"taken_at":1700000020,"product_type":"carousel_container",
	     "caption":{"text":"carousel caption","created_at":1700000019},"user":{"pk":"500"},
	     "carousel_media":[
	       {"pk":"1003_1","code":"CAROUSEL1_CHILD1","media_type":1,"taken_at":1700000020,
	        "image_versions2":{"candidates":[
	          {"url":"https://example.test/c1-small.jpg","width":200,"height":200},
	          {"url":"https://example.test/c1-large.jpg","width":800,"height":800}
	        ]}},
	       {"pk":"1003_2","code":"CAROUSEL1_CHILD2","media_type":1,"taken_at":1700000020,
	        "image_versions2":{"candidates":[
	          {"url":"https://example.test/c2-only.jpg","width":640,"height":640}
	        ]}}
	     ]},
	    {"pk":"1004","code":"FUTURE1","media_type":99,"taken_at":1700000030,"product_type":"future_type","user":{"pk":"500"}}
	  ],
	  "num_results":4,"more_available":true,"next_max_id":"cursor_abc123"
	}`)
	page, err := ParseUserFeed(body)
	if err != nil {
		t.Fatal(err)
	}
	if page.NumResults != 4 || !page.MoreAvailable || page.NextMaxID != "cursor_abc123" {
		t.Fatalf("page meta wrong: %+v", page)
	}
	if len(page.Media) != 4 {
		t.Fatalf("expected 4 media, got %d", len(page.Media))
	}

	// Photo: the larger of two image candidates wins.
	photo := page.Media[0]
	if photo.PK != "1001" || photo.OwnerID != "500" || photo.CaptionText != "a lovely photo" || !photo.Known {
		t.Fatalf("photo fields wrong: %+v", photo)
	}
	if len(photo.Files) != 1 || photo.Files[0].Kind != "image" || photo.Files[0].Width != 1080 || photo.Files[0].URL != "https://example.test/photo-large.jpg" {
		t.Fatalf("photo did not pick the larger candidate: %+v", photo.Files)
	}

	// Reel: the larger of two video versions wins; caption:null must not error.
	reel := page.Media[1]
	if reel.ProductType != "clips" || reel.CaptionText != "" || !reel.Known {
		t.Fatalf("reel fields wrong: %+v", reel)
	}
	if len(reel.Files) != 1 || reel.Files[0].Kind != "video" || reel.Files[0].Width != 1080 || reel.Files[0].Height != 1920 {
		t.Fatalf("reel did not pick the larger video version: %+v", reel.Files)
	}

	// Carousel: both children parsed; SelectBestCandidate flattens per-child
	// best files, tagged with the child's Position.
	carousel := page.Media[2]
	if !carousel.Known || len(carousel.Carousel) != 2 {
		t.Fatalf("carousel children not parsed: %+v", carousel)
	}
	if carousel.Carousel[0].PK != "1003_1" || carousel.Carousel[1].PK != "1003_2" {
		t.Fatalf("carousel children out of order: %+v", carousel.Carousel)
	}
	files := SelectBestCandidate(carousel)
	if len(files) != 2 {
		t.Fatalf("expected 2 flattened carousel files, got %d", len(files))
	}
	if files[0].URL != "https://example.test/c1-large.jpg" || files[0].Position != 0 {
		t.Fatalf("carousel child 0 did not pick its larger candidate: %+v", files[0])
	}
	if files[1].URL != "https://example.test/c2-only.jpg" || files[1].Position != 1 {
		t.Fatalf("carousel child 1 wrong: %+v", files[1])
	}

	// Unknown media_type is surfaced, never silently skipped.
	future := page.Media[3]
	if future.Known {
		t.Fatal("media_type 99 is not in KnownMediaTypes and must not be marked Known")
	}
	unknown := UnknownMediaTypes(page.Media)
	if len(unknown) != 1 || unknown[0] != 99 {
		t.Fatalf("unknown media_type not surfaced: %v", unknown)
	}
}

// TestParseSavedFeedUnwrapsMedia covers the {items:[{media:MEDIA}]} wrapper
// shape shared by saved posts, a collection's feed, and the liked feed.
func TestParseSavedFeedUnwrapsMedia(t *testing.T) {
	body := []byte(`{
	  "items":[
	    {"media":{"pk":"2001","code":"SAVED1","media_type":1,"taken_at":1700001000,
	       "image_versions2":{"candidates":[{"url":"https://example.test/saved1.jpg","width":500,"height":500}]}},
	     "collection_id":"9001"},
	    {"media":{"pk":"2002","code":"SAVED2","media_type":2,"taken_at":1700001010,
	       "video_versions":[{"url":"https://example.test/saved2.mp4","width":720,"height":1280}]}}
	  ],
	  "more_available":false,"next_max_id":""
	}`)
	page, err := ParseSavedFeed(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Media) != 2 || len(page.Saved) != 2 || page.NumResults != 2 {
		t.Fatalf("saved feed not unwrapped: %+v", page)
	}
	if page.Saved[0].MediaPK != "2001" || page.Saved[0].CollectionID != "9001" {
		t.Fatalf("saved item 0 wrong: %+v", page.Saved[0])
	}
	if page.Saved[1].MediaPK != "2002" || page.Saved[1].CollectionID != "" {
		t.Fatalf("saved item 1 (no collection_id) wrong: %+v", page.Saved[1])
	}
}

// TestParseCollectionsList covers /api/v1/collections/list/, including a
// collection with no cover_media.
func TestParseCollectionsList(t *testing.T) {
	body := []byte(`{
	  "items":[
	    {"collection_id":"9001","collection_name":"Recipes","collection_type":"custom",
	     "cover_media":{"pk":"2001"},"collection_media_count":12},
	    {"collection_id":"9002","collection_name":"All Posts","collection_type":"default",
	     "collection_media_count":0}
	  ],
	  "more_available":true,"next_max_id":"coll_cursor_1"
	}`)
	page, err := ParseCollectionsList(body)
	if err != nil {
		t.Fatal(err)
	}
	if !page.MoreAvailable || page.NextMaxID != "coll_cursor_1" || len(page.Collections) != 2 {
		t.Fatalf("collections page meta wrong: %+v", page)
	}
	if c := page.Collections[0]; c.ID != "9001" || c.Name != "Recipes" || c.Type != "custom" || c.CoverMediaPK != "2001" || c.MediaCount != 12 {
		t.Fatalf("collection 0 wrong: %+v", c)
	}
	if c := page.Collections[1]; c.ID != "9002" || c.CoverMediaPK != "" || c.MediaCount != 0 {
		t.Fatalf("collection 1 (no cover_media) wrong: %+v", c)
	}
}

// TestParseUserStoryReelAndNull covers both a populated reel and Instagram's
// reel:null shape (a user with no active story) — the latter is a normal
// empty StorySet, not drift.
func TestParseUserStoryReelAndNull(t *testing.T) {
	body := []byte(`{"reel":{"items":[
	  {"pk":"3001","code":"STORY1","media_type":1,"taken_at":1700002000,"expiring_at":1700002100,
	   "image_versions2":{"candidates":[{"url":"https://example.test/story1.jpg","width":1080,"height":1920}]}}
	]}}`)
	set, err := ParseUserStory(body, "500")
	if err != nil {
		t.Fatal(err)
	}
	if set.UserID != "500" || len(set.Stories) != 1 {
		t.Fatalf("story set wrong: %+v", set)
	}
	if s := set.Stories[0]; s.Media.PK != "3001" || s.ExpiringAtUS != 1700002100 || s.TakenAtUS != 1700002000 {
		t.Fatalf("story item wrong: %+v", s)
	}

	nullBody := []byte(`{"reel":null}`)
	emptySet, err := ParseUserStory(nullBody, "500")
	if err != nil {
		t.Fatal(err)
	}
	if emptySet.UserID != "500" || len(emptySet.Stories) != 0 {
		t.Fatalf("reel:null must parse to an empty (not error) StorySet: %+v", emptySet)
	}
}

// TestResolveResume covers the three-way resume classification: no valid
// cursor, an unchanged head, and a grown head.
func TestResolveResume(t *testing.T) {
	// No valid cursor → full re-crawl.
	if got := ResolveResume(FeedCursor{}, false, "500"); got != Invalidated {
		t.Fatalf("no cursor → %s, want invalidated", got)
	}
	// Feed head unchanged since what we stored → skip.
	prev := FeedCursor{SchemaVersion: FeedCursorSchemaVersion, NewestPK: "500"}
	if got := ResolveResume(prev, true, "500"); got != Unchanged {
		t.Fatalf("same head → %s, want unchanged", got)
	}
	// Feed head moved → fetch down to the last-stored media.
	if got := ResolveResume(prev, true, "900"); got != Grew {
		t.Fatalf("new head → %s, want grew", got)
	}
}

// TestFeedCursorSchemaMismatchInvalidates covers cursor version bumping:
// a stale-version cursor must always decode ok=false.
func TestFeedCursorSchemaMismatchInvalidates(t *testing.T) {
	stale := FeedCursor{SchemaVersion: 0, MaxID: "x"}
	if _, ok := DecodeFeedCursor(stale.Encode()); ok {
		t.Fatal("stale-version cursor must decode ok=false (→ invalidated)")
	}
	cur := FeedCursor{SchemaVersion: FeedCursorSchemaVersion, MaxID: "abc", NewestPK: "700"}
	got, ok := DecodeFeedCursor(cur.Encode())
	if !ok || got.MaxID != "abc" || got.NewestPK != "700" {
		t.Fatalf("round-trip failed: ok=%v got=%+v", ok, got)
	}
	if _, ok := DecodeFeedCursor(""); ok {
		t.Fatal("empty cursor must decode ok=false")
	}
}

// TestClassifyLiked covers the special HTTP 400 (restricted) outcome and the
// normal 200 path, proving Unavailable is set only for the former.
func TestClassifyLiked(t *testing.T) {
	restricted := ig.RawResponse{Status: 400, Body: []byte(`{"message":"restricted","status":"fail"}`)}
	result := ClassifyLiked(restricted, reliability.Floor{})
	if !result.Unavailable {
		t.Fatal("HTTP 400 on liked feed must set Unavailable")
	}
	if result.Verdict.State != reliability.StateOK || result.Verdict.N != 0 {
		t.Fatalf("liked-restricted must stay OK(0), not drift/error: %+v", result.Verdict)
	}
	if result.Page != nil {
		t.Fatal("restricted liked feed must not carry a page")
	}

	body := []byte(`{"items":[{"media":{"pk":"5001","code":"LIKED1","media_type":1,"taken_at":1700003000,
	  "image_versions2":{"candidates":[{"url":"https://example.test/liked1.jpg","width":400,"height":400}]}}}],
	  "more_available":false,"next_max_id":""}`)
	ok := ig.RawResponse{Status: 200, Body: body}
	result = ClassifyLiked(ok, reliability.Floor{})
	if result.Unavailable {
		t.Fatal("a normal 200 liked feed must not be marked Unavailable")
	}
	if result.Verdict.State != reliability.StateOK || result.Verdict.N != 1 {
		t.Fatalf("expected OK(1), got %+v", result.Verdict)
	}
	if result.Page == nil || len(result.Page.Liked) != 1 || result.Page.Liked[0].MediaPK != "5001" {
		t.Fatalf("liked item not parsed: %+v", result.Page)
	}
}

// TestMediaFieldCountAndTier covers the media upsert tie-break: richness
// first, then recency, then lane order.
func TestMediaFieldCountAndTier(t *testing.T) {
	if MediaFieldCount("", "", false) != 0 {
		t.Fatal("empty media → 0")
	}
	if MediaFieldCount("ABC123", "hello", true) != 3 {
		t.Fatal("full media → 3")
	}

	rich := Media{PK: "1", Code: "ABC", CaptionText: "hi", Files: []MediaFile{{Kind: "image"}}, Source: "web"}
	stub := Media{PK: "1", Source: "web"}
	if got := MediaTier(rich, stub, true); got.Code != "ABC" {
		t.Fatal("a bare stub must never clobber a richer media row, even if newer")
	}
	if got := MediaTier(stub, rich, true); got.Code != "ABC" {
		t.Fatal("a richer media row must win over a stub")
	}

	// Equal field count → newer capture wins.
	a := Media{Code: "A", Source: "web"}
	b := Media{Code: "B", Source: "web"}
	if got := MediaTier(a, b, true); got.Code != "B" {
		t.Fatal("equal field count → newer capture wins")
	}

	// Equal field count, same recency → higher lane wins.
	c := Media{Code: "C", Source: "mobile"}
	d := Media{Code: "D", Source: "web"}
	if got := MediaTier(c, d, false); got.Code != "D" {
		t.Fatal("equal field count, same recency → higher lane (web) wins")
	}
}
