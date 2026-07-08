package media

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// shardPath returns the content-addressed location for hash: two levels of
// two-hex-character shard directories under d.dir, so no single directory
// ends up holding every file the archiver has ever downloaded.
func (d *Downloader) shardPath(hash string) string {
	return filepath.Join(d.dir, hash[:2], hash[2:4], hash)
}

// store streams body to a temp file inside d.dir while hashing it, so memory
// use stays bounded no matter how large the media is. If wantSize is
// non-negative (the response declared a Content-Length) and the byte count
// actually read doesn't match it, the temp file is discarded and an error is
// returned before anything reaches the content-addressed store. Otherwise the
// bytes are atomically placed at their hash's destination — see commit.
func (d *Downloader) store(body io.Reader, wantSize int64) (hash string, size int64, path string, err error) {
	tmp, err := os.CreateTemp(d.dir, "tmp-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	h := sha256.New()
	n, copyErr := io.Copy(tmp, io.TeeReader(body, h))
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
	dest, err := d.commit(tmpPath, sum)
	if err != nil {
		return "", 0, "", err
	}
	return sum, n, dest, nil
}

// commit atomically moves tmpPath into place at hash's content-addressed
// destination, creating the shard directory as needed. If the destination
// already exists — a previous run, or another target in this batch that
// hashed the same — tmpPath is discarded and the existing path is returned
// instead: a successful dedup, not a re-download, since content-addressing
// guarantees the two files hold identical bytes.
func (d *Downloader) commit(tmpPath, hash string) (string, error) {
	dest := d.shardPath(hash)

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
