package main

import (
	"strings"
	"testing"

	"github.com/ss251/instacrawl/internal/store"
)

func TestGroupInt(t *testing.T) {
	cases := map[int64]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 1124: "1,124", 7007: "7,007", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := groupInt(in); got != want {
			t.Errorf("groupInt(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFriendlyDate(t *testing.T) {
	if got := friendlyDate(0); got != "—" {
		t.Fatalf("zero → %q, want em dash", got)
	}
	// 2026-01-01T00:00:00Z in microseconds.
	if got := friendlyDate(1767225600_000000); got != "Jan 1, 2026" {
		t.Fatalf("friendlyDate = %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{512: "512 B", 548 * 1024: "548 KB", 3 * 1024 * 1024: "3.0 MB"}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestHighlightSnippet(t *testing.T) {
	// Without color the brackets survive (a pipe still shows the match span).
	if got := highlightSnippet("a [japan] b", false); got != "a [japan] b" {
		t.Fatalf("no-color must be unchanged, got %q", got)
	}
	// With color the brackets become ANSI and the word is preserved between them.
	got := highlightSnippet("a [japan] b", true)
	if strings.Contains(got, "[japan]") || !strings.Contains(got, "japan") {
		t.Fatalf("colored snippet must replace brackets but keep the word: %q", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("colored snippet must contain an ANSI escape: %q", got)
	}
}

func TestPadMeasuresRawWidth(t *testing.T) {
	// pad measures the raw (uncolored) text so ANSI escapes don't skew alignment.
	colored := "\x1b[38;5;42m@alex\x1b[0m"
	got := pad(colored, "@alex", 10)
	if !strings.HasPrefix(got, colored) || !strings.HasSuffix(got, "     ") {
		t.Fatalf("pad must append spaces to raw width 10 (5 chars → 5 spaces): %q", got)
	}
	// No padding when the raw text already fills the column.
	if got := pad("@averylonghandle", "@averylonghandle", 10); got != "@averylonghandle" {
		t.Fatalf("overflow must not pad: %q", got)
	}
}

func TestSearchOutputCarriesSender(t *testing.T) {
	out := toSearchOutput([]store.SearchHit{{Entity: "dm", Key: "i1", Sender: "alex", Snippet: "[hi]"}})
	if len(out.Hits) != 1 || out.Hits[0].Sender != "alex" {
		t.Fatalf("sender must flow into the JSON payload: %+v", out.Hits)
	}
}
