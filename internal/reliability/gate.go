// Package reliability codifies instacrawl's "reliability spine": the rule that a
// fetch is not trusted because a request returned HTTP 200, but because a gate
// independent of the request layer classified the response into one of four
// states. The spine is the load-bearing contract of the whole tool — every
// source lane (web, mobile, carve) funnels its responses through Classify before
// a single row is committed.
//
// The design is grounded in loop-engineering principles (claude-mission-control
// research docs). The two that shape the type surface most directly:
//
//   - Verifier expressiveness must match the failure space (Ashby's Law): a
//     single scalar success/fail signal cannot regulate a multi-mode failure
//     space, so the gate is a four-valued enum, never a bool.
//   - The gate needs a fail-open / fail-closed split: an error *inside the gate*
//     (a bug in our own validator) must not masquerade as a genuine contract
//     violation from the remote. Those route to different places.
package reliability

import "fmt"

// State is the verdict the gate assigns to a single fetched response. It is a
// closed set: adding an Instagram failure mode means adding a State here and
// handling it at every switch, which the exhaustiveness test enforces.
type State int

const (
	// StateInvalid is the zero value and never a valid classification result.
	// Its presence in a committed row is itself a bug.
	StateInvalid State = iota

	// StateOK means the schema matched and the response cleared the semantic
	// floor (see Floor). Only StateOK responses may be committed.
	StateOK

	// StateAuthCheckpoint means Instagram returned a login/challenge/checkpoint.
	// This is a HARD STOP: the run halts and hands the challenge URL to the
	// user. instacrawl never auto-solves a 2FA or checkpoint challenge.
	StateAuthCheckpoint

	// StateRateLimited means a 429 or a feedback_required soft block. The run
	// backs off, checkpoints its cursor, and stops. Grinding a soft block into
	// a hard block is the failure mode this state exists to prevent.
	StateRateLimited

	// StateSchemaDrift means the response was reachable and authorized but its
	// shape no longer matches what the parser expects (including an unknown DM
	// item_type, or a cursor whose stored schema_version is stale). The run
	// stops and the raw JSON is retained so a later parser fix can re-derive the
	// rows without re-hitting the API. An unknown item_type is drift, never a
	// silent skip.
	StateSchemaDrift
)

func (s State) String() string {
	switch s {
	case StateOK:
		return "OK"
	case StateAuthCheckpoint:
		return "AUTH·CHECKPOINT"
	case StateRateLimited:
		return "RATE·LIMITED"
	case StateSchemaDrift:
		return "SCHEMA·DRIFT"
	case StateInvalid:
		return "INVALID"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// Terminal reports whether a state stops the current run. Only StateOK is
// non-terminal; every failure mode halts and is surfaced loudly (success is
// silent, failure is verbose).
func (s State) Terminal() bool { return s != StateOK }

// Verdict is the full result of a classification: the state, the number of
// records the response yielded (meaningful only for StateOK), and a
// human-facing reason that is empty on StateOK and specific otherwise.
type Verdict struct {
	State State
	// N is the count of parseable records in this response. Used by the
	// semantic floor and the dead-man's-switch (a resource with known history
	// that keeps returning N==0 is drift, not success).
	N int
	// Reason is a specific, operator-readable explanation for any non-OK state:
	// the resource, the cursor, an excerpt of the offending payload. Empty for
	// StateOK, because success is silent.
	Reason string
}

// OK constructs a passing verdict for a response that yielded n records.
func OK(n int) Verdict { return Verdict{State: StateOK, N: n} }

// Fail constructs a terminal verdict. It panics if handed StateOK, because a
// "failing OK" is a category error the compiler cannot catch.
func Fail(s State, reason string) Verdict {
	if s == StateOK {
		panic("reliability: Fail called with StateOK")
	}
	return Verdict{State: s, Reason: reason}
}

// GateError is returned by Classify when the gate ITSELF fails — a bug in our
// validator, an I/O error reading the raw body, a nil dependency. This is the
// fail-open half of the split: the caller logs it and may continue, and it is
// explicitly NOT a SchemaDrift, so a checker bug never gets mistaken for the
// remote changing its API. A genuine contract violation from the remote comes
// back as a StateSchemaDrift Verdict with a nil error instead.
type GateError struct {
	Op  string
	Err error
}

func (e *GateError) Error() string {
	return fmt.Sprintf("reliability gate internal failure during %s: %v", e.Op, e.Err)
}

func (e *GateError) Unwrap() error { return e.Err }

// Floor is the semantic postcondition applied to an otherwise-shape-valid
// response. Shape validity alone is not success: a resource that is KNOWN to
// have history but returns zero new records across ConsecutiveEmptyLimit
// scheduled runs is escalated to SchemaDrift, because "every page was HTTP 200
// and well-formed, yet nothing came back" is exactly the silent-empty failure
// the spine exists to catch. A resource with no known history legitimately
// returns zero and must not trip the floor.
type Floor struct {
	// HasKnownHistory is true when we have prior evidence this resource is
	// non-empty (e.g. a prior run committed rows for it, or the profile
	// advertises a non-zero count). Only then does an empty response count
	// against the floor.
	HasKnownHistory bool
	// ConsecutiveEmpty is how many prior consecutive scheduled runs already
	// returned zero for this resource.
	ConsecutiveEmpty int
	// ConsecutiveEmptyLimit is the number of empty-in-a-row runs tolerated
	// before an empty StateOK is re-classified as drift. Must be >= 1.
	ConsecutiveEmptyLimit int
}

// Apply re-classifies a shape-valid verdict against the semantic floor. It only
// ever downgrades StateOK; it never rescues a terminal state. Non-OK verdicts
// pass through unchanged.
func (f Floor) Apply(v Verdict) Verdict {
	if v.State != StateOK {
		return v
	}
	if v.N > 0 {
		return v
	}
	if !f.HasKnownHistory {
		return v // legitimately empty resource
	}
	limit := f.ConsecutiveEmptyLimit
	if limit < 1 {
		limit = 1
	}
	// This run is empty too, so it is the (ConsecutiveEmpty+1)-th in a row.
	if f.ConsecutiveEmpty+1 >= limit {
		return Fail(StateSchemaDrift, fmt.Sprintf(
			"resource with known history returned 0 records for %d consecutive runs — treating as drift, not success",
			f.ConsecutiveEmpty+1))
	}
	return v
}
