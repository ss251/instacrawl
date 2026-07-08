package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ss251/instacrawl/internal/ig"
)

// Clock is injectable so tests get deterministic fetched_at values. Defaults to
// time.Now in production.
type Clock func() time.Time

// UpsertAccount idempotently writes the owner account. Re-running a probe
// upserts by pk, so a second identical run is a no-op beyond fetched_at — this
// is the "OK(1) → commit" path the Phase 1 gate proves. source is the
// reliability lane tag ("web").
func (st *Store) UpsertAccount(ctx context.Context, a *ig.Account, source string, clock Clock) error {
	if a == nil {
		return errors.New("nil account")
	}
	if a.PK == "" {
		return errors.New("account has no pk")
	}
	if source == "" {
		source = "web"
	}
	if clock == nil {
		clock = time.Now
	}
	fetchedAt := clock().UTC().Format(time.RFC3339)

	_, err := st.s.DB().ExecContext(ctx, `
insert into account (pk, username, full_name, is_private, profile_pic_url, media_count, source, fetched_at, raw_json)
values (?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(pk) do update set
  username        = excluded.username,
  full_name       = excluded.full_name,
  is_private      = excluded.is_private,
  profile_pic_url = excluded.profile_pic_url,
  media_count     = excluded.media_count,
  source          = excluded.source,
  fetched_at      = excluded.fetched_at,
  raw_json        = excluded.raw_json
`, a.PK, a.Username, a.FullName, boolToInt(a.IsPrivate), a.ProfilePicURL, a.MediaCount, source, fetchedAt, a.RawJSON)
	if err != nil {
		return fmt.Errorf("upsert account %s: %w", a.PK, err)
	}
	return nil
}

// AccountMediaCount returns the advertised own-post total for pk (the Phase 3
// gate's "known total"), or (0, false) if absent/unknown.
func (st *Store) AccountMediaCount(ctx context.Context, pk string) (int, bool, error) {
	var mc sql.NullInt64
	err := st.s.DB().QueryRowContext(ctx, `select media_count from account where pk = ?`, pk).Scan(&mc)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("account media_count %s: %w", pk, err)
	}
	if !mc.Valid {
		return 0, false, nil
	}
	return int(mc.Int64), true, nil
}

// GetAccount reads an account by pk. The second return is false if absent.
func (st *Store) GetAccount(ctx context.Context, pk string) (*ig.Account, bool, error) {
	var (
		a        ig.Account
		priv     int
		fullName sql.NullString
		picURL   sql.NullString
		rawJSON  sql.NullString
	)
	err := st.s.DB().QueryRowContext(ctx, `
select pk, username, full_name, is_private, profile_pic_url, raw_json
from account where pk = ?`, pk).Scan(&a.PK, &a.Username, &fullName, &priv, &picURL, &rawJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get account %s: %w", pk, err)
	}
	a.FullName = fullName.String
	a.IsPrivate = priv != 0
	a.ProfilePicURL = picURL.String
	a.RawJSON = rawJSON.String
	return &a, true, nil
}

// CountAccounts returns the number of account rows (used by tests to prove
// upsert idempotency).
func (st *Store) CountAccounts(ctx context.Context) (int, error) {
	var n int
	if err := st.s.DB().QueryRowContext(ctx, `select count(*) from account`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
