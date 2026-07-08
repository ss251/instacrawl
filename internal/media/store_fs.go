package media

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BlobStore is the content-addressed byte store: files named by their SHA-256
// hash, sharded two levels deep, written atomically and de-duplicated. It is
// the shared write path for every lane that produces media bytes — the HTTP
// Downloader (web/mobile fetch) and the cache-carve lane both funnel through
// Put, so bytes are hashed, dedup'd, and placed identically regardless of where
// they came from. Build one with NewBlobStore; the zero value is not usable.
type BlobStore struct {
	dir string
}

// NewBlobStore returns a BlobStore rooted at dir (created lazily on the first
// Put).
func NewBlobStore(dir string) *BlobStore { return &BlobStore{dir: dir} }

// Dir returns the root directory the store places files under.
func (b *BlobStore) Dir() string { return b.dir }

// shardPath returns the content-addressed location for hash: two levels of
// two-hex-character shard directories under b.dir, so no single directory
// ends up holding every blob the archiver has ever stored.
func (b *BlobStore) shardPath(hash string) string {
	return filepath.Join(b.dir, hash[:2], hash[2:4], hash)
}

// Put streams r to a temp file inside b.dir while hashing it, so memory use
// stays bounded no matter how large the media is. If wantSize is non-negative
// (a declared Content-Length) and the byte count actually read doesn't match
// it, the temp file is discarded and an error is returned before anything
// reaches the content-addressed store. Otherwise the bytes are atomically
// placed at their hash's destination — see commit. Put creates b.dir if needed
// so a carve run can call it without a preceding Download.
func (b *BlobStore) Put(r io.Reader, wantSize int64) (hash string, size int64, path string, err error) {
	if err := os.MkdirAll(b.dir, 0o755); err != nil {
		return "", 0, "", fmt.Errorf("create blob dir %s: %w", b.dir, err)
	}
	tmp, err := os.CreateTemp(b.dir, "tmp-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	h := sha256.New()
	n, copyErr := io.Copy(tmp, io.TeeReader(r, h))
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("stream body: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("close temp file: %w", closeErr)
	}
	if wantSize >= 0 && wantSize != n {
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("read %d bytes, want %d per Content-Length", n, wantSize)
	}

	sum := hex.EncodeToString(h.Sum(nil))
	dest, err := b.commit(tmpPath, sum)
	if err != nil {
		return "", 0, "", err
	}
	return sum, n, dest, nil
}

// commit atomically moves tmpPath into place at hash's content-addressed
// destination, creating the shard directory as needed. If the destination
// already exists — a previous run, or another blob in this batch that hashed
// the same — tmpPath is discarded and the existing path is returned instead: a
// successful dedup, not a re-write, since content-addressing guarantees the two
// files hold identical bytes.
func (b *BlobStore) commit(tmpPath, hash string) (string, error) {
	dest := b.shardPath(hash)

	if _, err := os.Stat(dest); err == nil {
		_ = os.Remove(tmpPath)
		return dest, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("create shard dir for %s: %w", hash, err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("set permissions for %s: %w", hash, err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("rename into place for %s: %w", hash, err)
	}
	return dest, nil
}
