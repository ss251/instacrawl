package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/control"
)

// printStatusHuman renders `status` (no --json) as a concise, readable summary
// instead of the machine control-JSON. The JSON surface is unchanged — it is
// still emitted verbatim for `status --json` (the crawlctl contract).
func printStatusHuman(s control.Status) {
	color := useColor()
	fmt.Printf("%s %s\n", paint("instacrawl", senderSeq, color), paint("· "+s.State, dimSeq, color))
	fmt.Printf("  %s\n", s.Summary)

	threads := countByID(s, "threads")
	messages := countByID(s, "messages")
	media := countByID(s, "media")
	if threads > 0 || messages > 0 {
		parts := make([]string, 0, 3)
		if threads > 0 {
			parts = append(parts, groupInt(threads)+" threads")
		}
		parts = append(parts, groupInt(messages)+" messages")
		if media > 0 {
			parts = append(parts, groupInt(media)+" media")
		}
		fmt.Printf("  %s\n", strings.Join(parts, " · "))
	}
	for _, id := range []string{"own_posts", "saved", "stories", "collections"} {
		if v := countByID(s, id); v > 0 {
			fmt.Printf("  %s %s\n", groupInt(v), labelByID(s, id))
		}
	}

	if s.DatabasePath != "" {
		fmt.Printf("  %s  %s · %s\n", paint("db", dimSeq, color), tildePath(s.DatabasePath), humanBytes(s.DatabaseBytes))
	}
	if s.LastSyncAt != "" {
		fmt.Printf("  %s  %s\n", paint("last sync", dimSeq, color), humanTime(s.LastSyncAt))
	}

	for _, w := range s.Warnings {
		fmt.Printf("  %s %s\n", paint("⚠", "\x1b[33m", color), w)
	}
	for _, e := range s.Errors {
		fmt.Printf("  %s %s\n", paint("✗", "\x1b[31m", color), e)
	}
}

func countByID(s control.Status, id string) int64 {
	for _, c := range s.Counts {
		if c.ID == id {
			return c.Value
		}
	}
	return 0
}

func labelByID(s control.Status, id string) string {
	for _, c := range s.Counts {
		if c.ID == id {
			return c.Label
		}
	}
	return id
}

// groupInt formats an integer with thousands separators (1124 → "1,124").
func groupInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func humanTime(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format("2006-01-02 15:04 MST")
	}
	return s
}
