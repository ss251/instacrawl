// Package browsercookie reads the owner's own instagram.com session directly
// from a local browser's on-disk cookie store — the zero-friction import path
// (`instacrawl auth import --from-browser`), in the spirit of the crawlkit
// family reading local app data and of `yt-dlp --cookies-from-browser`.
//
// It is deliberately isolated here so the kooky dependency (and its cookie
// decryption machinery) never leaks into internal/session, which stays a pure
// data + secret-storage core. The secret never leaves this process: a read
// produces a Candidate whose cookies are handed straight to
// session.BundleFromCookies → the OS keychain, and are never printed or logged.
//
// Scope is locked to the legitimate use — the owner's OWN instagram.com session:
// reads are filtered to the instagram.com domain (the whole session cookie jar,
// exactly what the browser itself sends to Instagram — nothing from any other
// site), so this is never a general-purpose cookie exporter.
package browsercookie

import (
	"context"
	"sort"
	"strings"

	"github.com/browserutils/kooky"
	// Register only the browsers we can decrypt on the supported platforms.
	// NOT browser/all — that would compile in Safari/IE/opera/etc. and their deps.
	_ "github.com/browserutils/kooky/browser/brave"
	_ "github.com/browserutils/kooky/browser/chrome"
	_ "github.com/browserutils/kooky/browser/chromium"
	_ "github.com/browserutils/kooky/browser/edge"
	_ "github.com/browserutils/kooky/browser/firefox"

	"github.com/ss251/instacrawl/internal/session"
)

// supportedBrowsers is the set --from-browser can decrypt, in auto-detection
// preference order. Chrome is first because the owner drives it (browser-harness
// attaches to Chrome), so it is the likeliest host of a live session.
var supportedBrowsers = []string{"chrome", "brave", "edge", "chromium", "firefox"}

// Candidate is one (browser, profile) that holds a logged-in instagram.com
// session. Cookies carries the SECRET; it exists only to be handed to
// session.BundleFromCookies and must never be printed or logged.
type Candidate struct {
	Browser  string
	Profile  string
	DSUserID string // non-secret; used for selection and disambiguation display
	Cookies  map[string]string
}

// Source finds browsers/profiles holding a logged-in instagram.com session.
type Source interface {
	// Find scans browser ("auto" = every supported browser in preference order)
	// and returns each candidate holding a valid instagram.com sessionid.
	Find(ctx context.Context, browser string) ([]Candidate, error)
}

// SupportedBrowsers returns the accepted --from-browser names (besides "auto").
func SupportedBrowsers() []string { return append([]string(nil), supportedBrowsers...) }

// IsSupported reports whether name is a recognized --from-browser value.
func IsSupported(name string) bool {
	if name == "auto" {
		return true
	}
	return pref(name) >= 0
}

// pref returns a browser's preference index (lower = higher), or -1 if unsupported.
func pref(browser string) int {
	for i, b := range supportedBrowsers {
		if b == browser {
			return i
		}
	}
	return -1
}

type kookySource struct{}

// NewKookySource returns the production Source backed by kooky.
func NewKookySource() Source { return kookySource{} }

// Find enumerates cookie stores (metadata only — no decryption, no keychain
// prompt), orders them by browser preference then default-profile-first, then
// decrypts browser-group by browser-group and STOPS at the first browser that
// yields a session. That short-circuit bounds the macOS keychain prompt to a
// single browser in the common case.
func (kookySource) Find(ctx context.Context, browser string) ([]Candidate, error) {
	type ref struct {
		store   kooky.CookieStore
		browser string
		def     bool
	}
	var refs []ref
	for store, err := range kooky.TraverseCookieStores(ctx) {
		if err != nil || store == nil {
			continue
		}
		b := strings.ToLower(store.Browser())
		if pref(b) < 0 || (browser != "auto" && b != browser) {
			_ = store.Close()
			continue
		}
		refs = append(refs, ref{store: store, browser: b, def: store.IsDefaultProfile()})
	}
	defer func() {
		for _, r := range refs {
			_ = r.store.Close()
		}
	}()

	sort.SliceStable(refs, func(i, j int) bool {
		if pi, pj := pref(refs[i].browser), pref(refs[j].browser); pi != pj {
			return pi < pj
		}
		return refs[i].def && !refs[j].def // default profile first within a browser
	})

	var out []Candidate
	for i := 0; i < len(refs); {
		b := refs[i].browser
		for i < len(refs) && refs[i].browser == b {
			if c, ok := readCandidate(ctx, refs[i].store, b); ok {
				out = append(out, c)
			}
			i++
		}
		if len(out) > 0 {
			break // first browser (by preference) with a session wins
		}
	}
	return out, nil
}

// readCandidate decrypts one store's instagram.com cookies (this is where the
// keychain prompt happens) and returns a Candidate iff a sessionid is present.
func readCandidate(ctx context.Context, store kooky.CookieStore, browser string) (Candidate, bool) {
	cookies, err := store.TraverseCookies(
		kooky.DomainHasSuffix("instagram.com"),
		kooky.Valid, // skip locally-expired cookies
	).ReadAllCookies(ctx)
	if err != nil || len(cookies) == 0 {
		return Candidate{}, false
	}
	// Carry the whole instagram.com jar (sessionid, datr, …) — the same set the
	// browser sends to Instagram — so every endpoint the session can reach works.
	m := make(map[string]string, len(cookies))
	for _, c := range cookies {
		m[strings.ToLower(c.Name)] = c.Value
	}
	if strings.TrimSpace(m["sessionid"]) == "" {
		return Candidate{}, false
	}
	return Candidate{
		Browser:  browser,
		Profile:  store.Profile(),
		DSUserID: dsUserID(m),
		Cookies:  m,
	}, true
}

// dsUserID reports the account id for display/selection: the ds_user_id cookie,
// or the id encoded in the sessionid prefix. Never secret.
func dsUserID(cookies map[string]string) string {
	if id := strings.TrimSpace(cookies["ds_user_id"]); id != "" {
		return id
	}
	return (&session.WebBundle{SessionID: cookies["sessionid"]}).DeriveUserID()
}
