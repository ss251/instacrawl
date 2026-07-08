package ig

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ss251/instacrawl/internal/reliability"
)

// flexID accepts a JSON pk that is either a number (17841400000000000) or a
// string ("17841400000000000") and yields the canonical string form. Instagram
// is inconsistent about this across endpoints, and a big-int pk must never go
// through float64, so we keep the literal.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	s = strings.Trim(s, `"`)
	*f = flexID(s)
	return nil
}

// envelope is the endpoint-agnostic subset of any IG JSON response: the
// top-level status/message and the several shapes a challenge URL can take.
// Every endpoint (profile, inbox, thread, …) shares these, which is why the
// auth/rate/checkpoint gate can be shared across all of them.
type envelope struct {
	Status        string `json:"status"`
	Message       string `json:"message"`
	CheckpointURL string `json:"checkpoint_url"`
	ChallengeURL  string `json:"challenge_url"`
	Challenge     *struct {
		URL string `json:"url"`
	} `json:"challenge"`
}

func (e envelope) challenge() string {
	if e.CheckpointURL != "" {
		return e.CheckpointURL
	}
	if e.ChallengeURL != "" {
		return e.ChallengeURL
	}
	if e.Challenge != nil && e.Challenge.URL != "" {
		return e.Challenge.URL
	}
	return ""
}

// profileBody embeds the shared envelope and adds the self-profile payload.
type profileBody struct {
	envelope
	User *struct {
		PK            flexID `json:"pk"`
		PKID          flexID `json:"pk_id"`
		Username      string `json:"username"`
		FullName      string `json:"full_name"`
		IsPrivate     bool   `json:"is_private"`
		ProfilePicURL string `json:"profile_pic_url"`
		MediaCount    int    `json:"media_count"`
	} `json:"user"`
}

// ClassifyEnvelope applies the endpoint-agnostic half of the four-state gate:
// the auth / checkpoint / rate-limit / server-error detection that is identical
// for every IG endpoint. The ordering is auth → rate → shape, because a
// checkpoint can arrive dressed as a 200, a 302, or a 400.
//
//   - terminal == true: the returned Verdict is final; the caller returns it
//     immediately (it is one of AUTH·CHECKPOINT / RATE·LIMITED / SCHEMA·DRIFT).
//     challengeURL is set on AUTH·CHECKPOINT when discoverable.
//   - terminal == false: a clean HTTP 200 whose envelope carries no auth/rate
//     signal. The caller must parse it with its OWN payload parser and decide
//     OK vs SCHEMA·DRIFT from the payload shape.
//
// This is pure and deterministic — no I/O — so it is exhaustively fixture-tested.
func ClassifyEnvelope(resp RawResponse) (verdict reliability.Verdict, challengeURL string, terminal bool) {
	// 1. Redirect to a login/challenge page (3xx + Location).
	if isRedirect(resp.Status) {
		loc := strings.ToLower(resp.Location)
		if strings.Contains(loc, "/accounts/login") || strings.Contains(loc, "/challenge") {
			return reliability.Fail(reliability.StateAuthCheckpoint,
				"redirected to login/challenge: "+resp.Location), resp.Location, true
		}
	}

	// 2. An HTML login wall (logged-out web frequently returns 200 + HTML).
	//    Check this BEFORE attempting a JSON parse, or it misclassifies as drift.
	if looksLikeHTML(resp) {
		return reliability.Fail(reliability.StateAuthCheckpoint,
			"received an HTML page (login wall) instead of a JSON response — session is logged out"), "", true
	}

	var env envelope
	parseErr := json.Unmarshal(resp.Body, &env)

	// 3. Explicit auth/challenge signals in a JSON body.
	if parseErr == nil {
		msg := strings.ToLower(strings.TrimSpace(env.Message))
		if url := env.challenge(); url != "" || isAuthMessage(msg) {
			return reliability.Fail(reliability.StateAuthCheckpoint,
				fmt.Sprintf("authentication required (message=%q)", env.Message)), url, true
		}
	}

	// 4. Unauthorized status codes.
	if resp.Status == 401 || resp.Status == 403 {
		return reliability.Fail(reliability.StateAuthCheckpoint,
			fmt.Sprintf("unauthorized (HTTP %d) — session invalid or checkpointed", resp.Status)), "", true
	}

	// 5. Rate limiting: 429, or a feedback_required / "please wait" body.
	if resp.Status == 429 || (parseErr == nil && isRateMessage(strings.ToLower(env.Message))) {
		reason := fmt.Sprintf("rate limited (HTTP %d, message=%q)", resp.Status, env.Message)
		if resp.RetryAfter != "" {
			reason += "; Retry-After=" + resp.RetryAfter
		}
		return reliability.Fail(reliability.StateRateLimited, reason), "", true
	}

	// 6. Server errors: back off and stop.
	if resp.Status >= 500 && resp.Status <= 599 {
		return reliability.Fail(reliability.StateRateLimited,
			fmt.Sprintf("server error (HTTP %d) — backing off", resp.Status)), "", true
	}

	// 7. Any non-200 that reached here is an unexpected shape — fail loud.
	if resp.Status != 200 {
		return reliability.Fail(reliability.StateSchemaDrift,
			fmt.Sprintf("unexpected response (HTTP %d) — dumping raw for triage", resp.Status)), "", true
	}

	// Clean 200 — the caller parses the payload.
	return reliability.Verdict{}, "", false
}

// Classify is the profile-specific gate: it runs the shared envelope gate, then
// (on a clean 200) parses the self-profile payload and asserts the returned pk
// equals expectedUserID. OK(1) is returned only when the shape AND the account
// identity both check out — a correctly-shaped 200 for the WRONG account is a
// terminal AUTH·CHECKPOINT, never a silent success.
func Classify(resp RawResponse, expectedUserID string) ProbeResult {
	out := ProbeResult{WWWClaim: resp.WWWClaim}

	v, challengeURL, terminal := ClassifyEnvelope(resp)
	if terminal {
		out.Verdict = v
		out.ChallengeURL = challengeURL
		if v.State == reliability.StateSchemaDrift {
			out.RawBody = string(resp.Body)
		}
		return out
	}

	// Clean 200: parse the profile payload.
	var body profileBody
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		out.RawBody = string(resp.Body)
		out.Verdict = reliability.Fail(reliability.StateSchemaDrift,
			"200 OK but body is not valid JSON — dumping raw for re-parse")
		return out
	}
	if body.User == nil || string(body.User.PK) == "" || body.User.Username == "" {
		out.RawBody = string(resp.Body)
		out.Verdict = reliability.Fail(reliability.StateSchemaDrift,
			"200 OK but required keys (user.pk, user.username) are missing/renamed — dumping raw for re-parse")
		return out
	}
	pk := strings.TrimSpace(string(body.User.PK))
	if expectedUserID != "" && pk != strings.TrimSpace(expectedUserID) {
		out.Verdict = reliability.Fail(reliability.StateAuthCheckpoint,
			fmt.Sprintf("authenticated as %s but expected %s — refusing to archive the wrong account", pk, expectedUserID))
		return out
	}
	out.Account = &Account{
		PK:            pk,
		Username:      body.User.Username,
		FullName:      body.User.FullName,
		IsPrivate:     body.User.IsPrivate,
		ProfilePicURL: body.User.ProfilePicURL,
		MediaCount:    body.User.MediaCount,
		RawJSON:       string(resp.Body),
	}
	out.Verdict = reliability.OK(1)
	return out
}

func isRedirect(status int) bool { return status >= 300 && status <= 399 }

func looksLikeHTML(resp RawResponse) bool {
	if strings.Contains(strings.ToLower(resp.ContentType), "text/html") {
		return true
	}
	head := strings.ToLower(strings.TrimSpace(string(resp.Body)))
	if len(head) > 64 {
		head = head[:64]
	}
	return strings.HasPrefix(head, "<!doctype") || strings.HasPrefix(head, "<html")
}

func isAuthMessage(msg string) bool {
	switch msg {
	case "checkpoint_required", "challenge_required", "login_required":
		return true
	}
	return false
}

func isRateMessage(msg string) bool {
	if msg == "feedback_required" {
		return true
	}
	return strings.Contains(msg, "please wait") || strings.Contains(msg, "wait a few minutes")
}
