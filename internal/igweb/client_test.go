package igweb

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/session"
)

// compile-time: the Go-native client satisfies the seam.
var _ ig.Fetcher = (*Client)(nil)

const (
	dummyUserID    = "17841400000000000"
	dummySessionID = dummyUserID + "%3ATESTSESSIONfakeonly"
)

// stubTransport captures the outbound request and returns a canned response.
type stubTransport struct {
	captured *http.Request
	resp     *http.Response
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.captured = req
	return s.resp, nil
}

func newResp(status int, body string) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-IG-Set-WWW-Claim", "hmac.testclaim")
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestFetchSelfProfileSendsRequiredHeadersAndCookies(t *testing.T) {
	stub := &stubTransport{resp: newResp(200, `{"status":"ok","user":{"pk":`+dummyUserID+`,"username":"testuser"}}`)}
	c, err := New(WithTransport(stub))
	if err != nil {
		t.Fatal(err)
	}
	b := &session.WebBundle{SessionID: dummySessionID, CSRFToken: "csrf123", DSUserID: dummyUserID}
	b.Normalize()

	resp, err := c.FetchSelfProfile(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}

	req := stub.captured
	if req == nil {
		t.Fatal("no request captured")
	}
	// Required headers.
	if got := req.Header.Get("X-IG-App-ID"); got != session.WebAppID {
		t.Fatalf("X-IG-App-ID = %q, want %q", got, session.WebAppID)
	}
	if got := req.Header.Get("User-Agent"); got == "" {
		t.Fatal("User-Agent must be set")
	}
	if got := req.Header.Get("X-CSRFToken"); got != "csrf123" {
		t.Fatalf("X-CSRFToken = %q", got)
	}
	// The cookie jar must have attached the session cookie to the outbound request.
	cookie := req.Header.Get("Cookie")
	if !strings.Contains(cookie, "sessionid=") {
		t.Fatalf("outbound Cookie header missing sessionid: %q", cookie)
	}
	// Header extraction into RawResponse.
	if resp.Status != 200 {
		t.Fatalf("status = %d", resp.Status)
	}
	if resp.WWWClaim != "hmac.testclaim" {
		t.Fatalf("WWWClaim not extracted: %q", resp.WWWClaim)
	}
	if resp.ContentType != "application/json" {
		t.Fatalf("ContentType = %q", resp.ContentType)
	}
}

func TestFetchSelfProfileRunsThroughClassifier(t *testing.T) {
	// Full path: fetch (stubbed) → Classify → OK(1).
	stub := &stubTransport{resp: newResp(200, `{"status":"ok","user":{"pk":`+dummyUserID+`,"username":"testuser","full_name":"T"}}`)}
	c, err := New(WithTransport(stub))
	if err != nil {
		t.Fatal(err)
	}
	b := &session.WebBundle{SessionID: dummySessionID, DSUserID: dummyUserID}
	b.Normalize()

	pr, err := ig.Probe(context.Background(), c, b)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Account == nil || pr.Account.Username != "testuser" || pr.Transport != "web" {
		t.Fatalf("expected OK via web transport with account, got %+v", pr)
	}
}
