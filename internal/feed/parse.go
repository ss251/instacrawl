package feed

import (
	"encoding/json"
	"fmt"

	"github.com/ss251/instacrawl/internal/jsonx"
)

// The JSON field names/nesting below were LIVE-VALIDATED 2026-07-09 against a
// logged-in session (schema-only probe): own posts/reels (items, num_results,
// more_available, next_max_id), media (pk, code, media_type, taken_at,
// product_type, caption.text/created_at, image_versions2.candidates,
// video_versions, carousel_media), saved posts, collections list, and story
// (reel.items[]/null) all match. The collection-feed shape
// (/api/v1/feed/collection/{id}/) has NOT itself been probed — it is ASSUMED
// to mirror the saved feed's {items:[{media:MEDIA}]} wrapper, and a media's
// owner (user.pk) sub-field likewise awaits a probe with the field present.
// Everything is parsed LENIENTLY — a bad/renamed field degrades to zero
// rather than failing the whole batch — and RawJSON is retained, so a future
// drift can never lose a historical page.

type captionJSON struct {
	Text      string    `json:"text"`
	CreatedAt jsonx.I64 `json:"created_at"`
}

type candidateJSON struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type videoVersionJSON struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type mediaJSON struct {
	PK          jsonx.Str    `json:"pk"`
	Code        string       `json:"code"`
	MediaType   int          `json:"media_type"`
	TakenAt     jsonx.I64    `json:"taken_at"`
	ProductType string       `json:"product_type"`
	Caption     *captionJSON `json:"caption"`
	User        *struct {
		PK jsonx.Str `json:"pk"`
	} `json:"user"`
	ImageVersions2 *struct {
		Candidates []candidateJSON `json:"candidates"`
	} `json:"image_versions2"`
	VideoVersions []videoVersionJSON `json:"video_versions"`
	CarouselMedia []json.RawMessage  `json:"carousel_media"`
}

type mediaWrapperJSON struct {
	Media        json.RawMessage `json:"media"`
	CollectionID jsonx.Str       `json:"collection_id"`
}

// bestCandidate returns the image candidate with the largest width*height
// area.
func bestCandidate(cands []candidateJSON) (candidateJSON, bool) {
	if len(cands) == 0 {
		return candidateJSON{}, false
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if c.Width*c.Height > best.Width*best.Height {
			best = c
		}
	}
	return best, true
}

// bestVideoVersion returns the video version with the largest width*height
// area.
func bestVideoVersion(vs []videoVersionJSON) (videoVersionJSON, bool) {
	if len(vs) == 0 {
		return videoVersionJSON{}, false
	}
	best := vs[0]
	for _, v := range vs[1:] {
		if v.Width*v.Height > best.Width*best.Height {
			best = v
		}
	}
	return best, true
}

// bestFilesFrom picks the single highest-resolution image candidate (the
// photo itself, or a video's poster thumbnail) and the single
// highest-resolution video version (video/reel media only), so a photo
// yields one file and a video yields up to two.
func bestFilesFrom(mj mediaJSON) []MediaFile {
	var out []MediaFile
	if mj.ImageVersions2 != nil {
		if best, ok := bestCandidate(mj.ImageVersions2.Candidates); ok {
			out = append(out, MediaFile{Kind: "image", URL: best.URL, Width: best.Width, Height: best.Height})
		}
	}
	if best, ok := bestVideoVersion(mj.VideoVersions); ok {
		out = append(out, MediaFile{Kind: "video", URL: best.URL, Width: best.Width, Height: best.Height})
	}
	return out
}

// mediaFrom converts one media payload into a Media, recursively parsing
// carousel_media into full child Media (each retaining its own raw bytes).
// raw is the exact bytes of this media, retained verbatim.
func mediaFrom(mj mediaJSON, raw json.RawMessage, source string) Media {
	m := Media{
		PK:          string(mj.PK),
		Code:        mj.Code,
		MediaType:   mj.MediaType,
		ProductType: mj.ProductType,
		TakenAtUS:   int64(mj.TakenAt),
		Files:       bestFilesFrom(mj),
		RawJSON:     string(raw),
		Source:      source,
		Known:       KnownMediaTypes[mj.MediaType],
	}
	if mj.User != nil {
		m.OwnerID = string(mj.User.PK)
	}
	if mj.Caption != nil {
		m.CaptionText = mj.Caption.Text
		m.CaptionCreatedAtUS = int64(mj.Caption.CreatedAt)
	}
	for _, rawChild := range mj.CarouselMedia {
		var cj mediaJSON
		_ = json.Unmarshal(rawChild, &cj) // lenient: a bad carousel child degrades, never fails the parent
		m.Carousel = append(m.Carousel, mediaFrom(cj, rawChild, source))
	}
	return m
}

// SelectBestCandidate returns the file(s) to actually download for m: its own
// Files (already narrowed to the best image/video candidate at parse time)
// for a non-carousel Media, or — for a carousel — the flattened per-child
// best files, each tagged with its child's Position (index within Carousel).
// Hash/LocalPath/Downloaded/Missing stay at their zero values; the caller
// fills them in once internal/media has actually fetched the bytes.
func SelectBestCandidate(m Media) []MediaFile {
	if len(m.Carousel) == 0 {
		return m.Files
	}
	var out []MediaFile
	for i, child := range m.Carousel {
		for _, f := range SelectBestCandidate(child) {
			f.Position = i
			out = append(out, f)
		}
	}
	return out
}

// UnknownMediaTypes walks media (recursively through carousels) and returns
// the distinct media_type values not in KnownMediaTypes, in first-seen order.
// Classifiers use this to detect drift without FeedPage itself needing an
// UnknownTypes field.
func UnknownMediaTypes(media []Media) []int {
	seen := map[int]bool{}
	var out []int
	var walk func([]Media)
	walk = func(ms []Media) {
		for _, m := range ms {
			if !m.Known && !seen[m.MediaType] {
				seen[m.MediaType] = true
				out = append(out, m.MediaType)
			}
			if len(m.Carousel) > 0 {
				walk(m.Carousel)
			}
		}
	}
	walk(media)
	return out
}

// ParseUserFeed parses one page of a user's own posts/reels
// (/api/v1/feed/user/{id}/): {items:[MEDIA], num_results, more_available,
// next_max_id}.
func ParseUserFeed(body []byte) (*FeedPage, error) {
	var env struct {
		Items         []json.RawMessage `json:"items"`
		NumResults    int               `json:"num_results"`
		MoreAvailable bool              `json:"more_available"`
		NextMaxID     string            `json:"next_max_id"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode user feed: %w", err)
	}
	page := &FeedPage{
		NumResults:    env.NumResults,
		MoreAvailable: env.MoreAvailable,
		NextMaxID:     env.NextMaxID,
		RawJSON:       string(body),
	}
	for _, raw := range env.Items {
		var mj mediaJSON
		_ = json.Unmarshal(raw, &mj) // lenient: a bad item degrades, never fails the page
		page.Media = append(page.Media, mediaFrom(mj, raw, "web"))
	}
	return page, nil
}

// parseWrappedFeed backs every endpoint whose items wrap media as
// {"media": MEDIA}: saved posts, a single collection's feed, and the liked
// feed. collectionID, when non-empty, is authoritative (the caller already
// knows it from the request URL) and overrides any per-item collection_id;
// when empty, a per-item collection_id (if present) is used instead.
func parseWrappedFeed(body []byte, source, collectionID string) (*FeedPage, error) {
	var env struct {
		Items         []mediaWrapperJSON `json:"items"`
		MoreAvailable bool               `json:"more_available"`
		NextMaxID     string             `json:"next_max_id"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode wrapped feed: %w", err)
	}
	page := &FeedPage{
		MoreAvailable: env.MoreAvailable,
		NextMaxID:     env.NextMaxID,
		RawJSON:       string(body),
	}
	for _, item := range env.Items {
		var mj mediaJSON
		_ = json.Unmarshal(item.Media, &mj) // lenient: a bad item degrades, never fails the page
		m := mediaFrom(mj, item.Media, source)
		page.Media = append(page.Media, m)
		cid := collectionID
		if cid == "" {
			cid = string(item.CollectionID)
		}
		page.Saved = append(page.Saved, SavedItem{MediaPK: m.PK, CollectionID: cid})
	}
	page.NumResults = len(page.Media)
	return page, nil
}

// ParseSavedFeed parses one page of saved posts
// (/api/v1/feed/saved/posts/): {items:[{media:MEDIA}], next_max_id,
// more_available}.
func ParseSavedFeed(body []byte) (*FeedPage, error) {
	return parseWrappedFeed(body, "web", "")
}

// ParseCollectionFeed parses one page of a single collection's contents
// (/api/v1/feed/collection/{id}/). The shape is ASSUMED to mirror the saved
// feed's {items:[{media:MEDIA}]} wrapper — it has not itself been
// live-validated. collectionID (from the request URL) is stamped onto every
// resulting SavedItem.
func ParseCollectionFeed(body []byte, collectionID string) (*FeedPage, error) {
	return parseWrappedFeed(body, "web", collectionID)
}

// ParseLikedFeed parses one page of the (usually restricted) liked feed
// (/api/v1/feed/liked/), assumed to share the same {items:[{media:MEDIA}]}
// wrapper shape as saved posts.
func ParseLikedFeed(body []byte) (*FeedPage, error) {
	page, err := parseWrappedFeed(body, "web", "")
	if err != nil {
		return nil, err
	}
	for _, s := range page.Saved {
		page.Liked = append(page.Liked, LikedItem{MediaPK: s.MediaPK})
	}
	page.Saved = nil
	return page, nil
}

// ParseCollectionsList parses one page of /api/v1/collections/list/:
// {items:[{collection_id, collection_name, collection_type, cover_media,
// collection_media_count}], next_max_id, more_available}.
func ParseCollectionsList(body []byte) (*CollectionsPage, error) {
	var env struct {
		Items []struct {
			CollectionID   jsonx.Str       `json:"collection_id"`
			CollectionName string          `json:"collection_name"`
			CollectionType string          `json:"collection_type"`
			CoverMedia     json.RawMessage `json:"cover_media"`
			MediaCount     int             `json:"collection_media_count"`
		} `json:"items"`
		MoreAvailable bool   `json:"more_available"`
		NextMaxID     string `json:"next_max_id"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode collections list: %w", err)
	}
	page := &CollectionsPage{
		MoreAvailable: env.MoreAvailable,
		NextMaxID:     env.NextMaxID,
		RawJSON:       string(body),
	}
	for _, it := range env.Items {
		var coverPK jsonx.Str
		if len(it.CoverMedia) > 0 {
			var cover struct {
				PK jsonx.Str `json:"pk"`
			}
			_ = json.Unmarshal(it.CoverMedia, &cover) // lenient: a bad cover degrades to "", never fails the page
			coverPK = cover.PK
		}
		page.Collections = append(page.Collections, Collection{
			ID:           string(it.CollectionID),
			Name:         it.CollectionName,
			Type:         it.CollectionType,
			CoverMediaPK: string(coverPK),
			MediaCount:   it.MediaCount,
		})
	}
	return page, nil
}

// ParseUserStory parses a user's active stories
// (/api/v1/feed/user/{id}/story/): {reel:{items:[MEDIA]}|null, ...}. A null
// (or absent) reel yields an empty StorySet — most users simply have no
// active story at any given moment, so this is a normal outcome, not drift.
// userID (from the request URL) is stamped onto the result since the reel
// payload itself was not live-validated to carry it.
func ParseUserStory(body []byte, userID string) (*StorySet, error) {
	var env struct {
		Reel *struct {
			Items []json.RawMessage `json:"items"`
		} `json:"reel"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode user story: %w", err)
	}
	set := &StorySet{UserID: userID, RawJSON: string(body)}
	if env.Reel == nil {
		return set, nil
	}
	for _, raw := range env.Reel.Items {
		var sj struct {
			mediaJSON
			ExpiringAt jsonx.I64 `json:"expiring_at"`
		}
		_ = json.Unmarshal(raw, &sj) // lenient: a bad story item degrades, never fails the set
		m := mediaFrom(sj.mediaJSON, raw, "web")
		set.Stories = append(set.Stories, Story{
			Media:        m,
			ExpiringAtUS: int64(sj.ExpiringAt),
			TakenAtUS:    m.TakenAtUS,
		})
	}
	return set, nil
}
