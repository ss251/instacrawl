// Package ig is the shared Instagram-domain core: the normalized response type,
// the Fetcher seam that lets either a Go-native web client or an instagrapi
// subprocess serve a request, the endpoint contract, and the pure four-state
// classifier that both transports funnel through. It deliberately holds NO
// transport code (no net/http, no os/exec) so the classifier is fully
// fixture-testable with zero network.
package ig

import "github.com/ss251/instacrawl/internal/reliability"

// RawResponse is a transport-agnostic view of a fetched HTTP response. Whichever
// Fetcher ran (Go-native or instagrapi) extracts the few semantically-relevant
// headers into these fields, so the classifier never touches net/http.
type RawResponse struct {
	Status      int    // HTTP status code
	ContentType string // Content-Type header (lowercased)
	Location    string // Location header (for 3xx redirects)
	WWWClaim    string // x-ig-set-www-claim response header (mutable-seam capture)
	RetryAfter  string // Retry-After header, if any
	Body        []byte // response body bytes
}

// Account is the self-profile parsed from a successful probe. Only fields safe
// to persist locally (no email/phone) are captured — see endpoints.go for the
// safe-to-store endpoint-selection rule.
type Account struct {
	PK            string `json:"pk"`
	Username      string `json:"username"`
	FullName      string `json:"full_name"`
	IsPrivate     bool   `json:"is_private"`
	ProfilePicURL string `json:"profile_pic_url"`
	RawJSON       string `json:"-"` // raw probe body, retained for the store's raw_json column
}

// ProbeResult is the full outcome of a read-only probe.
type ProbeResult struct {
	Verdict      reliability.Verdict
	Account      *Account // non-nil only when Verdict.State == StateOK
	ChallengeURL string   // set on AUTH·CHECKPOINT when a challenge URL is discoverable
	RawBody      string   // retained on SCHEMA·DRIFT so a later parser fix can re-derive rows
	WWWClaim     string   // captured x-ig-www-claim (not persisted in Phase 1)
	Transport    string   // which Fetcher produced this ("web"|"instagrapi")
}
