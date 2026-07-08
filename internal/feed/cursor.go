package feed

import "encoding/json"

// FeedCursorSchemaVersion versions the ENCODING of the cursor value stored
// alongside each feed resource (a user's own feed, the saved feed, a given
// collection, ...). It is distinct from store.SchemaVersion (the table DDL
// version). Bumping it auto-invalidates every stored cursor (they decode to
// a mismatched version → full re-crawl), which is the cursor-schema-drift
// trigger the reliability spine calls for.
const FeedCursorSchemaVersion = 1

// FeedCursor is the per-resource resume state, JSON-encoded into the cursor
// column. Carrying ConsecutiveEmpty here means one cursor row holds all
// resume state — no second lookup for the dead-man's-switch floor.
type FeedCursor struct {
	SchemaVersion    int    `json:"v"`
	MaxID            string `json:"max_id"`    // backward-pagination resume point (next_max_id)
	NewestPK         string `json:"newest"`    // "grew" detection anchor
	NewestTakenAtUS  int64  `json:"newest_ts"` // newest media we've stored
	Backfilled       bool   `json:"backfilled"`
	ConsecutiveEmpty int    `json:"empty"`
}

// Encode marshals the cursor for storage.
func (c FeedCursor) Encode() string {
	b, _ := json.Marshal(c)
	return string(b)
}

// DecodeFeedCursor parses a stored cursor. ok is false when the value is
// absent, unparseable, or a mismatched schema version — all of which mean
// the resource must be treated as invalidated (full re-crawl).
func DecodeFeedCursor(s string) (FeedCursor, bool) {
	if s == "" {
		return FeedCursor{}, false
	}
	var c FeedCursor
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return FeedCursor{}, false
	}
	if c.SchemaVersion != FeedCursorSchemaVersion {
		return c, false
	}
	return c, true
}

// Resolution is the three-way resume classification for a feed resource.
type Resolution int

const (
	// Invalidated: no valid cursor — full re-crawl from newest.
	Invalidated Resolution = iota
	// Unchanged: the resource's head hasn't moved since what we've stored —
	// skip.
	Unchanged
	// Grew: fetch only the new head down to the last-stored media.
	Grew
)

func (r Resolution) String() string {
	switch r {
	case Unchanged:
		return "unchanged"
	case Grew:
		return "grew"
	default:
		return "invalidated"
	}
}

// ResolveResume classifies a feed resource's resume state. ok is whether the
// stored cursor decoded to the current schema version. headPK is the pk the
// API currently reports at the head of the feed.
func ResolveResume(prev FeedCursor, ok bool, headPK string) Resolution {
	if !ok {
		return Invalidated
	}
	if headPK == prev.NewestPK {
		return Unchanged
	}
	return Grew
}
