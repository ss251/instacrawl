package carve

import "testing"

func TestCanonicalKey(t *testing.T) {
	// A realistic IG CDN basename that passes the plausibility guard.
	const name = "453992915_17862345678901234_n.jpg"
	cases := []struct {
		desc string
		url  string
		want string
	}{
		{"plain cdn url", "https://scontent.cdninstagram.com/v/t51.2885-15/" + name, name},
		{"signed query dropped", "https://scontent.cdninstagram.com/v/t51/" + name + "?stp=dst-jpg&_nc_ht=x&oh=deadbeef&oe=1234", name},
		{"size-variant prefix collapses", "https://scontent.cdninstagram.com/v/t51/e35/s1080x1080/" + name, name},
		{"host irrelevant", "https://scontent-lax3-1.xx.fbcdn.net/o1/v/" + name, name},
		{"uppercase normalized", "https://cdn/V/" + "453992915_17862345678901234_N.JPG", "453992915_17862345678901234_n.jpg"},
		{"generic short name rejected", "https://cdn/image.jpg", ""},
		{"no underscore rejected", "https://cdn/photograph1234567890.jpg", ""},
		{"non-media extension rejected", "https://cdn/453992915_17862345678901234_n.txt", ""},
		{"empty", "", ""},
		{"no path", "https://cdn", ""},
		{"garbage", "::::not a url", ""},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if got := CanonicalKey(c.url); got != c.want {
				t.Fatalf("CanonicalKey(%q) = %q, want %q", c.url, got, c.want)
			}
		})
	}
}

func TestKeyFromName(t *testing.T) {
	const name = "453992915_17862345678901234_n.jpg"
	cases := []struct {
		desc string
		in   string
		want string
	}{
		{"bare basename", name, name},
		{"unix path", "/var/cache/ig/" + name, name},
		{"windows path", `C:\Users\me\Cache\` + name, name},
		{"uppercase normalized", "453992915_17862345678901234_N.JPG", name},
		{"generic rejected", "image.jpg", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if got := KeyFromName(c.in); got != c.want {
				t.Fatalf("KeyFromName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The two attribution paths must agree: a filename that passes the guard yields
// the exact key its CDN URL would, or the index and the walker never match.
func TestCanonicalKeyAndNameAgree(t *testing.T) {
	const name = "453992915_17862345678901234_n.jpg"
	fromURL := CanonicalKey("https://scontent.cdninstagram.com/v/t51/e35/s1080x1080/" + name + "?oe=abc")
	fromName := KeyFromName("/tmp/cache/" + name)
	if fromURL == "" || fromURL != fromName {
		t.Fatalf("attribution keys disagree: url=%q name=%q", fromURL, fromName)
	}
}
