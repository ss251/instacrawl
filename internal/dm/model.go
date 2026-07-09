// Package dm is the direct-message domain: the tagged-union message model, the
// pure inbox/thread parsers, the four-state classifiers (built on the shared
// ig envelope gate), the schema-versioned cursors with three-way resume, and
// the participant reconciler. It is pure domain logic — it imports ig and
// reliability but NOT store, so the orchestration package (internal/dmsync) can
// wire domain + persistence without an import cycle.
package dm

// ItemType discriminates a direct-message item. Unknown types are surfaced and
// counted as SCHEMA·DRIFT, never silently skipped.
type ItemType string

const (
	ItemText          ItemType = "text"
	ItemMedia         ItemType = "media"
	ItemVoice         ItemType = "voice_media"
	ItemMediaShare    ItemType = "media_share"
	ItemReelShare     ItemType = "reel_share"
	ItemStoryShare    ItemType = "story_share"
	ItemClip          ItemType = "clip" // a shared Reel/clip — the most common share in modern DMs
	ItemLink          ItemType = "link"
	ItemLike          ItemType = "like"
	ItemAnimatedMedia ItemType = "animated_media"
	ItemPlaceholder   ItemType = "placeholder"
	ItemActionLog     ItemType = "action_log"
	ItemVideoCall     ItemType = "video_call_event" // a voice/video call log entry
)

// KnownItemTypes is the closed set the parser recognizes. Anything outside it is
// drift, not a skip — that is how a genuinely new type gets surfaced (clip and
// video_call_event were added this way after a live run flagged them).
var KnownItemTypes = map[ItemType]bool{
	ItemText: true, ItemMedia: true, ItemVoice: true, ItemMediaShare: true,
	ItemReelShare: true, ItemStoryShare: true, ItemClip: true, ItemLink: true, ItemLike: true,
	ItemAnimatedMedia: true, ItemPlaceholder: true, ItemActionLog: true, ItemVideoCall: true,
}

// Message is one direct message, a tagged union over ItemType. Only the
// sub-field(s) relevant to the type are populated; RawJSON is always retained so
// a later parser fix can re-derive rows without re-hitting the API.
type Message struct {
	ItemID      string
	ThreadID    string
	UserID      string
	TimestampUS int64
	ItemType    ItemType
	RawItemType string // literal string as seen (drift audit)
	Known       bool   // false → item_type not in KnownItemTypes
	Text        string
	MediaJSON   string // media locator payload (bytes are Phase 3)
	SharedJSON  string // shared post/reel/story payload
	LinkJSON    string
	Reactions   []Reaction
	ReplyTo     *ReplyRef
	RawJSON     string
	Source      string
}

// Reaction is a like/emoji on a message item.
type Reaction struct {
	ItemID      string
	UserID      string
	Emoji       string
	TimestampUS int64
}

// ReplyRef references a parent message that may be older than the current page
// or already unsent; resolved on a later pass.
type ReplyRef struct {
	ParentItemID string
	Resolved     bool
}

// Participant is a thread member's profile. FieldCount drives reconciler tiering
// so an anonymous numeric-ID stub never clobbers a richer stored profile.
type Participant struct {
	UserID        string
	Username      string
	FullName      string
	ProfilePicURL string
	IsAdmin       bool
	FieldCount    int
	Source        string
}

// ThreadEvent is a non-message thread action (rename, add/remove member, …),
// whether it arrives as an action_log item or a thread-level field.
type ThreadEvent struct {
	EventID     string
	ThreadID    string
	Kind        string
	ActorID     string
	TargetID    string
	TimestampUS int64
	RawJSON     string
	Source      string
}

// Thread is a conversation (1:1 or group).
type Thread struct {
	ThreadID       string
	Title          string
	IsGroup        bool
	IsPending      bool
	Participants   []Participant
	AdminIDs       []string
	LastActivityUS int64
	Source         string
}

// InboxPage is one parsed inbox (or pending_inbox) page.
type InboxPage struct {
	Threads    []Thread
	NextCursor string // "" when there are no older pages
	SeqID      string // threaded through every inbox page
}

// ThreadPage is one parsed page of a thread's messages (paginated backwards).
type ThreadPage struct {
	ThreadID     string
	Thread       *Thread // thread metadata when the page carries it
	Messages     []Message
	Events       []ThreadEvent
	Participants []Participant
	OldestCursor string   // "" at the start of the thread (fully backfilled)
	MinTS        int64    // oldest timestamp in this page (unsend window)
	MaxTS        int64    // newest timestamp in this page (unsend window)
	ItemIDs      []string // item_ids present in this page (unsend detection)
	UnknownTypes []string // item_type strings seen that aren't in KnownItemTypes
}
