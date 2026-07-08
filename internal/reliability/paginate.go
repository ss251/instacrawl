package reliability

import "fmt"

// PageGuard bounds a pagination loop independently of the four-state gate. The
// gate decides whether a given response is trustworthy; the guard decides
// whether the loop is allowed to ask for another page at all. These are kept
// separate on purpose: if the channel that reports "am I done paginating" is
// the same one that can be schema-drifted, a drifted response could poison the
// termination decision and spin forever. The guard answers to neither the
// remote nor the parser — only to a hard page cap and to cursor movement.
type PageGuard struct {
	// MaxPages caps how many pages a single resource may fetch in one run.
	// Zero means "unset" and Next will refuse to proceed, because an unbounded
	// paginator is never correct here.
	MaxPages int

	pages    int
	lastSeen map[string]struct{}
}

// NewPageGuard returns a guard that allows at most maxPages fetches per run.
func NewPageGuard(maxPages int) *PageGuard {
	return &PageGuard{MaxPages: maxPages, lastSeen: make(map[string]struct{})}
}

// Next reports whether the loop may fetch another page given the cursor it is
// about to use. It stops the loop when the page cap is hit, when the cursor is
// empty (natural end of pagination), or when the cursor repeats a value already
// seen this run (a non-advancing cursor — the remote handing back the same page
// forever, which no gate would catch because each such page can be perfectly
// shape-valid). The returned error is non-nil only for the cap and repeat cases,
// which are anomalies worth logging; a natural end returns (false, nil).
func (g *PageGuard) Next(cursor string) (proceed bool, err error) {
	if g.MaxPages <= 0 {
		return false, fmt.Errorf("page guard misconfigured: MaxPages must be > 0")
	}
	if cursor == "" {
		return false, nil // natural end of pagination
	}
	if g.pages >= g.MaxPages {
		return false, fmt.Errorf("page cap reached (%d pages) — stopping to avoid an unbounded crawl", g.MaxPages)
	}
	if _, seen := g.lastSeen[cursor]; seen {
		return false, fmt.Errorf("cursor %q did not advance (already fetched this run) — stopping to avoid an infinite loop", cursor)
	}
	g.lastSeen[cursor] = struct{}{}
	g.pages++
	return true, nil
}

// Pages returns how many pages the guard has admitted so far this run.
func (g *PageGuard) Pages() int { return g.pages }
