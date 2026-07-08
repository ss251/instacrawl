package ig

import (
	"context"
	"fmt"

	"github.com/ss251/instacrawl/internal/session"
)

// Probe runs a read-only self-profile check through the given Fetcher and
// classifies the result. The Fetcher is injected, which is the hybrid seam: the
// same Probe drives the Go-native web client or the instagrapi subprocess.
//
// A transport-level failure (no response at all — DNS, connection refused) is
// returned as a non-nil error with a zero ProbeResult, deliberately NOT mapped
// to one of the four API states: "the socket died" is a different fact from
// "the API said no", and fabricating an AUTH/RATE/DRIFT verdict for a dead
// network would be dishonest. The caller reports it as an environmental error.
func Probe(ctx context.Context, f Fetcher, b *session.WebBundle) (ProbeResult, error) {
	if f == nil {
		return ProbeResult{}, fmt.Errorf("probe: nil fetcher")
	}
	if b == nil {
		return ProbeResult{}, fmt.Errorf("probe: nil session bundle")
	}
	resp, err := f.FetchSelfProfile(ctx, b)
	if err != nil {
		// Sanitize in case the transport error string embedded a URL/cookie.
		return ProbeResult{}, fmt.Errorf("%s transport failure: %s", f.Name(), session.Sanitize(err.Error()))
	}
	result := Classify(resp, b.DSUserID)
	result.Transport = f.Name()
	return result, nil
}
