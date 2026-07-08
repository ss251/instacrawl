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

// profileBody is the union of the fields the classifier reads across the OK,
// checkpoint, and error response shapes. Absent fields stay zero.
type profileBody struct {
	Status        string `json:"status"`
	Message       string `json:"message"`
	CheckpointURL string `json:"checkpoint_url"`
	ChallengeURL  string `json:"challenge_url"`
	Challenge     *struct {
		URL string `json:"url"`
	} `json:"challenge"`
	User *struct {
		PK            flexID `json:"pk"`
		PKID          flexID `json:"pk_id"`
		Username      string `json:"username"`
		FullName      string `json:"full_name"`
		IsPrivate     bool   `json:"is_private"`
		ProfilePicURL string `json:"profile_pic_url"`
	} `json:"user"`
}

// Classify is the pure four-state gate over a single response. It is
// deterministic and does no I/O, so it is exhaustively fixture-testable. The
// ordering is auth → rate → shape, because a checkpoint can arrive dressed as a
// 200, a 302, or a 400, so it must be checked before the happy-path parse.
//
// The identity assertion lives INSIDE the gate: OK(1) is returned only when the
// server-returned pk equals expectedUserID. A correctly-shaped 200 for the
// WRONG account is a terminal AUTH·CHECKPOINT, never a silent success — this is
// the whole point of "a 200 is not success."
func Classify(resp RawResponse, expectedUserID string) ProbeResult {
	out := ProbeResult{WWWClaim: resp.WWWClaim}

	// 1. Redirect to a login/challenge page (3xx + Location).
	if isRedirect(resp.Status) {
		loc := strings.ToLower(resp.Location)
		if strings.Contains(loc, "/accounts/login") || strings.Contains(loc, "/challenge") {
			out.ChallengeURL = resp.Location
			out.Verdict = reliability.Fail(reliability.StateAuthCheckpoint,
				"redirected to login/challenge: "+resp.Location)
			return out
		}
	}

	// 2. An HTML login wall (logged-out web frequently returns 200 + HTML).
	//    Check this BEFORE attempting a JSON parse, or it misclassifies as drift.
	if looksLikeHTML(resp) {
		out.Verdict = reliability.Fail(reliability.StateAuthCheckpoint,
			"received an HTML page (login wall) instead of a JSON profile — session is logged out")
		return out
	}

	// Attempt to parse the body as the profile/error union. A parse failure is
	// not fatal by itself; later branches still classify by status.
	var body profileBody
	parseErr := json.Unmarshal(resp.Body, &body)

	// 3. Explicit auth/challenge signals in a JSON body.
	if parseErr == nil {
		msg := strings.ToLower(strings.TrimSpace(body.Message))
		if url := challengeURL(body); url != "" || isAuthMessage(msg) {
			out.ChallengeURL = url
			out.Verdict = reliability.Fail(reliability.StateAuthCheckpoint,
				fmt.Sprintf("authentication required (message=%q)", body.Message))
			return out
		}
	}

	// 4. Unauthorized status codes.
	if resp.Status == 401 || resp.Status == 403 {
		out.Verdict = reliability.Fail(reliability.StateAuthCheckpoint,
			fmt.Sprintf("unauthorized (HTTP %d) — session invalid or checkpointed", resp.Status))
		return out
	}

	// 5. Rate limiting: 429, or a feedback_required / "please wait" body.
	if resp.Status == 429 || (parseErr == nil && isRateMessage(strings.ToLower(body.Message))) {
		reason := fmt.Sprintf("rate limited (HTTP %d, message=%q)", resp.Status, body.Message)
		if resp.RetryAfter != "" {
			reason += "; Retry-After=" + resp.RetryAfter
		}
		out.Verdict = reliability.Fail(reliability.StateRateLimited, reason)
		return out
	}

	// 6. Server errors: back off and stop.
	if resp.Status >= 500 && resp.Status <= 599 {
		out.Verdict = reliability.Fail(reliability.StateRateLimited,
			fmt.Sprintf("server error (HTTP %d) — backing off", resp.Status))
		return out
	}

	// 7. Happy path: 200 with a parseable profile.
	if resp.Status == 200 {
		if parseErr != nil {
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
			RawJSON:       string(resp.Body),
		}
		out.Verdict = reliability.OK(1)
		return out
	}

	// 8. Anything else is an unexpected shape — fail loud, retain raw.
	out.RawBody = string(resp.Body)
	out.Verdict = reliability.Fail(reliability.StateSchemaDrift,
		fmt.Sprintf("unexpected response (HTTP %d) — dumping raw for triage", resp.Status))
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

func challengeURL(body profileBody) string {
	if body.CheckpointURL != "" {
		return body.CheckpointURL
	}
	if body.ChallengeURL != "" {
		return body.ChallengeURL
	}
	if body.Challenge != nil && body.Challenge.URL != "" {
		return body.Challenge.URL
	}
	return ""
}
