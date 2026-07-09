package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ss251/instacrawl/internal/browsercookie"
	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/reliability"
	"github.com/ss251/instacrawl/internal/session"
)

// keychainKey is the map key installAuthStubs stores bundles under.
func keychainKey() string { return session.KeychainService + "/" + session.KeychainAccount }

// checkpointProbe is a probe that reports the server rejected the session.
func checkpointProbe() func(context.Context, string, *session.WebBundle) (ig.ProbeResult, error) {
	return func(context.Context, string, *session.WebBundle) (ig.ProbeResult, error) {
		return ig.ProbeResult{
			Verdict:      reliability.Fail(reliability.StateAuthCheckpoint, "logged out"),
			ChallengeURL: "https://www.instagram.com/challenge/AB/CD/",
		}, nil
	}
}

// cand builds a browser candidate with a valid sessionid for the given account.
func cand(browser, profile, dsUserID string) browsercookie.Candidate {
	return browsercookie.Candidate{
		Browser:  browser,
		Profile:  profile,
		DSUserID: dsUserID,
		Cookies: map[string]string{
			"sessionid":  dsUserID + "%3Afake",
			"ds_user_id": dsUserID,
			"csrftoken":  "abc",
		},
	}
}

// fakeBrowser sets browserSourceFn to return canned candidates.
func fakeBrowser(cands []browsercookie.Candidate, err error) {
	browserSourceFn = func(context.Context, string) ([]browsercookie.Candidate, error) {
		return cands, err
	}
}

func TestBrowserImportOKSavesWithProvenance(t *testing.T) {
	kc := installAuthStubs(t)
	probeFn = okProbe("testuser", "17841400000000000")
	fakeBrowser([]browsercookie.Candidate{cand("chrome", "Default", "17841400000000000")}, nil)

	if code := run([]string{"auth", "import", "--from-browser", "chrome"}); code != 0 {
		t.Fatalf("browser import should exit 0, got %d", code)
	}
	b, ok := (*kc)[keychainKey()]
	if !ok {
		t.Fatal("browser import must save the bundle")
	}
	if b.Acquired != "browser:chrome" {
		t.Fatalf("provenance should be browser:chrome, got %q", b.Acquired)
	}
	if b.SessionID == "" || b.DSUserID != "17841400000000000" {
		t.Fatalf("bundle not mapped correctly: %+v", b.Redacted())
	}
}

func TestBrowserImportUnknownBrowserIsUsage(t *testing.T) {
	installAuthStubs(t)
	fakeBrowser(nil, nil)
	if code := run([]string{"auth", "import", "--from-browser", "netscape"}); code != 2 {
		t.Fatalf("unknown browser must be usage (exit 2), got %d", code)
	}
}

func TestBrowserImportMutualExclusion(t *testing.T) {
	installAuthStubs(t)
	fakeBrowser([]browsercookie.Candidate{cand("chrome", "Default", "1")}, nil)
	stdin = strings.NewReader("sessionid=1%3Afake")
	if code := run([]string{"auth", "import", "--from-browser", "chrome", "--stdin"}); code != 2 {
		t.Fatalf("--from-browser + --stdin must be a usage error, got %d", code)
	}
	if code := run([]string{"auth", "import", "--from-browser", "chrome", "--from-file", "/tmp/x"}); code != 2 {
		t.Fatalf("--from-browser + --from-file must be a usage error, got %d", code)
	}
}

func TestBrowserImportNoSessionExits1(t *testing.T) {
	kc := installAuthStubs(t)
	fakeBrowser(nil, nil) // no candidates
	if code := run([]string{"auth", "import", "--from-browser", "auto"}); code != 1 {
		t.Fatalf("no session found must exit 1, got %d", code)
	}
	if len(*kc) != 0 {
		t.Fatal("nothing must be stored when no session is found")
	}
}

func TestBrowserImportSameAccountAcrossProfilesPicksDefault(t *testing.T) {
	kc := installAuthStubs(t)
	probeFn = okProbe("testuser", "999")
	// Two profiles, same account; the source returns default-first.
	fakeBrowser([]browsercookie.Candidate{
		cand("chrome", "Default", "999"),
		cand("chrome", "Profile 1", "999"),
	}, nil)
	if code := run([]string{"auth", "import", "--from-browser", "chrome"}); code != 0 {
		t.Fatalf("same-account multi-profile should import the default, got %d", code)
	}
	if len(*kc) != 1 {
		t.Fatal("exactly one bundle should be stored")
	}
}

func TestBrowserImportDifferentAccountsDisambiguates(t *testing.T) {
	kc := installAuthStubs(t)
	probeFn = okProbe("testuser", "111")
	fakeBrowser([]browsercookie.Candidate{
		cand("chrome", "Default", "111"),
		cand("chrome", "Profile 1", "222"),
	}, nil)
	if code := run([]string{"auth", "import", "--from-browser", "chrome"}); code != 2 {
		t.Fatalf("two different accounts must force a usage error to pick --profile, got %d", code)
	}
	if len(*kc) != 0 {
		t.Fatal("nothing must be stored while the account is ambiguous")
	}
}

func TestBrowserImportProfileFilterResolvesAmbiguity(t *testing.T) {
	kc := installAuthStubs(t)
	probeFn = okProbe("testuser", "222")
	fakeBrowser([]browsercookie.Candidate{
		cand("chrome", "Default", "111"),
		cand("chrome", "Profile 1", "222"),
	}, nil)
	if code := run([]string{"auth", "import", "--from-browser", "chrome", "--profile", "Profile 1"}); code != 0 {
		t.Fatalf("--profile should resolve the ambiguity and import, got %d", code)
	}
	if b, ok := (*kc)[keychainKey()]; !ok || b.DSUserID != "222" {
		t.Fatalf("wrong profile imported: %+v", b)
	}
}

func TestBrowserImportCheckpointDoesNotSave(t *testing.T) {
	kc := installAuthStubs(t)
	probeFn = checkpointProbe()
	fakeBrowser([]browsercookie.Candidate{cand("chrome", "Default", "17841400000000000")}, nil)
	if code := run([]string{"auth", "import", "--from-browser", "chrome"}); code != 1 {
		t.Fatalf("a browser session the server rejects must exit 1, got %d", code)
	}
	if len(*kc) != 0 {
		t.Fatal("a rejected session must not be saved")
	}
}

// A cookie-read error carrying a secret must be scrubbed before it is printed.
func TestBrowserImportErrorIsSanitized(t *testing.T) {
	installAuthStubs(t)
	secret := "17841400000000000%3AsuperSecretValue"
	fakeBrowser(nil, &leakyError{msg: "boom sessionid=" + secret})

	out := captureStderr(t, func() {
		_ = run([]string{"auth", "import", "--from-browser", "chrome"})
	})
	if strings.Contains(out, "superSecretValue") {
		t.Fatalf("a session secret leaked into output: %q", out)
	}
}

type leakyError struct{ msg string }

func (e *leakyError) Error() string { return e.msg }

// captureStderr runs fn with os.Stderr redirected and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}
