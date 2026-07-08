// Package media downloads content-addressed media bytes for the feed
// archiver. Instagram's CDN URLs (scontent.cdninstagram.com, fbcdn.net) are
// signed and expire, so bytes must be captured in the same pass that
// discovers them. Downloader does that capture: it streams each URL to disk
// and names the result by its SHA-256 hash, so bytes already stored under a
// hash — whether from an earlier target in the same batch or a previous run
// — are never written to disk twice.
package media

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// defaultConcurrency is how many downloads run at once when the caller
// doesn't set WithConcurrency.
const defaultConcurrency = 4

// defaultTimeout bounds a single download. Media files can be large, so this
// is generous rather than tight.
const defaultTimeout = 60 * time.Second

// Target is one piece of media to fetch: a signed, expiring CDN URL plus the
// metadata already known about it. Kind is "image" or "video".
type Target struct {
	URL    string
	Kind   string
	Width  int
	Height int
}

// Result is the outcome of downloading one Target. Exactly one shape applies:
//   - success: Hash, LocalPath, and Size are set; Missing is false and Err is
//     nil.
//   - expired: the signed CDN URL had already expired, or the media is gone
//     (HTTP 403 or 404). Missing is true and Err is nil — this is an expected
//     outcome, not a failure, so the caller keeps the target's metadata
//     without its bytes.
//   - failure: Err is non-nil (transport error, unexpected HTTP status, or a
//     byte count that didn't match a declared Content-Length).
type Result struct {
	URL       string
	Hash      string
	LocalPath string
	Size      int64
	Missing   bool
	Err       error
}

// Downloader fetches media bytes over HTTP and stores them content-addressed
// on disk. Build one with New; the zero value is not usable.
type Downloader struct {
	httpClient  *http.Client
	dir         string
	concurrency int
}

// Option configures a Downloader built by New.
type Option func(*Downloader)

// WithHTTPClient injects the HTTP client, so tests can point Downloader at an
// httptest.Server (or any custom RoundTripper) instead of the real network. A
// nil client is ignored.
func WithHTTPClient(c *http.Client) Option {
	return func(d *Downloader) {
		if c != nil {
			d.httpClient = c
		}
	}
}

// WithConcurrency sets how many downloads run at once. Values less than 1 are
// ignored, leaving the default in place.
func WithConcurrency(n int) Option {
	return func(d *Downloader) {
		if n > 0 {
			d.concurrency = n
		}
	}
}

// New builds a Downloader that stores files under dir (created lazily on the
// first Download call). The default concurrency is 4 and the default HTTP
// client carries a generous fixed timeout; both are overridable via Option.
func New(dir string, opts ...Option) *Downloader {
	d := &Downloader{
		httpClient:  &http.Client{Timeout: defaultTimeout},
		dir:         dir,
		concurrency: defaultConcurrency,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Dir returns the root directory Downloader stores files under.
func (d *Downloader) Dir() string { return d.dir }

// Download fetches every target through a bounded pool of d.concurrency
// worker goroutines and returns one Result per target, in the same order as
// targets.
func (d *Downloader) Download(ctx context.Context, targets []Target) []Result {
	results := make([]Result, len(targets))
	if len(targets) == 0 {
		return results
	}
	if err := os.MkdirAll(d.dir, 0o755); err != nil {
		for i, t := range targets {
			results[i] = Result{URL: t.URL, Err: fmt.Errorf("create download dir %s: %w", d.dir, err)}
		}
		return results
	}

	type job struct {
		index  int
		target Target
	}
	jobs := make(chan job)

	var wg sync.WaitGroup
	wg.Add(d.concurrency)
	for i := 0; i < d.concurrency; i++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				results[j.index] = d.fetch(ctx, j.target)
			}
		}()
	}
	for i, t := range targets {
		jobs <- job{index: i, target: t}
	}
	close(jobs)
	wg.Wait()

	return results
}

// fetch downloads a single target and classifies the outcome. Only a genuine
// transport failure (no HTTP response at all), an unexpected HTTP status, or
// an I/O error while storing the bytes ends up in Result.Err; an
// expired/gone CDN URL is reported via Result.Missing instead.
func (d *Downloader) fetch(ctx context.Context, t Target) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return Result{URL: t.URL, Err: fmt.Errorf("build request for %s: %w", t.URL, err)}
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return Result{URL: t.URL, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		// The signed CDN URL already expired (or the media is gone). Expected
		// in a crawler that revisits old posts, so it is not an error.
		return Result{URL: t.URL, Missing: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{URL: t.URL, Err: fmt.Errorf("download %s: unexpected status %d", t.URL, resp.StatusCode)}
	}

	hash, size, path, err := d.store(resp.Body, resp.ContentLength)
	if err != nil {
		return Result{URL: t.URL, Err: fmt.Errorf("download %s: %w", t.URL, err)}
	}
	return Result{URL: t.URL, Hash: hash, LocalPath: path, Size: size}
}
