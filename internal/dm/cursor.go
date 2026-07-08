package dm

import "encoding/json"

// CursorSchemaVersion versions the ENCODING of the cursor value stored in
// crawlkit's sync_cursor_state.cursor column. It is distinct from
// store.SchemaVersion (the table DDL version). Bumping it auto-invalidates every
// stored cursor (they decode to a mismatched version → full re-crawl), which is
// the cursor-schema-drift trigger the reliability spine calls for.
const CursorSchemaVersion = 1

// ThreadCursor is the per-thread resume state, JSON-encoded into the cursor
// column. Carrying ConsecutiveEmpty here means one cursor row holds all resume
// state — no second lookup for the dead-man's-switch floor.
type ThreadCursor struct {
	SchemaVersion     int    `json:"v"`
	OldestCursor      string `json:"oldest"`    // backward-pagination resume point
	NewestItemID      string `json:"newest"`    // "grew" detection anchor
	NewestTimestampUS int64  `json:"newest_ts"` // newest message we've stored
	Backfilled        bool   `json:"backfilled"`
	ConsecutiveEmpty  int    `json:"empty"`
}

// Encode marshals the cursor for storage.
func (c ThreadCursor) Encode() string {
	b, _ := json.Marshal(c)
	return string(b)
}

// DecodeThreadCursor parses a stored cursor. ok is false when the value is
// absent, unparseable, or a mismatched schema version — all of which mean the
// thread must be treated as invalidated (full re-crawl).
func DecodeThreadCursor(s string) (ThreadCursor, bool) {
	if s == "" {
		return ThreadCursor{}, false
	}
	var c ThreadCursor
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return ThreadCursor{}, false
	}
	if c.SchemaVersion != CursorSchemaVersion {
		return c, false
	}
	return c, true
}

// InboxCursor carries the inbox pagination cursor AND seq_id (which must thread
// through every inbox page, not just page 1).
type InboxCursor struct {
	SchemaVersion int    `json:"v"`
	Cursor        string `json:"cursor"`
	SeqID         string `json:"seq_id"`
}

// Encode marshals the inbox cursor.
func (c InboxCursor) Encode() string {
	b, _ := json.Marshal(c)
	return string(b)
}

// DecodeInboxCursor parses a stored inbox cursor; ok=false on absent/bad/version
// mismatch.
func DecodeInboxCursor(s string) (InboxCursor, bool) {
	if s == "" {
		return InboxCursor{}, false
	}
	var c InboxCursor
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return InboxCursor{}, false
	}
	if c.SchemaVersion != CursorSchemaVersion {
		return c, false
	}
	return c, true
}

// Resolution is the three-way resume classification for a thread.
type Resolution int

const (
	// Invalidated: no valid cursor — full re-crawl from newest.
	Invalidated Resolution = iota
	// Unchanged: the thread has no activity newer than what we've stored — skip.
	Unchanged
	// Grew: fetch only the new tail down to the last-stored message.
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

// ResolveResume classifies a thread's resume state. ok is whether the stored
// cursor decoded to the current schema version. threadLastActivityUS is the
// inbox-reported last-activity timestamp for the thread.
func ResolveResume(prev ThreadCursor, ok bool, threadLastActivityUS int64) Resolution {
	if !ok {
		return Invalidated
	}
	if threadLastActivityUS > 0 && threadLastActivityUS <= prev.NewestTimestampUS {
		return Unchanged
	}
	return Grew
}
