package ig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ss251/instacrawl/internal/reliability"
	"github.com/ss251/instacrawl/internal/session"
)

const expectedID = "17841400000000000"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestClassifyTable(t *testing.T) {
	cases := []struct {
		name   string
		resp   RawResponse
		want   reliability.State
		assert func(t *testing.T, pr ProbeResult)
	}{
		{
			name: "ok_self_profile",
			resp: RawResponse{Status: 200, ContentType: "application/json", Body: fixture(t, "ok_self_profile.json")},
			want: reliability.StateOK,
			assert: func(t *testing.T, pr ProbeResult) {
				if pr.Verdict.N != 1 {
					t.Fatalf("OK must be N=1, got %d", pr.Verdict.N)
				}
				if pr.Account == nil || pr.Account.PK != expectedID || pr.Account.Username != "testuser" {
					t.Fatalf("account not extracted correctly: %+v", pr.Account)
				}
			},
		},
		{
			name: "checkpoint_required",
			resp: RawResponse{Status: 200, ContentType: "application/json", Body: fixture(t, "checkpoint_required.json")},
			want: reliability.StateAuthCheckpoint,
			assert: func(t *testing.T, pr ProbeResult) {
				if !strings.Contains(pr.ChallengeURL, "/challenge/") {
					t.Fatalf("challenge URL not carried: %q", pr.ChallengeURL)
				}
				if pr.Account != nil {
					t.Fatal("no account may be extracted on checkpoint")
				}
			},
		},
		{
			name: "login_required_400",
			resp: RawResponse{Status: 400, ContentType: "application/json", Body: fixture(t, "login_required.json")},
			want: reliability.StateAuthCheckpoint,
		},
		{
			name: "logged_out_html_200",
			resp: RawResponse{Status: 200, ContentType: "text/html; charset=utf-8", Body: fixture(t, "logged_out.html")},
			want: reliability.StateAuthCheckpoint,
		},
		{
			name: "redirect_to_login",
			resp: RawResponse{Status: 302, Location: "https://www.instagram.com/accounts/login/?next=/"},
			want: reliability.StateAuthCheckpoint,
		},
		{
			name: "bare_403",
			resp: RawResponse{Status: 403},
			want: reliability.StateAuthCheckpoint,
		},
		{
			name: "identity_mismatch",
			resp: RawResponse{Status: 200, ContentType: "application/json", Body: fixture(t, "identity_mismatch.json")},
			want: reliability.StateAuthCheckpoint,
			assert: func(t *testing.T, pr ProbeResult) {
				if pr.Account != nil {
					t.Fatal("wrong-account 200 must not yield an account")
				}
				if !strings.Contains(pr.Verdict.Reason, "99999999999") {
					t.Fatalf("mismatch reason should name the wrong id: %q", pr.Verdict.Reason)
				}
			},
		},
		{
			name: "rate_feedback_required",
			resp: RawResponse{Status: 400, ContentType: "application/json", Body: fixture(t, "rate_feedback_required.json")},
			want: reliability.StateRateLimited,
		},
		{
			name: "rate_429",
			resp: RawResponse{Status: 429, RetryAfter: "120"},
			want: reliability.StateRateLimited,
			assert: func(t *testing.T, pr ProbeResult) {
				if !strings.Contains(pr.Verdict.Reason, "120") {
					t.Fatalf("Retry-After should surface in reason: %q", pr.Verdict.Reason)
				}
			},
		},
		{
			name: "server_error_503",
			resp: RawResponse{Status: 503},
			want: reliability.StateRateLimited,
		},
		{
			name: "drift_missing_keys",
			resp: RawResponse{Status: 200, ContentType: "application/json", Body: fixture(t, "schema_drift.json")},
			want: reliability.StateSchemaDrift,
			assert: func(t *testing.T, pr ProbeResult) {
				if pr.RawBody == "" {
					t.Fatal("drift must retain the raw body for re-parse")
				}
			},
		},
		{
			name: "drift_bad_json",
			resp: RawResponse{Status: 200, ContentType: "application/json", Body: []byte("{not valid json")},
			want: reliability.StateSchemaDrift,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pr := Classify(c.resp, expectedID)
			if pr.Verdict.State != c.want {
				t.Fatalf("state = %s, want %s (reason=%q)", pr.Verdict.State, c.want, pr.Verdict.Reason)
			}
			if c.assert != nil {
				c.assert(t, pr)
			}
		})
	}
}

func TestClassifyCapturesWWWClaim(t *testing.T) {
	pr := Classify(RawResponse{Status: 200, ContentType: "application/json", WWWClaim: "hmac.xyz", Body: fixture(t, "ok_self_profile.json")}, expectedID)
	if pr.WWWClaim != "hmac.xyz" {
		t.Fatalf("WWWClaim not captured: %q", pr.WWWClaim)
	}
}

// --- Probe seam over a fake Fetcher (no network) ---

type fakeFetcher struct {
	resp RawResponse
	err  error
}

func (f fakeFetcher) FetchSelfProfile(context.Context, *session.WebBundle) (RawResponse, error) {
	return f.resp, f.err
}
func (f fakeFetcher) Fetch(context.Context, *session.WebBundle, Request) (RawResponse, error) {
	return f.resp, f.err
}
func (f fakeFetcher) Name() string { return "fake" }

func TestProbeThroughSeam(t *testing.T) {
	b := &session.WebBundle{SessionID: expectedID + "%3Afake", DSUserID: expectedID}
	b.Normalize()

	pr, err := Probe(context.Background(), fakeFetcher{resp: RawResponse{Status: 200, ContentType: "application/json", Body: fixture(t, "ok_self_profile.json")}}, b)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Verdict.State != reliability.StateOK || pr.Transport != "fake" {
		t.Fatalf("expected OK via fake transport, got %s/%s", pr.Verdict.State, pr.Transport)
	}
}

func TestProbeTransportFailureIsNotAVerdict(t *testing.T) {
	b := &session.WebBundle{SessionID: expectedID + "%3Afake", DSUserID: expectedID}
	b.Normalize()
	_, err := Probe(context.Background(), fakeFetcher{err: errors.New("dial tcp: connection refused")}, b)
	if err == nil {
		t.Fatal("transport failure must return an error, not a fabricated verdict")
	}
}

// TestFixturesHaveNoSecrets guards against ever committing a real session
// cookie into testdata — response fixtures are profiles and must never contain
// a sessionid.
func TestFixturesHaveNoSecrets(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join("testdata", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		low := strings.ToLower(string(data))
		for _, forbidden := range []string{"sessionid", "csrftoken", "\"authorization\""} {
			if strings.Contains(low, forbidden) {
				t.Fatalf("fixture %s appears to contain a secret field %q — scrub it", e.Name(), forbidden)
			}
		}
	}
}
