package session

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Parse autodetects the input format and returns a normalized WebBundle. It
// accepts, in rough order of how a user is likely to supply it:
//   - a Cookie header string:  "sessionid=…; csrftoken=…; ds_user_id=…"
//   - a Cookie-Editor JSON export: [{"name":"sessionid","value":"…"}, …]
//   - a Netscape cookies.txt file body
//   - instacrawl's own bundle JSON dump: {"sessionid":"…", …}
//
// acquired labels the provenance ("paste"|"cookiefile"|"json"|"env").
func Parse(raw, acquired string) (*WebBundle, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("empty input: nothing to import")
	}
	switch trimmed[0] {
	case '[':
		return ParseJSONExport(trimmed, acquired)
	case '{':
		return parseBundleJSON(trimmed, acquired)
	}
	// Netscape files are tab-delimited, multi-line, and mention the domain.
	if looksLikeNetscape(trimmed) {
		return ParseCookiesTxt(trimmed, acquired)
	}
	return ParseCookieHeader(trimmed, acquired)
}

// ParseCookieHeader parses a "k=v; k=v" cookie header string.
func ParseCookieHeader(raw, acquired string) (*WebBundle, error) {
	cookies := map[string]string{}
	for _, part := range strings.Split(raw, ";") {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		cookies[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
	}
	return BundleFromCookies(cookies, acquired)
}

// ParseCookiesTxt parses a Netscape cookies.txt body, keeping only
// instagram.com cookies.
func ParseCookiesTxt(raw, acquired string) (*WebBundle, error) {
	cookies := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		domain := strings.ToLower(fields[0])
		if !strings.Contains(domain, "instagram.com") {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(fields[5]))
		value := strings.TrimSpace(fields[6])
		cookies[name] = value
	}
	return BundleFromCookies(cookies, acquired)
}

// cookieExport is one entry in a Cookie-Editor-style JSON array.
type cookieExport struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
}

// ParseJSONExport parses a Cookie-Editor JSON array export.
func ParseJSONExport(raw, acquired string) (*WebBundle, error) {
	var entries []cookieExport
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parse cookie JSON export: %w", err)
	}
	cookies := map[string]string{}
	for _, e := range entries {
		if e.Domain != "" && !strings.Contains(strings.ToLower(e.Domain), "instagram.com") {
			continue
		}
		cookies[strings.ToLower(strings.TrimSpace(e.Name))] = strings.TrimSpace(e.Value)
	}
	return BundleFromCookies(cookies, acquired)
}

// parseBundleJSON parses instacrawl's own {"sessionid":…} bundle dump.
func parseBundleJSON(raw, acquired string) (*WebBundle, error) {
	var b WebBundle
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return nil, fmt.Errorf("parse bundle JSON: %w", err)
	}
	if b.Acquired == "" {
		b.Acquired = acquired
	}
	b.Normalize()
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return &b, nil
}

// BundleFromCookies is the single mapper from a set of instagram.com cookie
// name→value pairs to a normalized, validated WebBundle. Every import path — the
// paste/file/JSON parsers above and the --from-browser reader (internal/
// browsercookie) — funnels through here, so the "which cookie fills which field"
// truth lives in exactly one place. Values are stored verbatim (the sessionid is
// URL-encoded on the wire, which is what Instagram expects).
func BundleFromCookies(cookies map[string]string, acquired string) (*WebBundle, error) {
	b := &WebBundle{
		SessionID: cookies["sessionid"],
		DSUserID:  cookies["ds_user_id"],
		CSRFToken: cookies["csrftoken"],
		Mid:       cookies["mid"],
		Rur:       cookies["rur"],
		IGDID:     cookies["ig_did"],
		Acquired:  acquired,
	}
	// Carry every OTHER instagram.com cookie (datr, ps_l, …) as Extra — the web
	// lane is the real browser cookie jar, and some endpoints (direct messages)
	// require cookies the named set omits.
	for name, value := range cookies {
		if namedCookie[name] || strings.TrimSpace(value) == "" {
			continue
		}
		if b.Extra == nil {
			b.Extra = map[string]string{}
		}
		b.Extra[name] = value
	}
	b.Normalize()
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}

// namedCookie is the set of cookies that map to dedicated WebBundle fields;
// everything else instagram.com sets goes into Extra.
var namedCookie = map[string]bool{
	"sessionid": true, "ds_user_id": true, "csrftoken": true,
	"mid": true, "rur": true, "ig_did": true,
}

func looksLikeNetscape(raw string) bool {
	if !strings.Contains(raw, "\t") {
		return false
	}
	return strings.Contains(strings.ToLower(raw), "instagram")
}
