package carve

import (
	"net/url"
	"path"
	"strings"
)

// minAttributableLen is the shortest basename we will trust as an Instagram CDN
// media name. IG CDN filenames look like `453992915_17862345678901234_n.jpg` —
// long, and carrying at least one underscore. The guard exists so the
// format-agnostic walker never attributes a generic file (`image.jpg`,
// `avatar.png`, an icon) to a stored media_file by an accidental name collision.
const minAttributableLen = 16

// CanonicalKey reduces a CDN URL to its attribution key: the lowercased path
// basename, with scheme, host, and the signed query string dropped (the query
// differs on every fetch; the basename embeds the stable content id). Instagram
// serves the same content under size-variant path prefixes
// (`…/e35/s1080x1080/NAME.jpg` vs `…/NAME.jpg`), which collapse to one key
// because only the basename is kept. Returns "" when the URL has no path or the
// basename is not a plausible CDN media name.
func CanonicalKey(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Path == "" {
		return ""
	}
	return keyFromBasename(path.Base(u.Path))
}

// KeyFromName reduces a cache-local filename to the same attribution key space
// as CanonicalKey, applying the same plausibility guard. Used for Sources (the
// walker) that know only an on-disk filename, not the original URL.
func KeyFromName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return keyFromBasename(path.Base(filepathToSlash(name)))
}

// keyFromBasename lowercases and guards a basename. Both attribution paths funnel
// through it so a URL-derived key and a filename-derived key are byte-identical
// for the same underlying CDN name.
func keyFromBasename(base string) string {
	base = strings.ToLower(strings.TrimSpace(base))
	if !attributable(base) {
		return ""
	}
	return base
}

// attributable reports whether a basename is a plausible IG CDN media name.
func attributable(base string) bool {
	if len(base) < minAttributableLen {
		return false
	}
	if !strings.ContainsRune(base, '_') {
		return false
	}
	// Must have a media-ish extension, so a stray long underscore-bearing text
	// file is not mistaken for media.
	ext := strings.ToLower(path.Ext(base))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif", ".mp4", ".mov", ".m4v", ".webm":
		return true
	default:
		return false
	}
}

// filepathToSlash normalizes OS path separators to '/' so path.Base works on a
// Windows-style hint too. (We avoid importing path/filepath to keep this file
// trivially OS-independent for tests.)
func filepathToSlash(p string) string { return strings.ReplaceAll(p, "\\", "/") }
