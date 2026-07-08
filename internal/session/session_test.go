package session

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

const (
	// Dummy, non-real values. NEVER put a real sessionid in a test.
	testUserID    = "17841400000000000"
	testSessionID = testUserID + "%3ATESTSESSIONonlyfake%3Aphase1"
)

func TestDeriveUserIDFromSessionID(t *testing.T) {
	b := &WebBundle{SessionID: testSessionID}
	if got := b.DeriveUserID(); got != testUserID {
		t.Fatalf("DeriveUserID = %q, want %q", got, testUserID)
	}
	// Non-numeric prefix → no id.
	if got := (&WebBundle{SessionID: "notanid:blah"}).DeriveUserID(); got != "" {
		t.Fatalf("expected empty derive for non-numeric prefix, got %q", got)
	}
}

func TestNormalizeFillsDefaultsAndDerivesID(t *testing.T) {
	b := &WebBundle{SessionID: testSessionID}
	b.Normalize()
	if b.AppID != WebAppID {
		t.Fatalf("AppID default = %q", b.AppID)
	}
	if b.UserAgent == "" {
		t.Fatal("UserAgent should default")
	}
	if b.DSUserID != testUserID {
		t.Fatalf("DSUserID derived = %q, want %q", b.DSUserID, testUserID)
	}
}

func TestValidateRequiresSessionAndIdentity(t *testing.T) {
	if err := (&WebBundle{}).Validate(); err == nil {
		t.Fatal("empty bundle must fail validation")
	}
	// sessionid with a non-numeric prefix and no ds_user_id → no identity.
	b := &WebBundle{SessionID: "garbage-no-colon"}
	b.Normalize()
	if err := b.Validate(); err == nil {
		t.Fatal("bundle with no derivable identity must fail validation")
	}
	ok := &WebBundle{SessionID: testSessionID}
	ok.Normalize()
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid bundle failed: %v", err)
	}
}

func TestStringAndRedactedLeakNoSecret(t *testing.T) {
	b := &WebBundle{SessionID: testSessionID, CSRFToken: "csrfsecret", DSUserID: testUserID}
	b.Normalize()
	s := b.String()
	if strings.Contains(s, "TESTSESSION") || strings.Contains(s, "csrfsecret") {
		t.Fatalf("String() leaked a secret: %q", s)
	}
	r := b.Redacted()
	if !r.HasSessionID || !r.HasCSRFToken {
		t.Fatal("Redacted must report presence booleans")
	}
	blob, _ := json.Marshal(r)
	if strings.Contains(string(blob), "TESTSESSION") || strings.Contains(string(blob), "csrfsecret") {
		t.Fatalf("Redacted JSON leaked a secret: %s", blob)
	}
}

func TestParseCookieHeader(t *testing.T) {
	raw := "sessionid=" + testSessionID + "; csrftoken=abc123; ds_user_id=" + testUserID + "; mid=xyz"
	b, err := ParseCookieHeader(raw, "paste")
	if err != nil {
		t.Fatal(err)
	}
	if b.SessionID != testSessionID || b.CSRFToken != "abc123" || b.DSUserID != testUserID || b.Mid != "xyz" {
		t.Fatalf("parsed bundle wrong: %+v", b.Redacted())
	}
	if b.Acquired != "paste" {
		t.Fatalf("acquired = %q", b.Acquired)
	}
}

func TestParseJSONExport(t *testing.T) {
	raw := `[
	  {"name":"sessionid","value":"` + testSessionID + `","domain":".instagram.com"},
	  {"name":"csrftoken","value":"abc123","domain":".instagram.com"},
	  {"name":"unrelated","value":"nope","domain":".example.com"}
	]`
	b, err := ParseJSONExport(raw, "json")
	if err != nil {
		t.Fatal(err)
	}
	if b.SessionID != testSessionID || b.CSRFToken != "abc123" {
		t.Fatalf("parsed export wrong: %+v", b.Redacted())
	}
}

func TestParseCookiesTxt(t *testing.T) {
	raw := "# Netscape HTTP Cookie File\n" +
		".instagram.com\tTRUE\t/\tTRUE\t9999999999\tsessionid\t" + testSessionID + "\n" +
		".instagram.com\tTRUE\t/\tTRUE\t9999999999\tcsrftoken\tabc123\n" +
		".example.com\tTRUE\t/\tTRUE\t0\tsessionid\tSHOULD_BE_IGNORED\n"
	b, err := ParseCookiesTxt(raw, "cookiefile")
	if err != nil {
		t.Fatal(err)
	}
	if b.SessionID != testSessionID {
		t.Fatalf("cookies.txt sessionid = %q", b.SessionID)
	}
}

func TestParseAutodetect(t *testing.T) {
	cases := map[string]string{
		"header": "sessionid=" + testSessionID + "; csrftoken=abc",
		"json":   `[{"name":"sessionid","value":"` + testSessionID + `"}]`,
		"bundle": `{"sessionid":"` + testSessionID + `"}`,
	}
	for name, raw := range cases {
		b, err := Parse(raw, "paste")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if b.SessionID != testSessionID {
			t.Fatalf("%s: sessionid = %q", name, b.SessionID)
		}
	}
}

func TestKeychainRoundTrip(t *testing.T) {
	store := map[string]string{}
	sessionKeyringSet = func(s, a, v string) error { store[s+"/"+a] = v; return nil }
	sessionKeyringGet = func(s, a string) (string, error) {
		v, ok := store[s+"/"+a]
		if !ok {
			return "", keyring.ErrNotFound
		}
		return v, nil
	}
	sessionKeyringDelete = func(s, a string) error { delete(store, s+"/"+a); return nil }
	t.Cleanup(func() {
		sessionKeyringSet, sessionKeyringGet, sessionKeyringDelete = keyring.Set, keyring.Get, keyring.Delete
	})

	b := &WebBundle{SessionID: testSessionID}
	b.Normalize()
	if err := StoreBundle(KeychainService, KeychainAccount, b); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBundle(KeychainService, KeychainAccount)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != testSessionID || got.DSUserID != testUserID {
		t.Fatalf("round-trip mismatch: %+v", got.Redacted())
	}
	if err := DeleteBundle(KeychainService, KeychainAccount); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(KeychainService, KeychainAccount); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expected ErrNoSession after delete, got %v", err)
	}
	// Deleting again is idempotent.
	if err := DeleteBundle(KeychainService, KeychainAccount); err != nil {
		t.Fatalf("second delete should be a no-op: %v", err)
	}
}

func TestResolveBundleEnvBeatsKeychain(t *testing.T) {
	sessionKeyringGet = func(s, a string) (string, error) { return "", keyring.ErrNotFound }
	t.Cleanup(func() { sessionKeyringGet = keyring.Get })

	env := map[string]string{EnvSessionID: testSessionID}
	getenv := func(k string) string { return env[k] }
	b, source, err := ResolveBundle(Config{}, getenv)
	if err != nil {
		t.Fatal(err)
	}
	if source != "env" || b.DSUserID != testUserID {
		t.Fatalf("expected env source with derived id, got source=%q id=%q", source, b.DSUserID)
	}
}

func TestResolveBundleNoneReportsNoSession(t *testing.T) {
	cfg := Config{Session: SessionMeta{Source: "none"}}
	_, _, err := ResolveBundle(cfg, func(string) string { return "" })
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("source=none must yield ErrNoSession, got %v", err)
	}
}

func TestSanitizeScrubsSecrets(t *testing.T) {
	cases := []string{
		"cookie: sessionid=" + testSessionID + "; csrftoken=abc",
		`{"sessionid":"` + testSessionID + `"}`,
		"failed GET https://i.instagram.com/api/v1/x sessionid=" + testSessionID,
		"Authorization: Bearer IGT:2:supersecrettoken",
	}
	for _, c := range cases {
		out := Sanitize(c)
		if strings.Contains(out, "TESTSESSION") || strings.Contains(out, "supersecrettoken") {
			t.Fatalf("Sanitize leaked a secret\n in:  %q\n out: %q", c, out)
		}
	}
}
