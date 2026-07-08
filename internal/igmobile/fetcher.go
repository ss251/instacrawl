// Package igmobile is the instagrapi-subprocess Fetcher — the OPT-IN transport
// for the mobile private API. It plugs into the same ig.Fetcher seam as the
// Go-native web client, which is what makes the fetch layer hybrid.
//
// Phase 1 ships this as a stub: the interface and wiring exist so the seam is
// real and proven, but the actual instagrapi subprocess lands in Phase 5 (the
// mobile lane), where instagrapi's device/challenge handling is the point.
package igmobile

import (
	"context"
	"errors"

	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/session"
)

// ErrNotImplemented explains why the instagrapi transport is unavailable in
// Phase 1 and warns about the safety tradeoff of using it for the web lane.
var ErrNotImplemented = errors.New(
	"the instagrapi fetcher lands in Phase 5 (mobile lane) and is not available yet; " +
		"note that it fabricates a device fingerprint, so routing the web lane through it " +
		"forfeits the web lane's no-device safety advantage — prefer --fetcher=web")

// Fetcher is the (stubbed) instagrapi subprocess transport.
type Fetcher struct{}

// New returns the stub fetcher.
func New() *Fetcher { return &Fetcher{} }

// Name identifies this transport.
func (f *Fetcher) Name() string { return "instagrapi" }

// FetchSelfProfile is not implemented in Phase 1.
func (f *Fetcher) FetchSelfProfile(context.Context, *session.WebBundle) (ig.RawResponse, error) {
	return ig.RawResponse{}, ErrNotImplemented
}

// Fetch is not implemented until the mobile lane lands (Phase 5).
func (f *Fetcher) Fetch(context.Context, *session.WebBundle, ig.Request) (ig.RawResponse, error) {
	return ig.RawResponse{}, ErrNotImplemented
}

// compile-time assertion that the stub satisfies the seam.
var _ ig.Fetcher = (*Fetcher)(nil)
