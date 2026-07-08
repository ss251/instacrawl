package dm

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The JSON field names/nesting below are the Phase-2 best guess at Instagram's
// private direct_v2 shapes. They are validated and retuned in 2b against a real
// session (this is the fragile surface, analogous to ig/endpoints.go).
// Everything is parsed LENIENTLY — a bad/renamed field degrades to zero rather
// than failing the whole batch — and RawJSON is retained, so a second future
// drift can never lose a historical page.

// jsonStr accepts a JSON number or string and yields the string form (big-int
// safe: never routes an id through float64).
type jsonStr string

func (j *jsonStr) UnmarshalJSON(b []byte) error {
	*j = jsonStr(strings.Trim(strings.TrimSpace(string(b)), `"`))
	return nil
}

// jsonI64 accepts a JSON number or numeric string and yields int64.
type jsonI64 int64

func (j *jsonI64) UnmarshalJSON(b []byte) error {
	*j = 0
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	// Lenient: a malformed number degrades to 0 rather than failing the batch.
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		*j = jsonI64(v)
	}
	return nil
}

type userJSON struct {
	PK            jsonStr `json:"pk"`
	Username      string  `json:"username"`
	FullName      string  `json:"full_name"`
	ProfilePicURL string  `json:"profile_pic_url"`
}

type threadJSON struct {
	ThreadID     jsonStr           `json:"thread_id"`
	ThreadTitle  string            `json:"thread_title"`
	IsGroup      bool              `json:"is_group"`
	Users        []userJSON        `json:"users"`
	AdminUserIDs []jsonStr         `json:"admin_user_ids"`
	LastActivity jsonI64           `json:"last_activity_at"`
	Items        []json.RawMessage `json:"items"`
	OldestCursor string            `json:"oldest_cursor"`
	HasOlder     bool              `json:"has_older"`
}

type reactionJSON struct {
	SenderID  jsonStr `json:"sender_id"`
	Emoji     string  `json:"emoji"`
	Timestamp jsonI64 `json:"timestamp"`
}

type itemJSON struct {
	ItemID     jsonStr         `json:"item_id"`
	UserID     jsonStr         `json:"user_id"`
	Timestamp  jsonI64         `json:"timestamp"`
	ItemType   string          `json:"item_type"`
	Text       string          `json:"text"`
	Media      json.RawMessage `json:"media"`
	MediaShare json.RawMessage `json:"media_share"`
	ReelShare  json.RawMessage `json:"reel_share"`
	StoryShare json.RawMessage `json:"story_share"`
	Link       json.RawMessage `json:"link"`
	Reactions  *struct {
		Likes  []reactionJSON `json:"likes"`
		Emojis []reactionJSON `json:"emojis"`
	} `json:"reactions"`
	RepliedTo *struct {
		ItemID jsonStr `json:"item_id"`
	} `json:"replied_to_message"`
	ActionLog json.RawMessage `json:"action_log"`
}

func participantFrom(u userJSON, source string) Participant {
	return Participant{
		UserID:        string(u.PK),
		Username:      u.Username,
		FullName:      u.FullName,
		ProfilePicURL: u.ProfilePicURL,
		FieldCount:    FieldCount(u.Username, u.FullName, u.ProfilePicURL),
		Source:        source,
	}
}

func rawStr(r json.RawMessage) string {
	if len(r) == 0 || string(r) == "null" {
		return ""
	}
	return string(r)
}

// messageFrom converts one item into a Message (and, for an action_log item, a
// projected ThreadEvent). raw is the exact bytes of this item, retained.
func messageFrom(it itemJSON, threadID, source string, raw json.RawMessage) (Message, *ThreadEvent) {
	itype := ItemType(it.ItemType)
	known := KnownItemTypes[itype]
	m := Message{
		ItemID:      string(it.ItemID),
		ThreadID:    threadID,
		UserID:      string(it.UserID),
		TimestampUS: int64(it.Timestamp),
		ItemType:    itype,
		RawItemType: it.ItemType,
		Known:       known,
		Text:        it.Text,
		MediaJSON:   rawStr(it.Media),
		SharedJSON:  firstNonEmpty(rawStr(it.MediaShare), rawStr(it.ReelShare), rawStr(it.StoryShare)),
		LinkJSON:    rawStr(it.Link),
		RawJSON:     string(raw),
		Source:      source,
	}
	if it.RepliedTo != nil && string(it.RepliedTo.ItemID) != "" {
		m.ReplyTo = &ReplyRef{ParentItemID: string(it.RepliedTo.ItemID)}
	}
	if it.Reactions != nil {
		for _, r := range it.Reactions.Likes {
			m.Reactions = append(m.Reactions, Reaction{ItemID: m.ItemID, UserID: string(r.SenderID), Emoji: "❤", TimestampUS: int64(r.Timestamp)})
		}
		for _, r := range it.Reactions.Emojis {
			m.Reactions = append(m.Reactions, Reaction{ItemID: m.ItemID, UserID: string(r.SenderID), Emoji: r.Emoji, TimestampUS: int64(r.Timestamp)})
		}
	}
	var ev *ThreadEvent
	if itype == ItemActionLog {
		ev = &ThreadEvent{
			EventID:     m.ItemID,
			ThreadID:    threadID,
			Kind:        "action_log",
			ActorID:     m.UserID,
			TimestampUS: m.TimestampUS,
			RawJSON:     string(raw),
			Source:      source,
		}
	}
	return m, ev
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// ParseInbox parses an inbox page (source='web', not pending).
func ParseInbox(body []byte) (*InboxPage, error) { return parseInbox(body, false) }

// ParsePendingInbox parses a pending (message-request) inbox page; threads are
// marked IsPending.
func ParsePendingInbox(body []byte) (*InboxPage, error) { return parseInbox(body, true) }

func parseInbox(body []byte, pending bool) (*InboxPage, error) {
	var env struct {
		SeqID jsonStr `json:"seq_id"`
		Inbox struct {
			Threads      []threadJSON `json:"threads"`
			HasOlder     bool         `json:"has_older"`
			OldestCursor string       `json:"oldest_cursor"`
		} `json:"inbox"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode inbox: %w", err)
	}
	page := &InboxPage{SeqID: string(env.SeqID)}
	if env.Inbox.HasOlder {
		page.NextCursor = env.Inbox.OldestCursor
	}
	for _, tj := range env.Inbox.Threads {
		th := Thread{
			ThreadID:       string(tj.ThreadID),
			Title:          tj.ThreadTitle,
			IsGroup:        tj.IsGroup,
			IsPending:      pending,
			LastActivityUS: int64(tj.LastActivity),
			Source:         "web",
		}
		for _, u := range tj.Users {
			th.Participants = append(th.Participants, participantFrom(u, "web"))
		}
		for _, a := range tj.AdminUserIDs {
			th.AdminIDs = append(th.AdminIDs, string(a))
		}
		page.Threads = append(page.Threads, th)
	}
	return page, nil
}

// ParseThread parses one page of a thread's messages (paginated backwards).
func ParseThread(body []byte) (*ThreadPage, error) {
	var env struct {
		Thread threadJSON `json:"thread"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode thread: %w", err)
	}
	t := env.Thread
	threadID := string(t.ThreadID)
	page := &ThreadPage{ThreadID: threadID}
	if t.HasOlder {
		page.OldestCursor = t.OldestCursor
	}

	thread := &Thread{
		ThreadID:       threadID,
		Title:          t.ThreadTitle,
		IsGroup:        t.IsGroup,
		LastActivityUS: int64(t.LastActivity),
		Source:         "web",
	}
	for _, u := range t.Users {
		p := participantFrom(u, "web")
		thread.Participants = append(thread.Participants, p)
	}
	for _, a := range t.AdminUserIDs {
		thread.AdminIDs = append(thread.AdminIDs, string(a))
	}
	page.Thread = thread
	page.Participants = thread.Participants

	for i, rawItem := range t.Items {
		var it itemJSON
		_ = json.Unmarshal(rawItem, &it) // lenient: a bad item degrades, never fails the page
		msg, ev := messageFrom(it, threadID, "web", rawItem)
		page.Messages = append(page.Messages, msg)
		page.ItemIDs = append(page.ItemIDs, msg.ItemID)
		if !msg.Known {
			page.UnknownTypes = append(page.UnknownTypes, msg.RawItemType)
		}
		if ev != nil {
			page.Events = append(page.Events, *ev)
		}
		if i == 0 {
			page.MinTS, page.MaxTS = msg.TimestampUS, msg.TimestampUS
			continue
		}
		if msg.TimestampUS < page.MinTS {
			page.MinTS = msg.TimestampUS
		}
		if msg.TimestampUS > page.MaxTS {
			page.MaxTS = msg.TimestampUS
		}
	}
	return page, nil
}
