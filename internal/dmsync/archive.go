// Package dmsync is the DM archiver orchestrator: it wires the fetch transport
// (ig.Fetcher), the domain logic (internal/dm), and persistence
// (internal/store) into the inbox→thread→item crawl. It lives in its own
// package (not internal/dm) so that store can depend on dm's types without an
// import cycle.
//
// Crawl policy: the run walks the inbox (and pending inbox) to discover threads,
// then archives each thread with three-way resume so an unchanged thread costs
// ZERO fetches on a re-run. Terminal states are handled by scope: AUTH·CHECKPOINT
// and RATE·LIMITED halt the whole run (the session is bad / we're throttled),
// while a per-thread SCHEMA·DRIFT is isolated — that thread is recorded and
// skipped, the rest continue, and the run exits non-zero at the end. The inbox
// itself drifting halts (we can't enumerate threads).
package dmsync

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ss251/instacrawl/internal/dm"
	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/pace"
	"github.com/ss251/instacrawl/internal/reliability"
	"github.com/ss251/instacrawl/internal/session"
	"github.com/ss251/instacrawl/internal/store"
)

const (
	defaultMaxInboxPages  = 50
	defaultMaxThreadPages = 500
	emptyPageLimit        = 3 // consecutive empty pages on a known thread → drift
)

// Deps are the archiver's injected dependencies.
type Deps struct {
	Fetcher        ig.Fetcher
	Store          *store.Store
	Bundle         *session.WebBundle
	Pacer          *pace.Pacer
	MaxInboxPages  int
	MaxThreadPages int
	IncludePending bool
	FullRecrawl    bool
	OnlyThread     string // if set, archive just this thread (skip the inbox walk)
}

// Summary reports the outcome of a run.
type Summary struct {
	InboxThreads       int
	ThreadsFetched     int
	ThreadsSkipped     int
	MessagesNew        int
	State              string // "OK" | terminal state name
	Reason             string
	ChallengeURL       string
	DriftedThreads     []string
	PendingUnavailable bool // the optional message-requests inbox could not be read
}

// terminal carries a run-halting verdict.
type terminal struct {
	verdict      reliability.Verdict
	challengeURL string
}

// threadOutcome is the per-thread result.
type threadOutcome struct {
	skipped  bool
	drift    *reliability.Verdict
	terminal *terminal
}

// Archive runs the DM crawl.
func Archive(ctx context.Context, d Deps) (Summary, error) {
	if d.MaxInboxPages <= 0 {
		d.MaxInboxPages = defaultMaxInboxPages
	}
	if d.MaxThreadPages <= 0 {
		d.MaxThreadPages = defaultMaxThreadPages
	}
	if d.Pacer == nil {
		d.Pacer = pace.New(0, 0)
	}

	sum := Summary{State: "OK"}
	before, err := d.Store.CountMessages(ctx)
	if err != nil {
		return sum, err
	}

	// 1. Discover threads.
	var threads []dm.Thread
	if d.OnlyThread != "" {
		threads = []dm.Thread{{ThreadID: d.OnlyThread, Source: "web"}}
	} else {
		primary, term, err := walkInbox(ctx, d, false)
		if err != nil {
			return sum, err
		}
		if term != nil {
			return terminalSummary(sum, term), nil
		}
		threads = primary
		if d.IncludePending {
			pending, pterm, err := walkInbox(ctx, d, true)
			if err != nil {
				return sum, err
			}
			if pterm != nil {
				// The pending/requests inbox is OPTIONAL. The main inbox already
				// succeeded, so a terminal here is endpoint-specific (e.g. the
				// account has no requests folder), NOT a bad session — record it and
				// keep archiving the main threads rather than halting the whole run.
				sum.PendingUnavailable = true
			} else {
				threads = append(threads, pending...)
			}
		}
	}
	sum.InboxThreads = len(threads)

	// 2. Archive each thread.
	for _, th := range threads {
		out, err := archiveThread(ctx, d, th)
		if err != nil {
			return sum, err
		}
		switch {
		case out.terminal != nil:
			return terminalSummary(sum, out.terminal), nil // AUTH·CHECKPOINT / RATE·LIMITED → halt
		case out.drift != nil:
			sum.DriftedThreads = append(sum.DriftedThreads, th.ThreadID) // isolate + continue
		case out.skipped:
			sum.ThreadsSkipped++
		default:
			sum.ThreadsFetched++
		}
	}

	after, err := d.Store.CountMessages(ctx)
	if err != nil {
		return sum, err
	}
	sum.MessagesNew = after - before
	if len(sum.DriftedThreads) > 0 {
		sum.State = reliability.StateSchemaDrift.String()
		types := ""
		if m, terr := d.Store.UnknownMessageTypes(ctx); terr == nil && len(m) > 0 {
			types = " [" + formatUnknownTypes(m) + "]"
		}
		sum.Reason = fmt.Sprintf("%d thread(s) carried unrecognized item type(s)%s — messages retained (raw kept, flagged); add the types and run `instacrawl reparse` to recover them without re-fetching", len(sum.DriftedThreads), types)
	}
	return sum, nil
}

// walkInbox pages the (pending) inbox, collecting thread stubs. The PageGuard
// gates the TRANSITION to the next page — the first fetch is unconditional
// (calling guard.Next("") before the first fetch would end immediately).
func walkInbox(ctx context.Context, d Deps, pending bool) ([]dm.Thread, *terminal, error) {
	guard := reliability.NewPageGuard(d.MaxInboxPages)
	var threads []dm.Thread
	cursor, seqID := "", ""
	for {
		req := ig.InboxRequest(cursor, seqID)
		if pending {
			req = ig.PendingInboxRequest(cursor)
		}
		resp, err := d.Fetcher.Fetch(ctx, d.Bundle, req)
		if err != nil {
			return nil, nil, fmt.Errorf("%s inbox transport failure: %s", d.Fetcher.Name(), session.Sanitize(err.Error()))
		}
		res := dm.ClassifyInbox(resp, reliability.Floor{}) // inbox has no known-history floor
		if res.Verdict.State != reliability.StateOK {
			return nil, &terminal{verdict: res.Verdict, challengeURL: res.ChallengeURL}, nil
		}
		threads = append(threads, res.Page.Threads...)
		seqID = res.Page.SeqID

		// PageGuard.Next returns proceed=false on a natural end (empty cursor),
		// the page cap, or a non-advancing cursor. The latter two carry a
		// reason in the error, but all three are a bounded stop, not a run
		// failure — so we stop enumerating without propagating an error.
		proceed, guardErr := guard.Next(res.Page.NextCursor)
		if !proceed {
			_ = guardErr
			break
		}
		cursor = res.Page.NextCursor
		if err := d.Pacer.Wait(ctx); err != nil {
			return nil, nil, err
		}
	}
	return threads, nil, nil
}

// advanceMode selects which cursor frontier a committed page advances. A thread
// has two INDEPENDENT frontiers — the newest-message anchor (head) and the
// backward-backfill cursor (tail) — so a head catch-up must not clobber the
// backfill frontier, and vice-versa.
type advanceMode int

const (
	advanceFull advanceMode = iota // fresh crawl: track both frontiers (head→bottom in one pass)
	advanceHead                    // head catch-up: track only the newest anchor
	advanceTail                    // backfill continuation: track only the oldest frontier
)

// archiveThread crawls one thread with three-way resume across TWO frontiers.
// A fresh/invalidated thread is crawled head→bottom in one pass. A resumed
// thread is handled in up to two phases: a head catch-up (fetch messages newer
// than the stored anchor) and a tail backfill (continue from the stored
// backward frontier toward the start of the thread). The zero-fetch win holds
// only for a thread with no new activity that is ALSO fully backfilled.
func archiveThread(ctx context.Context, d Deps, th dm.Thread) (threadOutcome, error) {
	prev, ok, err := d.Store.GetThreadCursor(ctx, th.ThreadID)
	if err != nil {
		return threadOutcome{}, err
	}
	res := dm.ResolveResume(prev, ok, th.LastActivityUS)
	if d.FullRecrawl {
		res = dm.Invalidated
	}
	// The only zero-fetch case: no new activity AND already fully backfilled. A
	// partially-backfilled thread must continue its backfill even when the head
	// has not moved (this is the bug the two-frontier model fixes).
	if res == dm.Unchanged && prev.Backfilled {
		return threadOutcome{skipped: true}, nil
	}

	hasHistory, err := d.Store.ThreadHasHistory(ctx, th.ThreadID)
	if err != nil {
		return threadOutcome{}, err
	}

	var drifted *reliability.Verdict
	cur := prev

	// Phase 1 — head. Invalidated crawls the whole history (advanceFull); Grew
	// fetches only the new tail down to the known anchor (advanceHead), leaving
	// the backfill frontier untouched. Unchanged skips phase 1.
	switch res {
	case dm.Invalidated:
		c, out, err := pagePhase(ctx, d, th, "", "", hasHistory, dm.ThreadCursor{}, advanceFull, &drifted)
		if err != nil || out.terminal != nil || out.drift != nil {
			return out, err
		}
		cur = c
	case dm.Grew:
		c, out, err := pagePhase(ctx, d, th, "", prev.NewestItemID, hasHistory, cur, advanceHead, &drifted)
		if err != nil || out.terminal != nil || out.drift != nil {
			return out, err
		}
		cur = c
	}

	// Phase 2 — tail backfill. Resume from the stored frontier toward the start,
	// for a Grew or Unchanged thread not yet fully backfilled. Invalidated already
	// went to the bottom in phase 1 (or hit the page cap; the next run resumes it
	// as Unchanged/Grew), so it never double-spends the budget here.
	if res != dm.Invalidated && !cur.Backfilled {
		c, out, err := pagePhase(ctx, d, th, cur.OldestCursor, "", hasHistory, cur, advanceTail, &drifted)
		if err != nil || out.terminal != nil || out.drift != nil {
			return out, err
		}
		cur = c
	}

	return threadOutcome{drift: drifted}, nil
}

// pagePhase crawls one range of a thread backward from startCursor, committing
// each page. It stops at stopAtItemID (a head catch-up, when that already-stored
// id appears in the page) or at the natural bottom / page cap (a backfill).
// mode selects which cursor frontier the commits advance. The returned outcome
// is non-empty ONLY for a run-halting terminal or a structural (unparseable)
// drift; an unknown-item-type drift is recorded via drifted and does not stop
// the crawl (the page is still committed, its unknown items flagged + raw-kept).
func pagePhase(ctx context.Context, d Deps, th dm.Thread, startCursor, stopAtItemID string,
	hasHistory bool, cur dm.ThreadCursor, mode advanceMode, drifted **reliability.Verdict,
) (dm.ThreadCursor, threadOutcome, error) {
	guard := reliability.NewPageGuard(d.MaxThreadPages)
	cursor := startCursor
	firstPage := true

	for {
		resp, err := d.Fetcher.Fetch(ctx, d.Bundle, ig.ThreadRequest(th.ThreadID, cursor))
		if err != nil {
			return cur, threadOutcome{}, fmt.Errorf("%s thread %s transport failure: %s", d.Fetcher.Name(), th.ThreadID, session.Sanitize(err.Error()))
		}
		floor := reliability.Floor{
			HasKnownHistory:       hasHistory,
			ConsecutiveEmpty:      cur.ConsecutiveEmpty,
			ConsecutiveEmptyLimit: emptyPageLimit,
		}
		tr := dm.ClassifyThread(resp, floor)

		switch tr.Verdict.State {
		case reliability.StateOK:
			// commit below
		case reliability.StateSchemaDrift:
			if tr.Page == nil {
				// Structural drift — nothing safe to commit. Isolate this thread.
				v := tr.Verdict
				return cur, threadOutcome{drift: &v}, nil
			}
			// Unknown item_type: the page parsed leniently (unknown items stored
			// item_type_known=0 + raw_json), so committing loses nothing and a later
			// parser re-derives it from raw (DECISIONS §4B). Flag, keep paging.
			v := tr.Verdict
			*drifted = &v
		default: // AUTH·CHECKPOINT / RATE·LIMITED → halt whole run
			return cur, threadOutcome{terminal: &terminal{verdict: tr.Verdict, challengeURL: tr.ChallengeURL}}, nil
		}

		cur = advanceCursor(cur, tr.Page, firstPage, mode)
		if err := d.Store.CommitThreadPage(ctx, tr.Page, cur); err != nil {
			return cur, threadOutcome{}, err
		}
		firstPage = false

		if reachedStop(tr.Page, stopAtItemID) {
			return cur, threadOutcome{}, nil // caught up to the known head
		}
		// proceed=false means fully backfilled (empty cursor), the page cap, or a
		// non-advancing cursor — all bounded stops for this thread, not failures.
		proceed, guardErr := guard.Next(tr.Page.OldestCursor)
		if !proceed {
			_ = guardErr
			return cur, threadOutcome{}, nil
		}
		cursor = tr.Page.OldestCursor
		if err := d.Pacer.Wait(ctx); err != nil {
			return cur, threadOutcome{}, err
		}
	}
}

// advanceCursor computes the next persisted cursor for a committed page. mode
// controls which frontier moves: the newest anchor is set from the first page of
// a head/full pass; the oldest cursor + empty-run counter advance every page of a
// tail/full pass. A head catch-up leaves the backfill frontier untouched (and
// vice-versa) so the two frontiers never clobber each other across runs.
func advanceCursor(prev dm.ThreadCursor, page *dm.ThreadPage, firstPage bool, mode advanceMode) dm.ThreadCursor {
	cur := prev
	cur.SchemaVersion = dm.CursorSchemaVersion

	if mode == advanceFull || mode == advanceTail {
		cur.OldestCursor = page.OldestCursor
		cur.Backfilled = page.OldestCursor == ""
		if len(page.Messages) == 0 {
			cur.ConsecutiveEmpty = prev.ConsecutiveEmpty + 1
		} else {
			cur.ConsecutiveEmpty = 0
		}
	}
	if (mode == advanceFull || mode == advanceHead) && firstPage {
		newestID, newestTS := cur.NewestItemID, cur.NewestTimestampUS
		for _, m := range page.Messages {
			if m.TimestampUS > newestTS {
				newestTS = m.TimestampUS
				newestID = m.ItemID
			}
		}
		cur.NewestItemID = newestID
		cur.NewestTimestampUS = newestTS
	}
	return cur
}

// formatUnknownTypes renders an item_type→count map as "raven_media×12, profile×3"
// (schema, not content — safe to log), ordered by count then name.
func formatUnknownTypes(m map[string]int) string {
	type kv struct {
		t string
		n int
	}
	pairs := make([]kv, 0, len(m))
	for t, n := range m {
		pairs = append(pairs, kv{t, n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].t < pairs[j].t
	})
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("%s×%d", p.t, p.n))
	}
	return strings.Join(parts, ", ")
}

func reachedStop(page *dm.ThreadPage, stopAt string) bool {
	if stopAt == "" {
		return false
	}
	for _, id := range page.ItemIDs {
		if id == stopAt {
			return true
		}
	}
	return false
}

func terminalSummary(sum Summary, t *terminal) Summary {
	sum.State = t.verdict.State.String()
	sum.Reason = t.verdict.Reason
	sum.ChallengeURL = t.challengeURL
	return sum
}
