// Package session models the Instagram web-session bundle and stores it in the
// OS keychain. It is web-lane-specific by design: the mobile lane's
// fabricated-device bundle is a separate type that lands in Phase 5, because
// prematurely unifying the two leaks the header/auth differences between the
// surfaces (see the blindspot recon).
//
// This package NEVER imports net/http — it is pure data + secret storage, so it
// can be unit-tested with no network and (via indirected keyring vars) no real
// keychain.
package session

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

// WebAppID is the well-known Instagram *web* application id. It is not a secret
// (it is baked into instagram.com's own JS) and is required on the web API.
const WebAppID = "936619743392459"

// DefaultUserAgent is a plausible desktop web User-Agent. It must stay
// internally consistent with the web app-id; a mismatched UA is a checkpoint
// trigger. Users may override it when importing a bundle.
const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// WebBundle is the complete web-session credential set. Every part must travel
// together or requests silently 400/403. The whole struct marshals to a single
// JSON string for keychain storage.
type WebBundle struct {
	// SessionID is THE bearer credential — full account access. Secret.
	SessionID string `json:"sessionid"`
	// DSUserID is the authenticated user id. It equals the sessionid's prefix
	// and is the identity the probe asserts the API agrees with.
	DSUserID string `json:"ds_user_id"`
	// CSRFToken mirrors the csrftoken cookie; required on POST and some GET.
	// Optional for a read-only GET probe.
	CSRFToken string `json:"csrftoken,omitempty"`
	Mid       string `json:"mid,omitempty"`
	Rur       string `json:"rur,omitempty"`
	IGDID     string `json:"ig_did,omitempty"`
	// UserAgent must be consistent with AppID.
	UserAgent string `json:"user_agent"`
	// AppID is the web X-IG-App-ID.
	AppID string `json:"app_id"`

	// WWWClaim is the rotating x-ig-www-claim token the server issues. It is the
	// one mutable field: the Phase 1 probe captures it but does NOT persist it
	// back (no echo loop yet — that is Phase 2). The field exists now so Phase 2
	// does not reshape the type.
	WWWClaim string `json:"x_ig_www_claim,omitempty"`

	// Acquired records HOW the bundle was obtained ("paste"|"cookiefile"|"json"
	// |"env"). Note: this is provenance of the SECRET, deliberately distinct
	// from the reliability lane tag source='web' on archived rows — two
	// different "source" concepts, kept named apart.
	Acquired   string    `json:"acquired,omitempty"`
	ImportedAt time.Time `json:"imported_at,omitempty"`
}

// Normalize fills defaults (AppID, UserAgent) and derives DSUserID from the
// sessionid when it is absent. It mutates the receiver in place.
func (b *WebBundle) Normalize() {
	b.SessionID = strings.TrimSpace(b.SessionID)
	b.CSRFToken = strings.TrimSpace(b.CSRFToken)
	b.DSUserID = strings.TrimSpace(b.DSUserID)
	if b.AppID == "" {
		b.AppID = WebAppID
	}
	if b.UserAgent == "" {
		b.UserAgent = DefaultUserAgent
	}
	if b.DSUserID == "" {
		b.DSUserID = deriveUserID(b.SessionID)
	}
}

// Validate returns an error if the bundle cannot possibly authenticate. It is
// intentionally lenient about the optional cookies (csrf/mid/rur) — only the
// sessionid is load-bearing for a read probe — but it does require that an
// identity can be established, so the probe has something to assert against.
func (b *WebBundle) Validate() error {
	if strings.TrimSpace(b.SessionID) == "" {
		return fmt.Errorf("session bundle is missing sessionid (the bearer credential)")
	}
	if strings.TrimSpace(b.DSUserID) == "" {
		return fmt.Errorf("session bundle has no ds_user_id and none could be derived from sessionid")
	}
	return nil
}

// DeriveUserID returns the numeric user id encoded in the sessionid prefix, or
// "" if it cannot be parsed. Instagram sessionids look like
// "<userid>%3A<opaque>" (%3A is a colon). This is a best-effort *expectation*;
// the probe's server-returned pk is the source of truth and is cross-checked
// against it.
func (b *WebBundle) DeriveUserID() string { return deriveUserID(b.SessionID) }

func deriveUserID(sessionID string) string {
	s := strings.TrimSpace(sessionID)
	if s == "" {
		return ""
	}
	// The cookie value is usually URL-encoded (%3A for ':'); decode first.
	if dec, err := url.QueryUnescape(s); err == nil {
		s = dec
	}
	id, _, found := strings.Cut(s, ":")
	if !found {
		return ""
	}
	id = strings.TrimSpace(id)
	if id == "" || !isAllDigits(id) {
		return ""
	}
	return id
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// RedactedBundle is the log/JSON-safe projection of a WebBundle: identity and
// metadata in the clear, secrets reduced to presence booleans. It is what
// `auth status --json` emits.
type RedactedBundle struct {
	DSUserID     string    `json:"ds_user_id"`
	UserAgent    string    `json:"user_agent"`
	AppID        string    `json:"app_id"`
	Acquired     string    `json:"acquired,omitempty"`
	ImportedAt   time.Time `json:"imported_at,omitempty"`
	HasSessionID bool      `json:"has_sessionid"`
	HasCSRFToken bool      `json:"has_csrftoken"`
	HasMid       bool      `json:"has_mid"`
	HasRur       bool      `json:"has_rur"`
	HasIGDID     bool      `json:"has_ig_did"`
	HasWWWClaim  bool      `json:"has_x_ig_www_claim"`
}

// Redacted returns the secret-free projection.
func (b *WebBundle) Redacted() RedactedBundle {
	return RedactedBundle{
		DSUserID:     b.DSUserID,
		UserAgent:    b.UserAgent,
		AppID:        b.AppID,
		Acquired:     b.Acquired,
		ImportedAt:   b.ImportedAt,
		HasSessionID: strings.TrimSpace(b.SessionID) != "",
		HasCSRFToken: strings.TrimSpace(b.CSRFToken) != "",
		HasMid:       strings.TrimSpace(b.Mid) != "",
		HasRur:       strings.TrimSpace(b.Rur) != "",
		HasIGDID:     strings.TrimSpace(b.IGDID) != "",
		HasWWWClaim:  strings.TrimSpace(b.WWWClaim) != "",
	}
}

// String renders the bundle without any secret value. This is the ONLY way a
// bundle should ever reach a log line or an error string.
func (b *WebBundle) String() string {
	return fmt.Sprintf("WebBundle{ds_user_id=%s, sessionid=%s, csrftoken=%s, ua=%q}",
		b.DSUserID, present(b.SessionID), present(b.CSRFToken), b.UserAgent)
}

// LogValue makes slog print the redacted form automatically.
func (b *WebBundle) LogValue() slog.Value {
	r := b.Redacted()
	return slog.GroupValue(
		slog.String("ds_user_id", r.DSUserID),
		slog.Bool("has_sessionid", r.HasSessionID),
		slog.Bool("has_csrftoken", r.HasCSRFToken),
		slog.String("acquired", r.Acquired),
	)
}

func present(s string) string {
	if strings.TrimSpace(s) == "" {
		return "absent"
	}
	return "present"
}
