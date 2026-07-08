package media

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestDownloadStoresContentAddressedBytes(t *testing.T) {
	payload := []byte("hello media bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := New(dir, WithHTTPClient(srv.Client()))

	results := d.Download(t.Context(), []Target{{URL: srv.URL, Kind: "image"}})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}
	if r.Missing {
		t.Fatal("Missing = true, want false")
	}

	want := sha256Hex(payload)
	if r.Hash != want {
		t.Fatalf("hash = %q, want %q", r.Hash, want)
	}
	if r.Size != int64(len(payload)) {
		t.Fatalf("size = %d, want %d", r.Size, len(payload))
	}
	wantPath := filepath.Join(dir, want[:2], want[2:4], want)
	if r.LocalPath != wantPath {
		t.Fatalf("local path = %q, want %q", r.LocalPath, wantPath)
	}
	got, err := os.ReadFile(r.LocalPath)
	if err != nil {
		t.Fatalf("read local path: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("stored bytes = %q, want %q", got, payload)
	}
}

func TestDownloadDedupsIdenticalBytesAcrossURLs(t *testing.T) {
	payload := []byte("identical bytes served from two different routes")
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) })
	mux.HandleFunc("/b", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	d := New(dir, WithHTTPClient(srv.Client()))

	results := d.Download(t.Context(), []Target{
		{URL: srv.URL + "/a", Kind: "image"},
		{URL: srv.URL + "/b", Kind: "image"},
	})
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("result %d: unexpected error: %v", i, r.Err)
		}
	}
	if results[0].Hash == "" || results[0].Hash != results[1].Hash {
		t.Fatalf("hashes differ: %q vs %q", results[0].Hash, results[1].Hash)
	}
	if results[0].LocalPath != results[1].LocalPath {
		t.Fatalf("local paths differ: %q vs %q", results[0].LocalPath, results[1].LocalPath)
	}

	shardDir := filepath.Join(dir, results[0].Hash[:2], results[0].Hash[2:4])
	entries, err := os.ReadDir(shardDir)
	if err != nil {
		t.Fatalf("read shard dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("shard dir has %d entries, want 1: %v", len(entries), names)
	}
}

func TestDownloadMarksExpiredCDNURLAsMissing(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer srv.Close()

			dir := t.TempDir()
			d := New(dir, WithHTTPClient(srv.Client()))

			results := d.Download(t.Context(), []Target{{URL: srv.URL, Kind: "image"}})
			r := results[0]
			if !r.Missing {
				t.Fatal("Missing = false, want true")
			}
			if r.Err != nil {
				t.Fatalf("Err = %v, want nil", r.Err)
			}
			if r.LocalPath != "" {
				t.Fatalf("LocalPath = %q, want empty", r.LocalPath)
			}

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("read dir: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("expected no files written, found %v", entries)
			}
		})
	}
}

func TestDownloadFailsOnUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := New(dir, WithHTTPClient(srv.Client()))

	results := d.Download(t.Context(), []Target{{URL: srv.URL, Kind: "image"}})
	r := results[0]
	if r.Err == nil {
		t.Fatal("Err = nil, want non-nil")
	}
	if r.Missing {
		t.Fatal("Missing = true, want false")
	}
}

func TestDownloadReturnsAllResultsInOrder(t *testing.T) {
	const n = 12
	mux := http.NewServeMux()
	for i := 0; i < n; i++ {
		mux.HandleFunc(fmt.Sprintf("/item-%d", i), func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, "payload-%d", i)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	d := New(dir, WithHTTPClient(srv.Client()), WithConcurrency(4))

	targets := make([]Target, n)
	for i := 0; i < n; i++ {
		targets[i] = Target{URL: fmt.Sprintf("%s/item-%d", srv.URL, i), Kind: "image"}
	}

	results := d.Download(t.Context(), targets)
	if len(results) != n {
		t.Fatalf("got %d results, want %d", len(results), n)
	}
	for i, r := range results {
		if r.URL != targets[i].URL {
			t.Fatalf("result %d URL = %q, want %q", i, r.URL, targets[i].URL)
		}
		if r.Err != nil {
			t.Fatalf("result %d: unexpected error: %v", i, r.Err)
		}
		want := sha256Hex([]byte(fmt.Sprintf("payload-%d", i)))
		if r.Hash != want {
			t.Fatalf("result %d hash = %q, want %q", i, r.Hash, want)
		}
	}
}
