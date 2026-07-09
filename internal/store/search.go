package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	crawlstore "github.com/openclaw/crawlkit/store"
)

// SearchHit is one full-text search result. Snippet is the ONLY message/caption
// content it carries; every other field is a locator (ids + timestamp) so a hit
// can be found again without dumping the row's content.
type SearchHit struct {
	Entity      string  // "dm" | "media"
	Key         string  // item_id (dm) | pk (media)
	ThreadID    string  // dm only
	Sender      string  // dm only — @username of the message author ("" if unknown)
	Code        string  // media only (shortcode)
	TimestampUS int64   // timestamp_us (dm) | taken_at_us (media)
	Unsent      bool    // dm only — the message was later unsent (retained, flagged)
	Snippet     string  // the matched span, with the term bracketed
	Score       float64 // bm25; more negative = better match
}

// ErrEmptyQuery is returned when the query reduces to no searchable tokens. Its
// text never contains the user's input (privacy: the query itself is sensitive).
var ErrEmptyQuery = errors.New("query has no searchable terms")

// Search runs a full-text query over the archive. entity ∈ {"dm","media","all"}
// ("" is treated as "all"). It reads private message/caption content but returns
// only a bracketed snippet per hit, and it never logs the query or the content.
//
// The query is sanitized through crawlkit's FTS5TokenQuery — arbitrary input
// becomes quoted, implicitly-ANDed tokens, so no user text can reach the FTS5
// parser as an operator (no injection, no operator-parse errors that could echo
// the query back). The sanitized match string is passed as a BOUND parameter.
func (st *Store) Search(ctx context.Context, terms, entity string, limit int) ([]SearchHit, error) {
	match := crawlstore.FTS5TokenQuery(terms)
	if match == "" {
		return nil, ErrEmptyQuery
	}
	if limit <= 0 {
		limit = 20
	}

	switch entity {
	case "dm":
		return st.searchDMs(ctx, match, limit)
	case "media":
		return st.searchMedia(ctx, match, limit)
	case "", "all":
		dm, err := st.searchDMs(ctx, match, limit)
		if err != nil {
			return nil, err
		}
		md, err := st.searchMedia(ctx, match, limit)
		if err != nil {
			return nil, err
		}
		hits := make([]SearchHit, 0, len(dm)+len(md))
		hits = append(hits, dm...)
		hits = append(hits, md...)
		// bm25 across two corpora is only heuristically comparable, but at a
		// personal archive's scale merging by score (then recency) is good enough;
		// --type exists when precision matters.
		sort.SliceStable(hits, func(i, j int) bool {
			if hits[i].Score != hits[j].Score {
				return hits[i].Score < hits[j].Score // more negative = better
			}
			return hits[i].TimestampUS > hits[j].TimestampUS
		})
		if len(hits) > limit {
			hits = hits[:limit]
		}
		return hits, nil
	default:
		return nil, fmt.Errorf("unknown search entity %q (want dm|media|all)", entity)
	}
}

func (st *Store) searchDMs(ctx context.Context, match string, limit int) ([]SearchHit, error) {
	rows, err := st.s.DB().QueryContext(ctx, `
select m.item_id, m.thread_id, coalesce(u.username, ''), m.timestamp_us, m.unsent_observed,
       snippet(dm_message_fts, 0, '[', ']', '…', 10), bm25(dm_message_fts)
from dm_message_fts
join dm_message m on m.rowid = dm_message_fts.rowid
left join dm_user u on u.user_id = m.user_id
where dm_message_fts match ?
order by rank
limit ?`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("search dms: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		var unsent int
		if err := rows.Scan(&h.Key, &h.ThreadID, &h.Sender, &h.TimestampUS, &unsent, &h.Snippet, &h.Score); err != nil {
			return nil, fmt.Errorf("scan dm hit: %w", err)
		}
		h.Entity = "dm"
		h.Unsent = unsent != 0
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dm hits: %w", err)
	}
	return out, nil
}

func (st *Store) searchMedia(ctx context.Context, match string, limit int) ([]SearchHit, error) {
	rows, err := st.s.DB().QueryContext(ctx, `
select m.pk, coalesce(m.code, ''), m.taken_at_us,
       snippet(media_fts, 0, '[', ']', '…', 10), bm25(media_fts)
from media_fts
join media m on m.rowid = media_fts.rowid
where media_fts match ?
order by rank
limit ?`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("search media: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		var takenAt sql.NullInt64
		if err := rows.Scan(&h.Key, &h.Code, &takenAt, &h.Snippet, &h.Score); err != nil {
			return nil, fmt.Errorf("scan media hit: %w", err)
		}
		h.Entity = "media"
		h.TimestampUS = takenAt.Int64
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate media hits: %w", err)
	}
	return out, nil
}

// OptimizeSearch merges the FTS5 index b-trees for faster queries and a smaller
// file. Call it after a fully successful sync (or a reindex), never on the read
// path. A failure is non-fatal — the caller logs and continues.
func (st *Store) OptimizeSearch(ctx context.Context) error {
	for _, table := range []string{"dm_message_fts", "media_fts"} {
		if err := crawlstore.OptimizeFTS5(ctx, st.s.DB(), table); err != nil {
			return err
		}
	}
	return nil
}

// ReindexSearch rebuilds both FTS5 indexes from their content tables and then
// verifies each against the two-argument integrity-check (the only form that
// detects an index/content mismatch). It heals the rare skew a migration or a
// manual edit could introduce. Content-free by construction.
func (st *Store) ReindexSearch(ctx context.Context) error {
	for _, table := range []string{"dm_message_fts", "media_fts"} {
		if _, err := st.s.DB().ExecContext(ctx, "insert into "+table+"("+table+") values('rebuild')"); err != nil {
			return fmt.Errorf("rebuild %s: %w", table, err)
		}
		if _, err := st.s.DB().ExecContext(ctx, "insert into "+table+"("+table+", rank) values('integrity-check', 1)"); err != nil {
			return fmt.Errorf("integrity-check %s: %w", table, err)
		}
	}
	return st.OptimizeSearch(ctx)
}
