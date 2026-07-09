package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/crawlkit/output"

	"github.com/ss251/instacrawl/internal/appmeta"
	"github.com/ss251/instacrawl/internal/store"
)

// searchStoreFn is the injectable store-open seam (tests point it at a temp DB).
var searchStoreFn = store.Open

func cmdSearch(jsonFlag bool, args []string) error {
	fs := newAuthFlags("search")
	typ := fs.String("type", "all", "entity to search: dm|media|all")
	limit := fs.Int("limit", 20, "maximum results (1..500)")
	jsonOut := fs.Bool("json", false, "emit results as JSON")
	reindex := fs.Bool("reindex", false, "rebuild the search index, then exit")
	verbose := fs.Bool("verbose", false, "also show the raw thread/item locators")
	if err := fs.Parse(args); err != nil {
		return usageError{msg: err.Error()}
	}
	useJSON := jsonFlag || *jsonOut

	switch *typ {
	case "dm", "media", "all":
	default:
		return usageError{msg: fmt.Sprintf("unknown --type %q (want dm|media|all)", *typ)}
	}
	if *limit < 1 || *limit > 500 {
		return usageError{msg: "--limit must be between 1 and 500"}
	}

	terms := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if *reindex && terms != "" {
		return usageError{msg: "--reindex rebuilds the whole index; do not pass search terms with it"}
	}
	if !*reindex && terms == "" {
		return usageError{msg: "search needs a query — e.g. `instacrawl search aurora`"}
	}

	// A search must never CREATE an archive; require one to already exist.
	dbPath := appmeta.DatabasePath()
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintln(os.Stderr, "instacrawl: no archive yet — run `instacrawl sync` first")
		return errReported
	}

	ctx := context.Background()
	st, err := searchStoreFn(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = st.Close() }()

	if *reindex {
		if err := st.ReindexSearch(ctx); err != nil {
			return fmt.Errorf("reindex: %w", err)
		}
		fmt.Println("Search index rebuilt.")
		return nil
	}

	start := time.Now()
	hits, err := st.Search(ctx, terms, *typ, *limit)
	elapsed := time.Since(start)
	if err != nil {
		if errors.Is(err, store.ErrEmptyQuery) {
			return usageError{msg: "search query has no searchable terms"}
		}
		return fmt.Errorf("search: %w", err)
	}

	if useJSON {
		return output.Write(os.Stdout, output.JSON, "search", toSearchOutput(hits))
	}
	printSearchHits(hits, *verbose, elapsed)
	return nil
}

// searchOutput is the JSON payload. The query string is deliberately NOT echoed.
type searchOutput struct {
	Total int             `json:"total"`
	Hits  []searchHitJSON `json:"hits"`
}

type searchHitJSON struct {
	Entity    string  `json:"entity"`
	Key       string  `json:"key"`
	ThreadID  string  `json:"thread_id,omitempty"`
	Sender    string  `json:"sender,omitempty"`
	Code      string  `json:"code,omitempty"`
	Timestamp string  `json:"timestamp,omitempty"`
	Unsent    bool    `json:"unsent,omitempty"`
	Snippet   string  `json:"snippet"`
	Score     float64 `json:"score"`
}

func toSearchOutput(hits []store.SearchHit) searchOutput {
	out := searchOutput{Total: len(hits), Hits: make([]searchHitJSON, 0, len(hits))}
	for _, h := range hits {
		out.Hits = append(out.Hits, searchHitJSON{
			Entity: h.Entity, Key: h.Key, ThreadID: h.ThreadID, Sender: h.Sender, Code: h.Code,
			Timestamp: formatHitTime(h.TimestampUS), Unsent: h.Unsent, Snippet: h.Snippet, Score: h.Score,
		})
	}
	return out
}

// printSearchHits renders results for a human: who, when, and the matched span
// with the term highlighted — locators (thread/item ids) stay behind --verbose.
func printSearchHits(hits []store.SearchHit, verbose bool, elapsed time.Duration) {
	color := useColor()
	if len(hits) == 0 {
		fmt.Println(paint("no matches.", dimSeq, color))
		return
	}
	for _, h := range hits {
		date := friendlyDate(h.TimestampUS)
		var who string
		switch h.Entity {
		case "dm":
			who = "@" + orElse(h.Sender, "unknown")
		case "media":
			who = "media"
		}
		line := fmt.Sprintf("  %s  %s  %s", pad(paint(who, senderSeq, color), who, 16),
			pad(paint(date, dimSeq, color), date, 13), highlightSnippet(h.Snippet, color))
		if h.Unsent {
			line += paint(" (unsent)", dimSeq, color)
		}
		fmt.Println(line)
		if verbose {
			var loc string
			if h.Entity == "media" {
				loc = fmt.Sprintf("code=%s pk=%s", h.Code, h.Key)
			} else {
				loc = fmt.Sprintf("thread=%s item=%s", h.ThreadID, h.Key)
			}
			fmt.Println(paint("      "+loc, dimSeq, color))
		}
	}
	fmt.Println(paint(fmt.Sprintf("  %d %s in %s", len(hits), plural(len(hits), "match", "matches"), roundDur(elapsed)), dimSeq, color))
}

// formatHitTime renders a microsecond timestamp as RFC3339 UTC, or "" for a
// zero/absent timestamp (e.g. media with no taken_at). Used only for JSON.
func formatHitTime(us int64) string {
	if us == 0 {
		return ""
	}
	return time.UnixMicro(us).UTC().Format(time.RFC3339)
}

// friendlyDate is the human date for a hit ("Mar 11, 2024"), or "—" if absent.
func friendlyDate(us int64) string {
	if us == 0 {
		return "—"
	}
	return time.UnixMicro(us).UTC().Format("Jan 2, 2006")
}

func roundDur(d time.Duration) time.Duration {
	if d < time.Millisecond {
		return d.Round(10 * time.Microsecond)
	}
	return d.Round(time.Millisecond)
}

func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// ANSI sequences for the human search view (never emitted to JSON or a pipe).
const (
	resetSeq  = "\x1b[0m"
	dimSeq    = "\x1b[2m"
	senderSeq = "\x1b[38;5;42m"                // green @handle
	hlOpen    = "\x1b[48;5;222m\x1b[38;5;236m" // amber highlight (bg + dark fg)
	hlClose   = resetSeq
)

// useColor reports whether to emit ANSI color: never when NO_COLOR is set, always
// when FORCE_COLOR is set (honoring the de-facto convention), otherwise only when
// stdout is an interactive terminal.
func useColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func paint(s, seq string, color bool) string {
	if !color || s == "" {
		return s
	}
	return seq + s + resetSeq
}

// pad left-aligns colored to a column of width runes, measuring the uncolored raw
// text so ANSI escapes don't throw off the alignment.
func pad(colored, raw string, width int) string {
	if n := width - len([]rune(raw)); n > 0 {
		return colored + strings.Repeat(" ", n)
	}
	return colored
}

// highlightSnippet turns the FTS bracket markers ([ … ]) into a terminal
// highlight. Without color it leaves the brackets, so a piped result still shows
// which span matched.
func highlightSnippet(s string, color bool) string {
	if !color {
		return s
	}
	s = strings.ReplaceAll(s, "[", hlOpen)
	return strings.ReplaceAll(s, "]", hlClose)
}
