package dm

import (
	"fmt"

	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/reliability"
)

// InboxResult is the classified outcome of an inbox page.
type InboxResult struct {
	Verdict      reliability.Verdict
	Page         *InboxPage // non-nil only on StateOK
	ChallengeURL string
	RawBody      string // retained on SCHEMA·DRIFT
}

// ThreadResult is the classified outcome of a thread page.
type ThreadResult struct {
	Verdict      reliability.Verdict
	Page         *ThreadPage // non-nil on OK; also returned on unknown-type drift so callers can retain/count
	ChallengeURL string
	RawBody      string
}

// ClassifyInbox runs the shared envelope gate, then parses the inbox payload.
// floor escalates an empty page for a resource with known history to drift.
func ClassifyInbox(resp ig.RawResponse, floor reliability.Floor) InboxResult {
	v, challengeURL, terminal := ig.ClassifyEnvelope(resp)
	if terminal {
		out := InboxResult{Verdict: v, ChallengeURL: challengeURL}
		if v.State == reliability.StateSchemaDrift {
			out.RawBody = string(resp.Body)
		}
		return out
	}
	page, err := ParseInbox(resp.Body)
	if err != nil {
		return InboxResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift, "inbox: "+err.Error()),
			RawBody: string(resp.Body),
		}
	}
	return InboxResult{Verdict: floor.Apply(reliability.OK(len(page.Threads))), Page: page}
}

// ClassifyThread runs the shared envelope gate, then parses the thread payload.
// An unknown item_type is drift (counted, never skipped); the parsed page is
// still returned so the caller can retain the raw and report the counts.
func ClassifyThread(resp ig.RawResponse, floor reliability.Floor) ThreadResult {
	v, challengeURL, terminal := ig.ClassifyEnvelope(resp)
	if terminal {
		out := ThreadResult{Verdict: v, ChallengeURL: challengeURL}
		if v.State == reliability.StateSchemaDrift {
			out.RawBody = string(resp.Body)
		}
		return out
	}
	page, err := ParseThread(resp.Body)
	if err != nil {
		return ThreadResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift, "thread: "+err.Error()),
			RawBody: string(resp.Body),
		}
	}
	if len(page.UnknownTypes) > 0 {
		return ThreadResult{
			Verdict: reliability.Fail(reliability.StateSchemaDrift,
				fmt.Sprintf("thread %s: %d unknown item_type(s) %v — retaining raw, not skipping",
					page.ThreadID, len(page.UnknownTypes), page.UnknownTypes)),
			Page:    page,
			RawBody: string(resp.Body),
		}
	}
	return ThreadResult{Verdict: floor.Apply(reliability.OK(len(page.Messages))), Page: page}
}
