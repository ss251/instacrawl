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

	hits, err := st.Search(ctx, terms, *typ, *limit)
	if err != nil {
		if errors.Is(err, store.ErrEmptyQuery) {
			return usageError{msg: "search query has no searchable terms"}
		}
		return fmt.Errorf("search: %w", err)
	}

	if useJSON {
		return output.Write(os.Stdout, output.JSON, "search", toSearchOutput(hits))
	}
	printSearchHits(hits)
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
			Entity: h.Entity, Key: h.Key, ThreadID: h.ThreadID, Code: h.Code,
			Timestamp: formatHitTime(h.TimestampUS), Unsent: h.Unsent, Snippet: h.Snippet, Score: h.Score,
		})
	}
	return out
}

func printSearchHits(hits []store.SearchHit) {
	for _, h := range hits {
		ts := formatHitTime(h.TimestampUS)
		if ts == "" {
			ts = "-"
		}
		var loc string
		switch h.Entity {
		case "dm":
			loc = fmt.Sprintf("thread=%s item=%s", h.ThreadID, h.Key)
			if h.Unsent {
				loc += " (unsent)"
			}
		case "media":
			loc = fmt.Sprintf("code=%s pk=%s", h.Code, h.Key)
		}
		fmt.Printf("%-6s %s  %s  %q\n", h.Entity, ts, loc, h.Snippet)
	}
}

// formatHitTime renders a microsecond timestamp as RFC3339 UTC, or "" for a
// zero/absent timestamp (e.g. media with no taken_at).
func formatHitTime(us int64) string {
	if us == 0 {
		return ""
	}
	return time.UnixMicro(us).UTC().Format(time.RFC3339)
}
