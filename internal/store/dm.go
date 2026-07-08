package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/openclaw/crawlkit/state"

	"github.com/ss251/instacrawl/internal/dm"
)

// cursorSource/cursorEntity name the crawlkit sync_cursor_state rows used for
// per-thread resume. The reconciler lane ("web") doubles as the cursor source.
const (
	cursorSource       = "web"
	cursorEntityThread = "dm_thread"
)

// GetThreadCursor reads and decodes a thread's resume cursor. ok is false when
// there is no row OR the stored value fails to decode at the current cursor
// schema version (either way → the thread is treated as invalidated).
func (st *Store) GetThreadCursor(ctx context.Context, threadID string) (dm.ThreadCursor, bool, error) {
	rec, found, err := state.NewCursor(st.s.DB()).Get(ctx, cursorSource, cursorEntityThread, threadID)
	if err != nil {
		return dm.ThreadCursor{}, false, fmt.Errorf("get thread cursor %s: %w", threadID, err)
	}
	if !found {
		return dm.ThreadCursor{}, false, nil
	}
	cur, decoded := dm.DecodeThreadCursor(rec.Cursor)
	return cur, decoded, nil
}

// ThreadHasHistory reports whether any message is already stored for the thread
// — the input to reliability.Floor.HasKnownHistory.
func (st *Store) ThreadHasHistory(ctx context.Context, threadID string) (bool, error) {
	var exists int
	err := st.s.DB().QueryRowContext(ctx,
		`select exists(select 1 from dm_message where thread_id = ? limit 1)`, threadID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("thread history %s: %w", threadID, err)
	}
	return exists != 0, nil
}

// CommitThreadPage persists one thread page and advances its cursor in a SINGLE
// transaction: unsend detection over the page's time window, then thread /
// participant / message / reaction / event upserts, then the cursor write. A
// crash between fetch and commit therefore neither half-applies (rollback) nor
// double-applies (upsert by stable id) — the concrete "idempotency ≠ locking".
func (st *Store) CommitThreadPage(ctx context.Context, page *dm.ThreadPage, cur dm.ThreadCursor) error {
	if page == nil {
		return errors.New("nil thread page")
	}
	tx, err := st.s.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit

	now := time.Now().UTC().Format(time.RFC3339)

	// Unsend detection: any item already stored within this page's exact time
	// window that is absent from the re-fetched page was unsent. Flag it — never
	// delete. Bounded to the re-fetched range, so un-fetched history is safe.
	if page.MaxTS > 0 {
		existing, err := messageIDsInRange(ctx, tx, page.ThreadID, page.MinTS, page.MaxTS)
		if err != nil {
			return err
		}
		present := make(map[string]bool, len(page.ItemIDs))
		for _, id := range page.ItemIDs {
			present[id] = true
		}
		for _, id := range existing {
			if !present[id] {
				if _, err := tx.ExecContext(ctx,
					`update dm_message set unsent_observed = 1, unsent_observed_at = ? where item_id = ? and unsent_observed = 0`,
					now, id); err != nil {
					return fmt.Errorf("mark unsent %s: %w", id, err)
				}
			}
		}
	}

	if page.Thread != nil {
		if err := upsertThread(ctx, tx, page.Thread, now); err != nil {
			return err
		}
		for _, p := range page.Thread.Participants {
			if err := upsertUser(ctx, tx, p, now); err != nil {
				return err
			}
			if err := upsertMembership(ctx, tx, page.ThreadID, p); err != nil {
				return err
			}
		}
	}

	for _, m := range page.Messages {
		if err := upsertMessage(ctx, tx, m, now); err != nil {
			return err
		}
		for _, r := range m.Reactions {
			if err := upsertReaction(ctx, tx, r); err != nil {
				return err
			}
		}
	}
	for _, e := range page.Events {
		if err := upsertEvent(ctx, tx, e, now); err != nil {
			return err
		}
	}

	// Cursor write in the SAME transaction as the upserts.
	if err := state.NewCursor(tx).Set(ctx, cursorSource, cursorEntityThread, page.ThreadID, cur.Encode()); err != nil {
		return fmt.Errorf("set thread cursor %s: %w", page.ThreadID, err)
	}
	return tx.Commit()
}

func messageIDsInRange(ctx context.Context, tx *sql.Tx, threadID string, minTS, maxTS int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`select item_id from dm_message where thread_id = ? and timestamp_us >= ? and timestamp_us <= ?`,
		threadID, minTS, maxTS)
	if err != nil {
		return nil, fmt.Errorf("message ids in range: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func upsertThread(ctx context.Context, tx *sql.Tx, t *dm.Thread, now string) error {
	admins, _ := json.Marshal(t.AdminIDs)
	_, err := tx.ExecContext(ctx, `
insert into dm_thread (thread_id, title, is_group, is_pending, admin_ids, last_activity_us, source, fetched_at)
values (?, ?, ?, ?, ?, ?, ?, ?)
on conflict(thread_id) do update set
  title=excluded.title, is_group=excluded.is_group, is_pending=excluded.is_pending,
  admin_ids=excluded.admin_ids, last_activity_us=excluded.last_activity_us,
  source=excluded.source, fetched_at=excluded.fetched_at`,
		t.ThreadID, t.Title, boolToInt(t.IsGroup), boolToInt(t.IsPending), string(admins),
		t.LastActivityUS, srcOr(t.Source), now)
	if err != nil {
		return fmt.Errorf("upsert thread %s: %w", t.ThreadID, err)
	}
	return nil
}

// upsertUser applies the reconciler tiering in SQL: a richer profile (more
// non-null fields) always wins; equal field counts → the newer capture wins. A
// numeric-ID-only stub (field_count 0) can never clobber a stored profile.
func upsertUser(ctx context.Context, tx *sql.Tx, p dm.Participant, now string) error {
	_, err := tx.ExecContext(ctx, `
insert into dm_user (user_id, username, full_name, profile_pic_url, field_count, source, fetched_at)
values (?, ?, ?, ?, ?, ?, ?)
on conflict(user_id) do update set
  username=excluded.username, full_name=excluded.full_name, profile_pic_url=excluded.profile_pic_url,
  field_count=excluded.field_count, source=excluded.source, fetched_at=excluded.fetched_at
where excluded.field_count > dm_user.field_count
   or (excluded.field_count = dm_user.field_count and excluded.fetched_at >= dm_user.fetched_at)`,
		p.UserID, p.Username, p.FullName, p.ProfilePicURL, p.FieldCount, srcOr(p.Source), now)
	if err != nil {
		return fmt.Errorf("upsert user %s: %w", p.UserID, err)
	}
	return nil
}

func upsertMembership(ctx context.Context, tx *sql.Tx, threadID string, p dm.Participant) error {
	_, err := tx.ExecContext(ctx, `
insert into dm_membership (thread_id, user_id, is_admin, source)
values (?, ?, ?, ?)
on conflict(thread_id, user_id) do update set is_admin=excluded.is_admin, source=excluded.source`,
		threadID, p.UserID, boolToInt(p.IsAdmin), srcOr(p.Source))
	if err != nil {
		return fmt.Errorf("upsert membership %s/%s: %w", threadID, p.UserID, err)
	}
	return nil
}

func upsertMessage(ctx context.Context, tx *sql.Tx, m dm.Message, now string) error {
	var replyTo any
	if m.ReplyTo != nil {
		replyTo = m.ReplyTo.ParentItemID
	}
	_, err := tx.ExecContext(ctx, `
insert into dm_message (item_id, thread_id, user_id, timestamp_us, item_type, item_type_known,
  text, media_json, shared_json, link_json, reply_to_item_id, source, fetched_at, raw_json)
values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(item_id) do update set
  thread_id=excluded.thread_id, user_id=excluded.user_id, timestamp_us=excluded.timestamp_us,
  item_type=excluded.item_type, item_type_known=excluded.item_type_known, text=excluded.text,
  media_json=excluded.media_json, shared_json=excluded.shared_json, link_json=excluded.link_json,
  reply_to_item_id=excluded.reply_to_item_id, source=excluded.source, fetched_at=excluded.fetched_at,
  raw_json=excluded.raw_json`,
		m.ItemID, m.ThreadID, m.UserID, m.TimestampUS, string(m.ItemType), boolToInt(m.Known),
		m.Text, nullIfEmpty(m.MediaJSON), nullIfEmpty(m.SharedJSON), nullIfEmpty(m.LinkJSON),
		replyTo, srcOr(m.Source), now, m.RawJSON)
	if err != nil {
		return fmt.Errorf("upsert message %s: %w", m.ItemID, err)
	}
	return nil
}

func upsertReaction(ctx context.Context, tx *sql.Tx, r dm.Reaction) error {
	_, err := tx.ExecContext(ctx, `
insert into dm_reaction (item_id, user_id, emoji, timestamp_us, source)
values (?, ?, ?, ?, 'web')
on conflict(item_id, user_id, emoji) do update set timestamp_us=excluded.timestamp_us`,
		r.ItemID, r.UserID, r.Emoji, r.TimestampUS)
	if err != nil {
		return fmt.Errorf("upsert reaction %s/%s: %w", r.ItemID, r.UserID, err)
	}
	return nil
}

func upsertEvent(ctx context.Context, tx *sql.Tx, e dm.ThreadEvent, now string) error {
	_, err := tx.ExecContext(ctx, `
insert into dm_thread_event (event_id, thread_id, kind, actor_id, target_id, timestamp_us, source, fetched_at, raw_json)
values (?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(event_id) do update set
  kind=excluded.kind, actor_id=excluded.actor_id, target_id=excluded.target_id,
  timestamp_us=excluded.timestamp_us, source=excluded.source, fetched_at=excluded.fetched_at,
  raw_json=excluded.raw_json`,
		e.EventID, e.ThreadID, e.Kind, e.ActorID, e.TargetID, e.TimestampUS, srcOr(e.Source), now, e.RawJSON)
	if err != nil {
		return fmt.Errorf("upsert event %s: %w", e.EventID, err)
	}
	return nil
}

// CountMessages / CountThreads back the idempotency gate test.
func (st *Store) CountMessages(ctx context.Context) (int, error) { return st.count(ctx, "dm_message") }
func (st *Store) CountThreads(ctx context.Context) (int, error)  { return st.count(ctx, "dm_thread") }

func (st *Store) count(ctx context.Context, table string) (int, error) {
	var n int
	// table is a package-internal literal, never user input.
	if err := st.s.DB().QueryRowContext(ctx, `select count(*) from `+table).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// UnsentObserved reports whether a message has been flagged unsent (test helper).
func (st *Store) UnsentObserved(ctx context.Context, itemID string) (bool, error) {
	var v int
	err := st.s.DB().QueryRowContext(ctx, `select unsent_observed from dm_message where item_id = ?`, itemID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

func srcOr(s string) string {
	if s == "" {
		return "web"
	}
	return s
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
