package ig

import (
	"context"
	"net/url"

	"github.com/ss251/instacrawl/internal/session"
)

// Request is a transport-agnostic description of one IG API call. Path is
// host-relative (e.g. "/api/v1/direct_v2/inbox/"); the fetcher supplies the
// host and the session headers/cookies. Query holds pagination params
// (cursor, seq_id, …). Method is reserved — Phase 2 is GET-only.
type Request struct {
	Method string
	Path   string
	Query  url.Values
}

// Fetcher is the transport seam. A Go-native web client (internal/igweb) and an
// instagrapi subprocess (internal/igmobile) both implement it, which is what
// makes the fetch layer HYBRID: the same probe + archiver work regardless of
// which transport ran.
//
// Both methods return a non-nil error ONLY for transport-level failures (DNS,
// connection refused, I/O) — an HTTP error *status* is carried in
// RawResponse.Status for the classifier to judge, never as a Go error. This
// split is deliberate: "the socket succeeded" and "the API is happy" are
// different questions, and only the classifier answers the second.
type Fetcher interface {
	// FetchSelfProfile issues the read-only self-profile request (Phase 1).
	FetchSelfProfile(ctx context.Context, b *session.WebBundle) (RawResponse, error)
	// Fetch issues an arbitrary GET against the given request (Phase 2+).
	Fetch(ctx context.Context, b *session.WebBundle, req Request) (RawResponse, error)
	// Name identifies the transport for logs and ProbeResult.Transport.
	Name() string
}
