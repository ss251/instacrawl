package ig

import (
	"context"

	"github.com/ss251/instacrawl/internal/session"
)

// Fetcher is the transport seam. A Go-native web client (internal/igweb) and an
// instagrapi subprocess (internal/igmobile) both implement it, which is what
// makes the fetch layer HYBRID: the same probe + classifier work regardless of
// which transport ran.
//
// FetchSelfProfile issues the read-only self-profile request. It returns a
// non-nil error ONLY for transport-level failures (DNS, connection refused,
// I/O) — an HTTP error *status* is not a Go error here; it is carried in
// RawResponse.Status for the classifier to judge. This split is deliberate:
// "the request succeeded at the socket level" and "the API is happy" are
// different questions, and only the classifier answers the second.
type Fetcher interface {
	FetchSelfProfile(ctx context.Context, b *session.WebBundle) (RawResponse, error)
	// Name identifies the transport for logs and ProbeResult.Transport.
	Name() string
}
