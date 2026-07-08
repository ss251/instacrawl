package carvefs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ss251/instacrawl/internal/carve"
)

// write creates a file under dir with the given name and bytes, and stamps a
// fixed mtime so CapturedAt is assertable.
func write(t *testing.T, dir, name string, b []byte, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func collect(t *testing.T, w *Walker) ([]carve.Item, error) {
	t.Helper()
	var items []carve.Item
	_, err := w.Scan(context.Background(), func(it carve.Item) error {
		items = append(items, it)
		return nil
	})
	return items, err
}

const jpg = "453992915_17862345678901234_n.jpg"
const mp4 = "222222222_17862345678901299_n.mp4"

func TestWalkerEmitsPlausibleMediaOnly(t *testing.T) {
	dir := t.TempDir()
	mtime := time.Date(2026, 1, 15, 8, 30, 0, 0, time.UTC)
	write(t, dir, jpg, []byte("JPEGBYTES"), mtime)
	write(t, dir, "sub/"+mp4, []byte("MP4BYTES"), mtime)
	write(t, dir, "image.jpg", []byte("generic"), mtime) // generic name → skip
	write(t, dir, "notes.txt", []byte("text"), mtime)    // non-media ext → skip

	items, err := collect(t, NewWalker([]string{dir}))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		names := make([]string, len(items))
		for i, it := range items {
			names[i] = it.Name
		}
		t.Fatalf("expected 2 plausible media files, got %d: %v", len(items), names)
	}

	byName := map[string]carve.Item{}
	for _, it := range items {
		byName[it.Name] = it
	}
	got, ok := byName[jpg]
	if !ok {
		t.Fatalf("top-level jpg not emitted")
	}
	if _, ok := byName[mp4]; !ok {
		t.Fatalf("nested mp4 not emitted (walk must recurse)")
	}
	// CapturedAt is the file mtime, not run time.
	if !got.CapturedAt.Equal(mtime) {
		t.Fatalf("CapturedAt = %v, want file mtime %v", got.CapturedAt, mtime)
	}
	if got.Size != int64(len("JPEGBYTES")) {
		t.Fatalf("Size = %d, want %d", got.Size, len("JPEGBYTES"))
	}
	// Open yields exactly the file bytes.
	rc, err := got.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(rc)
	if string(b) != "JPEGBYTES" {
		t.Fatalf("Open returned %q, want JPEGBYTES", b)
	}
}

func TestWalkerDoesNotFollowSymlinks(t *testing.T) {
	targetDir := t.TempDir()
	scanDir := t.TempDir()
	target := write(t, targetDir, "outside_17862345678901234_n.jpg", []byte("X"), time.Now())
	if err := os.Symlink(target, filepath.Join(scanDir, jpg)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	items, err := collect(t, NewWalker([]string{scanDir}))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("a symlink (even plausibly named) must not be emitted, got %d", len(items))
	}
}

func TestWalkerMaxItemsIsLoudButKeepsPartial(t *testing.T) {
	dir := t.TempDir()
	mtime := time.Now()
	write(t, dir, "111111111_17862345678901234_n.jpg", []byte("a"), mtime)
	write(t, dir, "222222222_17862345678901234_n.jpg", []byte("b"), mtime)
	write(t, dir, "333333333_17862345678901234_n.jpg", []byte("c"), mtime)

	items, err := collect(t, NewWalker([]string{dir}, WithMaxItems(2)))
	if err == nil {
		t.Fatal("hitting the item cap must return a loud error")
	}
	if len(items) != 2 {
		t.Fatalf("cap must keep exactly the emitted-so-far items, got %d", len(items))
	}
}

func TestWalkerMissingRootIsNotFatal(t *testing.T) {
	items, err := collect(t, NewWalker([]string{filepath.Join(t.TempDir(), "does-not-exist")}))
	if err != nil {
		t.Fatalf("a missing root must be skipped, not fatal: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items from a missing root, got %d", len(items))
	}
}

func TestStubsHonest(t *testing.T) {
	sources := []carve.Source{NewChromiumCache(nil), NewIndexedDB(nil)}
	for _, s := range sources {
		_, err := s.Scan(context.Background(), func(carve.Item) error { return nil })
		if !errors.Is(err, ErrNotImplemented) {
			t.Fatalf("%s stub must refuse with ErrNotImplemented, got %v", s.Name(), err)
		}
	}
}
