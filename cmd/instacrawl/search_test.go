package main

import (
	"strings"
	"testing"

	"github.com/ss251/instacrawl/internal/store"
)

// All of these fail during flag/argument validation, BEFORE the archive is
// touched, so they run hermetically and return exit 2 (usage).
func TestSearchUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no query", []string{"search"}},
		{"unknown type", []string{"search", "--type", "reels", "aurora"}},
		{"limit too high", []string{"search", "--limit", "9000", "aurora"}},
		{"limit too low", []string{"search", "--limit", "0", "aurora"}},
		{"reindex with terms", []string{"search", "--reindex", "aurora"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(c.args); got != 2 {
				t.Fatalf("run(%v) = %d, want 2 (usage error)", c.args, got)
			}
		})
	}
}

func TestFormatHitTime(t *testing.T) {
	if got := formatHitTime(0); got != "" {
		t.Fatalf("zero timestamp must format to empty, got %q", got)
	}
	// 2026-01-01T00:00:00Z in microseconds.
	if got := formatHitTime(1767225600_000000); got != "2026-01-01T00:00:00Z" {
		t.Fatalf("formatHitTime = %q", got)
	}
}

func TestToSearchOutputShape(t *testing.T) {
	hits := []store.SearchHit{
		{Entity: "dm", Key: "i1", ThreadID: "t1", TimestampUS: 1767225600_000000, Unsent: true, Snippet: "…[aurora]…", Score: -1.2},
		{Entity: "media", Key: "m1", Code: "ABC", Snippet: "[aurora] sky", Score: -0.9},
	}
	out := toSearchOutput(hits)
	if out.Total != 2 || len(out.Hits) != 2 {
		t.Fatalf("total/len mismatch: %+v", out)
	}
	if out.Hits[0].Timestamp != "2026-01-01T00:00:00Z" || !out.Hits[0].Unsent {
		t.Fatalf("dm hit shaped wrong: %+v", out.Hits[0])
	}
	if out.Hits[1].Timestamp != "" {
		t.Fatalf("a zero media timestamp must be omitted, got %q", out.Hits[1].Timestamp)
	}
	// The snippet is present; nothing here should carry the raw query.
	if !strings.Contains(out.Hits[0].Snippet, "aurora") {
		t.Fatalf("snippet lost: %+v", out.Hits[0])
	}
}
