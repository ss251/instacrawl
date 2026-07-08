// Package feed is the Instagram media-feed domain: the post/reel/carousel/
// story/saved/collection model, the pure feed parsers, the best-candidate
// file selector, the classifiers (built on the shared ig envelope gate), and
// the schema-versioned feed cursor with three-way resume. It is pure domain
// logic — it imports ig, reliability, and jsonx but NOT store/media/net/
// http/os, so the orchestration package (internal/feedsync) can wire domain +
// persistence + byte-download without an import cycle.
package feed

// Known media_type values. Unknown types are surfaced and counted as
// SCHEMA·DRIFT, never silently skipped.
const (
	MediaPhoto    = 1
	MediaVideo    = 2
	MediaCarousel = 8
)

// KnownMediaTypes is the closed set the parser recognizes. Anything outside
// it is drift, not a skip.
var KnownMediaTypes = map[int]bool{
	MediaPhoto:    true,
	MediaVideo:    true,
	MediaCarousel: true,
}

// Media is one feed item (post, reel, or carousel). A carousel populates
// Carousel with each child (itself a full Media, recursively parsed);
// RawJSON is always retained — top-level or nested — so a later parser fix
// can re-derive rows without re-hitting the API.
type Media struct {
	PK                 string
	Code               string
	MediaType          int
	ProductType        string // "clips" => reel
	OwnerID            string
	TakenAtUS          int64
	CaptionText        string
	CaptionCreatedAtUS int64
	Files              []MediaFile // best candidate per kind present (see SelectBestCandidate)
	Carousel           []Media     // populated only when MediaType == MediaCarousel
	RawJSON            string
	Source             string
	Known              bool // false => MediaType not in KnownMediaTypes
}

// MediaFile is one downloadable rendition of a Media: the highest-resolution
// candidate of its Kind. URL/Width/Height/Kind/Position are set at parse
// time (see SelectBestCandidate); Hash/LocalPath/Downloaded/Missing are
// zero-valued until the caller actually fetches the bytes (internal/media)
// and fills them in.
type MediaFile struct {
	Kind       string // "image" or "video"
	Position   int    // carousel child index (0 for non-carousel media)
	URL        string
	Width      int
	Height     int
	Hash       string
	LocalPath  string
	Downloaded bool
	Missing    bool
}

// Story is one active story item belonging to a StorySet.
type Story struct {
	Media        Media
	ExpiringAtUS int64
	TakenAtUS    int64
}

// StorySet is one user's set of active stories — Instagram's "reel" of
// ephemeral story items (unrelated to product_type "clips" feed reels). A
// user with no active stories parses to an empty StorySet, which is a normal
// outcome, not drift.
type StorySet struct {
	UserID  string
	Stories []Story
	RawJSON string
}

// SavedItem cross-references a saved post/reel back to the collection it was
// saved into. CollectionID is "" when the wrapping item carries none (the
// general saved feed does not always tag a collection).
type SavedItem struct {
	MediaPK      string
	CollectionID string
}

// LikedItem cross-references one entry of the (usually restricted) liked
// feed back to its media.
type LikedItem struct {
	MediaPK string
}

// Collection is one saved-posts collection (a named, orderable bucket of
// saved media).
type Collection struct {
	ID           string
	Name         string
	Type         string
	CoverMediaPK string
	MediaCount   int
}

// FeedPage is one parsed page shared by ParseUserFeed, ParseSavedFeed,
// ParseCollectionFeed, and ParseLikedFeed. Saved/Liked are populated only by
// the parsers whose endpoint wraps each item ({"media": MEDIA}); ParseUserFeed
// leaves both nil.
type FeedPage struct {
	Media         []Media
	Saved         []SavedItem
	Liked         []LikedItem
	NextMaxID     string
	MoreAvailable bool
	NumResults    int
	RawJSON       string
}

// CollectionsPage is one parsed page of /api/v1/collections/list/. This type
// is not itself named by the field list of FeedPage (whose Media/Saved/Liked
// fields have no room for a collection's own metadata), so ParseCollectionsList
// gets its own page type, mirroring how dm gives InboxPage and ThreadPage
// distinct shapes rather than forcing one shared struct.
type CollectionsPage struct {
	Collections   []Collection
	NextMaxID     string
	MoreAvailable bool
	RawJSON       string
}
