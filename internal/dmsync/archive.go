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
	InboxThreads   int
	ThreadsFetched int
	ThreadsSkipped int
	MessagesNew    int
	State          string // "OK" | terminal state name
	Reason         string
	ChallengeURL   string
	DriftedThreads []string
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
				return terminalSummary(sum, pterm), nil
			}
			threads = append(threads, pending...)
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
		sum.Reason = fmt.Sprintf("%d thread(s) hit schema drift and were skipped; raw retained", len(sum.DriftedThreads))
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

// archiveThread crawls one thread backwards with three-way resume.
func archiveThread(ctx context.Context, d Deps, th dm.Thread) (threadOutcome, error) {
	prev, ok, err := d.Store.GetThreadCursor(ctx, th.ThreadID)
	if err != nil {
		return threadOutcome{}, err
	}
	res := dm.ResolveResume(prev, ok, th.LastActivityUS)
	if d.FullRecrawl {
		res = dm.Invalidated
	}
	if res == dm.Unchanged {
		return threadOutcome{skipped: true}, nil // the idempotency win: zero fetches
	}

	hasHistory, err := d.Store.ThreadHasHistory(ctx, th.ThreadID)
	if err != nil {
		return threadOutcome{}, err
	}

	guard := reliability.NewPageGuard(d.MaxThreadPages)
	cursor := ""
	stopAt := ""
	if res == dm.Grew {
		stopAt = prev.NewestItemID
	}
	newest := prev
	firstPage := true

	for {
		resp, err := d.Fetcher.Fetch(ctx, d.Bundle, ig.ThreadRequest(th.ThreadID, cursor))
		if err != nil {
			return threadOutcome{}, fmt.Errorf("%s thread %s transport failure: %s", d.Fetcher.Name(), th.ThreadID, session.Sanitize(err.Error()))
		}
		floor := reliability.Floor{
			HasKnownHistory:       hasHistory,
			ConsecutiveEmpty:      newest.ConsecutiveEmpty,
			ConsecutiveEmptyLimit: emptyPageLimit,
		}
		tr := dm.ClassifyThread(resp, floor)

		switch tr.Verdict.State {
		case reliability.StateOK:
			newCur := advanceCursor(newest, tr.Page, firstPage)
			if err := d.Store.CommitThreadPage(ctx, tr.Page, newCur); err != nil {
				return threadOutcome{}, err
			}
			newest = newCur
			firstPage = false

			if res == dm.Grew && reachedStop(tr.Page, stopAt) {
				return threadOutcome{}, nil // caught up to the known head
			}
			// proceed=false means fully backfilled (empty cursor), the page
			// cap, or a non-advancing cursor — all bounded stops for this
			// thread, not run failures.
			proceed, guardErr := guard.Next(tr.Page.OldestCursor)
			if !proceed {
				_ = guardErr
				return threadOutcome{}, nil
			}
			cursor = tr.Page.OldestCursor
			if err := d.Pacer.Wait(ctx); err != nil {
				return threadOutcome{}, err
			}
		case reliability.StateSchemaDrift:
			v := tr.Verdict
			return threadOutcome{drift: &v}, nil // isolate this thread, continue others
		default: // AUTH·CHECKPOINT / RATE·LIMITED → halt whole run
			return threadOutcome{terminal: &terminal{verdict: tr.Verdict, challengeURL: tr.ChallengeURL}}, nil
		}
	}
}

// advanceCursor computes the next persisted cursor. The newest anchor is set
// only from the first (newest) page; the oldest cursor and empty-run counter
// update every page.
func advanceCursor(prev dm.ThreadCursor, page *dm.ThreadPage, firstPage bool) dm.ThreadCursor {
	cur := prev
	cur.SchemaVersion = dm.CursorSchemaVersion
	cur.OldestCursor = page.OldestCursor
	cur.Backfilled = page.OldestCursor == ""
	if len(page.Messages) == 0 {
		cur.ConsecutiveEmpty = prev.ConsecutiveEmpty + 1
	} else {
		cur.ConsecutiveEmpty = 0
	}
	if firstPage {
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
