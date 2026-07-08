package feedsync

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/media"
	"github.com/ss251/instacrawl/internal/pace"
	"github.com/ss251/instacrawl/internal/session"
	"github.com/ss251/instacrawl/internal/store"
)

// replayFetcher serves canned feed JSON keyed on request path and records the
// order of calls so tests can assert stories are fetched first.
type replayFetcher struct {
	respond func(path string) ig.RawResponse
	calls   []string
}

func (f *replayFetcher) FetchSelfProfile(context.Context, *session.WebBundle) (ig.RawResponse, error) {
	return ig.RawResponse{}, nil
}
func (f *replayFetcher) Fetch(_ context.Context, _ *session.WebBundle, req ig.Request) (ig.RawResponse, error) {
	f.calls = append(f.calls, req.Path)
	return f.respond(req.Path), nil
}
func (f *replayFetcher) Name() string { return "replay" }

func jsonResp(body string) ig.RawResponse {
	return ig.RawResponse{Status: 200, ContentType: "application/json", Body: []byte(body)}
}

// always builds a replayFetcher that returns the same response for every path.
func always(r ig.RawResponse) *replayFetcher {
	return &replayFetcher{respond: func(string) ig.RawResponse { return r }}
}

// mediaStub serves fixed bytes per URL path, or 403 for expired paths.
type mediaStub struct {
	bytesByPath map[string][]byte
	forbidden   map[string]bool
}

func (m *mediaStub) RoundTrip(req *http.Request) (*http.Response, error) {
	p := req.URL.Path
	mk := func(status int, b []byte) *http.Response {
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(b)), Header: http.Header{}, ContentLength: int64(len(b))}
	}
	if m.forbidden[p] {
		return mk(403, nil), nil
	}
	if b, ok := m.bytesByPath[p]; ok {
		return mk(200, b), nil
	}
	return mk(404, nil), nil
}

func tempStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func bundle() *session.WebBundle {
	b := &session.WebBundle{SessionID: "1%3Afake", DSUserID: "1"}
	b.Normalize()
	return b
}

func deps(f ig.Fetcher, st *store.Store, dl *media.Downloader) Deps {
	return Deps{Fetcher: f, Store: st, Bundle: bundle(), Pacer: pace.NewForTest(), Media: dl, DSUserID: "1"}
}

func mediaDL(t *testing.T, stub *mediaStub) *media.Downloader {
	t.Helper()
	return media.New(t.TempDir(), media.WithHTTPClient(&http.Client{Transport: stub}), media.WithConcurrency(2))
}

// one photo media, url points at /a.jpg
func ownFeedOne() string {
	return `{"num_results":1,"more_available":false,"items":[
	  {"pk":"m1","code":"c1","media_type":1,"taken_at":100,
	   "image_versions2":{"candidates":[{"url":"http://cdn.test/a.jpg","width":1080,"height":1080}]}}]}`
}

// --- The Phase 3 gate: two runs, run-2 zero new + zero bytes downloaded. ---

func TestFeedIdempotencyGate(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	stub := &mediaStub{bytesByPath: map[string][]byte{"/a.jpg": []byte("PIXELS-A")}}
	f := always(jsonResp(ownFeedOne()))

	s1, err := Archive(ctx, deps(f, st, mediaDL(t, stub)))
	if err != nil {
		t.Fatal(err)
	}
	if s1.State != "OK" || s1.MediaNew != 1 || s1.BlobsDownloaded != 1 {
		t.Fatalf("run1: %+v", s1)
	}

	// Run 2: head pk m1 == stored NewestPK → unchanged → 0 new, 0 downloads.
	s2, err := Archive(ctx, deps(f, st, mediaDL(t, stub)))
	if err != nil {
		t.Fatal(err)
	}
	if s2.MediaNew != 0 || s2.BlobsDownloaded != 0 {
		t.Fatalf("run2 must be a snapshot no-op: %+v", s2)
	}
	if n, _ := st.CountMedia(ctx); n != 1 {
		t.Fatalf("expected 1 media total, got %d", n)
	}
}

// --- Byte dedup: two media with identical bytes → one blob. ---

func TestBlobDedup(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	// Two media, both referencing /shared.jpg (identical bytes).
	page := `{"num_results":2,"more_available":false,"items":[
	  {"pk":"m1","media_type":1,"taken_at":200,"image_versions2":{"candidates":[{"url":"http://cdn.test/shared.jpg","width":1,"height":1}]}},
	  {"pk":"m2","media_type":1,"taken_at":100,"image_versions2":{"candidates":[{"url":"http://cdn.test/shared.jpg","width":1,"height":1}]}}]}`
	stub := &mediaStub{bytesByPath: map[string][]byte{"/shared.jpg": []byte("IDENTICAL")}}
	f := always(jsonResp(page))

	if _, err := Archive(ctx, deps(f, st, mediaDL(t, stub))); err != nil {
		t.Fatal(err)
	}
	m, _ := st.CountMedia(ctx)
	b, _ := st.CountBlobs(ctx)
	if m != 2 || b != 1 {
		t.Fatalf("identical bytes across 2 media must dedup to 1 blob: media=%d blobs=%d", m, b)
	}
}

// --- Expired media (403) is flagged missing, item not failed, run OK. ---

func TestExpiredMediaMissing(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	stub := &mediaStub{forbidden: map[string]bool{"/a.jpg": true}}
	f := always(jsonResp(ownFeedOne()))

	s, err := Archive(ctx, deps(f, st, mediaDL(t, stub)))
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "OK" {
		t.Fatalf("an expired CDN url must not fail the run: %+v", s)
	}
	if n, _ := st.CountMedia(ctx); n != 1 {
		t.Fatalf("media metadata must be stored even when bytes expired, got %d", n)
	}
	if b, _ := st.CountBlobs(ctx); b != 0 {
		t.Fatalf("no blob should exist for an expired url, got %d", b)
	}
}

// --- Liked 400 → unavailable, run OK, other feeds still archived. ---

func TestLikedUnavailable(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	f := &replayFetcher{respond: func(path string) ig.RawResponse {
		if strings.Contains(path, "/liked/") {
			return ig.RawResponse{Status: 400, ContentType: "application/json", Body: []byte(`{"message":"...","status":"fail"}`)}
		}
		return jsonResp(ownFeedOne())
	}}
	d := deps(f, st, mediaDL(t, &mediaStub{bytesByPath: map[string][]byte{"/a.jpg": []byte("X")}}))
	d.IncludeLiked = true
	s, err := Archive(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if !s.LikedUnavailable || s.State != "OK" {
		t.Fatalf("liked 400 must be best-effort unavailable, run OK: %+v", s)
	}
}

// --- Carousel → media_child edges + child media rows. ---

func TestCarousel(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	page := `{"num_results":1,"more_available":false,"items":[
	  {"pk":"car","media_type":8,"taken_at":300,"carousel_media":[
	    {"pk":"child0","media_type":1,"image_versions2":{"candidates":[{"url":"http://cdn.test/c0.jpg","width":1,"height":1}]}},
	    {"pk":"child1","media_type":1,"image_versions2":{"candidates":[{"url":"http://cdn.test/c1.jpg","width":1,"height":1}]}}]}]}`
	stub := &mediaStub{bytesByPath: map[string][]byte{"/c0.jpg": []byte("c0"), "/c1.jpg": []byte("c1")}}
	f := always(jsonResp(page))
	if _, err := Archive(ctx, deps(f, st, mediaDL(t, stub))); err != nil {
		t.Fatal(err)
	}
	// parent + 2 children = 3 media rows; 2 distinct child blobs.
	if m, _ := st.CountMedia(ctx); m != 3 {
		t.Fatalf("carousel should yield 3 media rows (parent+2), got %d", m)
	}
	if b, _ := st.CountBlobs(ctx); b != 2 {
		t.Fatalf("expected 2 child blobs, got %d", b)
	}
}

// --- Stories are fetched before the paginated feeds. ---

func TestStoriesFirst(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	story := `{"reel":{"items":[{"pk":"s1","media_type":1,"taken_at":400,
	  "image_versions2":{"candidates":[{"url":"http://cdn.test/s.jpg","width":1,"height":1}]}}]}}`
	f := &replayFetcher{respond: func(path string) ig.RawResponse {
		if strings.Contains(path, "/story/") {
			return jsonResp(story)
		}
		return jsonResp(ownFeedOne())
	}}
	stub := &mediaStub{bytesByPath: map[string][]byte{"/s.jpg": []byte("s"), "/a.jpg": []byte("a")}}
	d := deps(f, st, mediaDL(t, stub))
	d.IncludeStories = true
	s, err := Archive(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if s.Stories != 1 {
		t.Fatalf("expected 1 story, got %d", s.Stories)
	}
	// The first call must be the story endpoint.
	if len(f.calls) == 0 || !strings.Contains(f.calls[0], "/story/") {
		t.Fatalf("stories must be fetched first; call order = %v", f.calls)
	}
}

// --- Unknown media_type isolates that feed as drift; run does not halt. ---

func TestUnknownMediaTypeDrift(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	page := `{"num_results":1,"more_available":false,"items":[{"pk":"weird","media_type":99,"taken_at":1}]}`
	f := always(jsonResp(page))
	s, err := Archive(ctx, deps(f, st, nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "SCHEMA·DRIFT" || len(s.DriftedResources) != 1 || s.DriftedResources[0] != "own_feed" {
		t.Fatalf("unknown media_type should isolate own_feed as drift: %+v", s)
	}
}
