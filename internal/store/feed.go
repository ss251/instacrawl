package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openclaw/crawlkit/state"

	"github.com/ss251/instacrawl/internal/feed"
)

// Feed cursor entities (crawlkit sync_cursor_state rows), all under source "web".
const (
	FeedEntityOwn        = "feed_own"
	FeedEntitySaved      = "feed_saved"
	FeedEntityCollection = "feed_collection"
	FeedEntityLiked      = "feed_liked"
)

// Feed junction kinds handled by CommitFeedPage.
const (
	FeedKindOwn   = "own"
	FeedKindSaved = "saved" // also used for a collection feed (page.Saved carries the collection id)
	FeedKindLiked = "liked"
)

// GetFeedCursor reads and decodes a feed resource's resume cursor. ok is false
// when there is no row OR the stored value fails to decode at the current cursor
// schema version.
func (st *Store) GetFeedCursor(ctx context.Context, entity, key string) (feed.FeedCursor, bool, error) {
	rec, found, err := state.NewCursor(st.s.DB()).Get(ctx, cursorSource, entity, key)
	if err != nil {
		return feed.FeedCursor{}, false, fmt.Errorf("get feed cursor %s/%s: %w", entity, key, err)
	}
	if !found {
		return feed.FeedCursor{}, false, nil
	}
	cur, decoded := feed.DecodeFeedCursor(rec.Cursor)
	return cur, decoded, nil
}

// FeedHasHistory reports whether the resource has stored media — the input to
// reliability.Floor.HasKnownHistory. kind ∈ {own, saved, liked}; key is the
// collection id for a collection feed (kind=saved), else "".
func (st *Store) FeedHasHistory(ctx context.Context, kind, key string) (bool, error) {
	var q string
	var args []any
	switch kind {
	case FeedKindOwn:
		q = `select exists(select 1 from own_post limit 1)`
	case FeedKindSaved:
		q, args = `select exists(select 1 from saved_item where collection_id = ? limit 1)`, []any{key}
	case FeedKindLiked:
		q = `select exists(select 1 from liked_item limit 1)`
	default:
		return false, nil
	}
	var exists int
	if err := st.s.DB().QueryRowContext(ctx, q, args...).Scan(&exists); err != nil {
		return false, fmt.Errorf("feed history %s/%s: %w", kind, key, err)
	}
	return exists != 0, nil
}

// CommitFeedPage persists one feed page and advances its cursor in a SINGLE
// transaction: media (+carousel children +files +blobs) upserts, then the
// junction rows for kind, then the cursor write — the same "cursor write in the
// same tx as the upserts" contract as CommitThreadPage.
func (st *Store) CommitFeedPage(ctx context.Context, page *feed.FeedPage, cur feed.FeedCursor, entity, key, kind string) error {
	if page == nil {
		return errors.New("nil feed page")
	}
	tx, err := st.s.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339)

	for i, m := range page.Media {
		if err := upsertMediaTree(ctx, tx, m, now); err != nil {
			return err
		}
		if kind == FeedKindOwn {
			if err := upsertOwnPost(ctx, tx, m.PK, i, now); err != nil {
				return err
			}
		}
	}
	if kind == FeedKindSaved {
		for _, s := range page.Saved {
			if err := upsertSavedItem(ctx, tx, s.MediaPK, s.CollectionID, now); err != nil {
				return err
			}
		}
	}
	if kind == FeedKindLiked {
		for _, l := range page.Liked {
			if err := upsertLikedItem(ctx, tx, l.MediaPK, now); err != nil {
				return err
			}
		}
	}

	if err := state.NewCursor(tx).Set(ctx, cursorSource, entity, key, cur.Encode()); err != nil {
		return fmt.Errorf("set feed cursor %s/%s: %w", entity, key, err)
	}
	return tx.Commit()
}

// CommitStorySet persists a user's active stories (no cursor — stories are a
// single ephemeral fetch, not paginated).
func (st *Store) CommitStorySet(ctx context.Context, set *feed.StorySet) error {
	if set == nil {
		return errors.New("nil story set")
	}
	tx, err := st.s.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, s := range set.Stories {
		if err := upsertMediaTree(ctx, tx, s.Media, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
insert into story_item (media_pk, expiring_at_us, taken_at_us, source, fetched_at, raw_json)
values (?, ?, ?, 'web', ?, ?)
on conflict(media_pk) do update set
  expiring_at_us=excluded.expiring_at_us, taken_at_us=excluded.taken_at_us,
  fetched_at=excluded.fetched_at, raw_json=excluded.raw_json`,
			s.Media.PK, s.ExpiringAtUS, s.TakenAtUS, now, nullIfEmpty(s.Media.RawJSON)); err != nil {
			return fmt.Errorf("upsert story %s: %w", s.Media.PK, err)
		}
	}
	return tx.Commit()
}

// UpsertCollection records a saved-posts collection's metadata.
func (st *Store) UpsertCollection(ctx context.Context, c feed.Collection) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := st.s.DB().ExecContext(ctx, `
insert into collection (collection_id, name, collection_type, cover_media_pk, media_count, source, fetched_at)
values (?, ?, ?, ?, ?, 'web', ?)
on conflict(collection_id) do update set
  name=excluded.name, collection_type=excluded.collection_type,
  cover_media_pk=excluded.cover_media_pk, media_count=excluded.media_count, fetched_at=excluded.fetched_at`,
		c.ID, c.Name, c.Type, c.CoverMediaPK, c.MediaCount, now)
	if err != nil {
		return fmt.Errorf("upsert collection %s: %w", c.ID, err)
	}
	return nil
}

// upsertMediaTree upserts a media, its carousel children (recursively one
// level), and all their downloadable files + blobs.
func upsertMediaTree(ctx context.Context, tx *sql.Tx, m feed.Media, now string) error {
	if m.PK == "" {
		return nil // nothing addressable to store
	}
	if err := upsertMedia(ctx, tx, m, now); err != nil {
		return err
	}
	for _, f := range m.Files {
		if err := upsertMediaFile(ctx, tx, m.PK, f, now); err != nil {
			return err
		}
	}
	for i, child := range m.Carousel {
		if err := upsertMedia(ctx, tx, child, now); err != nil {
			return err
		}
		for _, f := range child.Files {
			if err := upsertMediaFile(ctx, tx, child.PK, f, now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
insert into media_child (parent_pk, child_pk, position) values (?, ?, ?)
on conflict(parent_pk, position) do update set child_pk=excluded.child_pk`,
			m.PK, child.PK, i); err != nil {
			return fmt.Errorf("upsert carousel child %s[%d]: %w", m.PK, i, err)
		}
	}
	return nil
}

// upsertMedia applies the richness tiering: a leaner re-capture never clobbers a
// richer stored row (the dm_user pattern).
func upsertMedia(ctx context.Context, tx *sql.Tx, m feed.Media, now string) error {
	fc := feed.MediaFieldCount(m.Code, m.CaptionText, len(m.Files) > 0)
	_, err := tx.ExecContext(ctx, `
insert into media (pk, code, media_type, media_type_known, product_type, owner_id, taken_at_us,
  caption_text, caption_created_at_us, field_count, source, fetched_at, raw_json)
values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(pk) do update set
  code=excluded.code, media_type=excluded.media_type, media_type_known=excluded.media_type_known,
  product_type=excluded.product_type, owner_id=excluded.owner_id, taken_at_us=excluded.taken_at_us,
  caption_text=excluded.caption_text, caption_created_at_us=excluded.caption_created_at_us,
  field_count=excluded.field_count, source=excluded.source, fetched_at=excluded.fetched_at, raw_json=excluded.raw_json
where excluded.field_count > media.field_count
   or (excluded.field_count = media.field_count and excluded.fetched_at >= media.fetched_at)`,
		m.PK, m.Code, m.MediaType, boolToInt(m.Known), m.ProductType, m.OwnerID, m.TakenAtUS,
		m.CaptionText, m.CaptionCreatedAtUS, fc, srcOr(m.Source), now, nullIfEmpty(m.RawJSON))
	if err != nil {
		return fmt.Errorf("upsert media %s: %w", m.PK, err)
	}
	return nil
}

func upsertMediaFile(ctx context.Context, tx *sql.Tx, mediaPK string, f feed.MediaFile, now string) error {
	// Register the blob first (content-addressed byte registry) if bytes landed.
	if f.Hash != "" {
		if _, err := tx.ExecContext(ctx, `
insert into media_blob (hash, downloaded_at, source) values (?, ?, 'web')
on conflict(hash) do nothing`, f.Hash, now); err != nil {
			return fmt.Errorf("register blob %s: %w", f.Hash, err)
		}
	}
	_, err := tx.ExecContext(ctx, `
insert into media_file (media_pk, kind, position, width, height, original_url, blob_hash, downloaded, missing, source)
values (?, ?, ?, ?, ?, ?, ?, ?, ?, 'web')
on conflict(media_pk, kind, position) do update set
  width=excluded.width, height=excluded.height, original_url=excluded.original_url,
  blob_hash=excluded.blob_hash, downloaded=excluded.downloaded, missing=excluded.missing, source=excluded.source`,
		mediaPK, f.Kind, f.Position, f.Width, f.Height, nullIfEmpty(f.URL), nullIfEmpty(f.Hash),
		boolToInt(f.Downloaded), boolToInt(f.Missing))
	if err != nil {
		return fmt.Errorf("upsert media_file %s/%s/%d: %w", mediaPK, f.Kind, f.Position, err)
	}
	return nil
}

func upsertOwnPost(ctx context.Context, tx *sql.Tx, pk string, pos int, now string) error {
	_, err := tx.ExecContext(ctx, `
insert into own_post (media_pk, feed_position, source, fetched_at) values (?, ?, 'web', ?)
on conflict(media_pk) do update set fetched_at=excluded.fetched_at`, pk, pos, now)
	if err != nil {
		return fmt.Errorf("upsert own_post %s: %w", pk, err)
	}
	return nil
}

func upsertSavedItem(ctx context.Context, tx *sql.Tx, pk, collectionID, now string) error {
	_, err := tx.ExecContext(ctx, `
insert into saved_item (media_pk, collection_id, source, fetched_at) values (?, ?, 'web', ?)
on conflict(media_pk, collection_id) do update set fetched_at=excluded.fetched_at`, pk, collectionID, now)
	if err != nil {
		return fmt.Errorf("upsert saved_item %s: %w", pk, err)
	}
	return nil
}

func upsertLikedItem(ctx context.Context, tx *sql.Tx, pk, now string) error {
	_, err := tx.ExecContext(ctx, `
insert into liked_item (media_pk, source, fetched_at) values (?, 'web', ?)
on conflict(media_pk) do update set fetched_at=excluded.fetched_at`, pk, now)
	if err != nil {
		return fmt.Errorf("upsert liked_item %s: %w", pk, err)
	}
	return nil
}

// Count helpers back the gate tests.
func (st *Store) CountMedia(ctx context.Context) (int, error)    { return st.count(ctx, "media") }
func (st *Store) CountOwnPosts(ctx context.Context) (int, error) { return st.count(ctx, "own_post") }
func (st *Store) CountBlobs(ctx context.Context) (int, error)    { return st.count(ctx, "media_blob") }
func (st *Store) CountSaved(ctx context.Context) (int, error)    { return st.count(ctx, "saved_item") }
func (st *Store) CountStories(ctx context.Context) (int, error)  { return st.count(ctx, "story_item") }
