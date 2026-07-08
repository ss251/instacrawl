package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ss251/instacrawl/internal/carve"
)

// UndownloadedFiles returns every media_file that has no bytes yet but does carry
// an original_url to attribute against — the carve lane's work list.
//
// Eligibility is `downloaded = 0`, NOT `missing = 1`: a media_file can be
// bytes-less for two reasons — an expired 403 (missing=1) OR a transport error
// during the original pass (the downloader's res.Err branch leaves both flags 0).
// Both are equally recoverable from cache, so keying on downloaded=0 catches the
// superset. A row with no original_url is unattributable by filename and is
// skipped (it can never be matched to a cached artifact).
func (st *Store) UndownloadedFiles(ctx context.Context) ([]carve.FileRef, error) {
	rows, err := st.s.DB().QueryContext(ctx, `
select media_pk, kind, position, original_url
from media_file
where downloaded = 0 and original_url is not null and original_url <> ''`)
	if err != nil {
		return nil, fmt.Errorf("query undownloaded media_files: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var refs []carve.FileRef
	for rows.Next() {
		var r carve.FileRef
		if err := rows.Scan(&r.MediaPK, &r.Kind, &r.Position, &r.OriginalURL); err != nil {
			return nil, fmt.Errorf("scan undownloaded media_file: %w", err)
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate undownloaded media_files: %w", err)
	}
	return refs, nil
}

// BackfillMediaFile attaches carved bytes (already written to the blob store
// under hash) to one bytes-less media_file, in a SINGLE transaction: register
// the blob with byte provenance source='carve', then flip the row to downloaded.
//
// This is the ENTIRE carve write path, and it is deliberately narrow — the
// bytes plane reconciles by fill-only-empty (DECISIONS.md §3):
//   - The UPDATE is guarded by `downloaded = 0`, so carve can only transition a
//     row from no-bytes to bytes; it can never replace an existing blob_hash or
//     clobber a live download. That single predicate is both the no-overwrite
//     rule and the idempotency key: a second run over the same cache updates
//     zero rows.
//   - It touches ONLY blob_hash / downloaded / missing. width, height,
//     original_url, and media_file.source (the metadata provenance) are left
//     alone; carve recovers bytes, never metadata.
//
// Returns true when this call actually filled the row (rows affected == 1),
// false when the row was already downloaded (a no-op — expected on re-runs).
func (st *Store) BackfillMediaFile(ctx context.Context, ref carve.FileRef, hash string, size int64) (bool, error) {
	tx, err := st.s.DB().BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin carve tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
insert into media_blob (hash, byte_size, downloaded_at, source) values (?, ?, ?, 'carve')
on conflict(hash) do nothing`, hash, sizeOrNull(size), now); err != nil {
		return false, fmt.Errorf("register carve blob %s: %w", hash, err)
	}

	res, err := tx.ExecContext(ctx, `
update media_file set blob_hash = ?, downloaded = 1, missing = 0
where media_pk = ? and kind = ? and position = ? and downloaded = 0`,
		hash, ref.MediaPK, ref.Kind, ref.Position)
	if err != nil {
		return false, fmt.Errorf("backfill media_file %s/%s/%d: %w", ref.MediaPK, ref.Kind, ref.Position, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit carve tx: %w", err)
	}
	return n == 1, nil
}

// sizeOrNull stores a non-negative byte size, or NULL when the size is unknown
// (a Source that couldn't stat the artifact passes -1).
func sizeOrNull(size int64) any {
	if size < 0 {
		return nil
	}
	return size
}

// CountDownloadedFiles counts media_file rows that have bytes — a gate-test
// helper mirroring the other Count* helpers.
func (st *Store) CountDownloadedFiles(ctx context.Context) (int, error) {
	var n int
	err := st.s.DB().QueryRowContext(ctx, `select count(*) from media_file where downloaded = 1`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count downloaded media_files: %w", err)
	}
	return n, nil
}

// blobSource reports the source recorded for a stored blob (gate-test helper:
// proves carve byte-provenance lands as 'carve', not 'web').
func (st *Store) blobSource(ctx context.Context, hash string) (string, bool, error) {
	var src string
	err := st.s.DB().QueryRowContext(ctx, `select source from media_blob where hash = ?`, hash).Scan(&src)
	switch {
	case err == sql.ErrNoRows:
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("blob source %s: %w", hash, err)
	}
	return src, true, nil
}
