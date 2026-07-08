package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/reliability"
	"github.com/ss251/instacrawl/internal/session"
)

// installAuthStubs redirects the auth seams at in-memory fakes and points HOME
// at a temp dir so no real keychain, network, or home archive is touched.
func installAuthStubs(t *testing.T) (stored *map[string]*session.WebBundle) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	kc := map[string]*session.WebBundle{}
	origStore, origDelete, origProbe, origStdin := storeBundleFn, deleteBundleFn, probeFn, stdin
	storeBundleFn = func(service, account string, b *session.WebBundle) error {
		kc[service+"/"+account] = b
		return nil
	}
	deleteBundleFn = func(service, account string) error {
		delete(kc, service+"/"+account)
		return nil
	}
	t.Cleanup(func() {
		storeBundleFn, deleteBundleFn, probeFn, stdin = origStore, origDelete, origProbe, origStdin
	})
	return &kc
}

func okProbe(username, pk string) func(context.Context, string, *session.WebBundle) (ig.ProbeResult, error) {
	return func(context.Context, string, *session.WebBundle) (ig.ProbeResult, error) {
		return ig.ProbeResult{
			Verdict: reliability.OK(1),
			Account: &ig.Account{PK: pk, Username: username, RawJSON: `{"ok":true}`},
		}, nil
	}
}

func TestAuthImportOKSavesAndExits0(t *testing.T) {
	kc := installAuthStubs(t)
	probeFn = okProbe("testuser", "17841400000000000")
	stdin = strings.NewReader("sessionid=17841400000000000%3Afake; csrftoken=abc")

	if code := run([]string{"auth", "import", "--stdin"}); code != 0 {
		t.Fatalf("expected exit 0 on OK import, got %d", code)
	}
	if _, ok := (*kc)[session.KeychainService+"/"+session.KeychainAccount]; !ok {
		t.Fatal("OK import must save the bundle to the keychain")
	}
}

func TestAuthImportCheckpointDoesNotSaveAndExits1(t *testing.T) {
	kc := installAuthStubs(t)
	probeFn = func(context.Context, string, *session.WebBundle) (ig.ProbeResult, error) {
		return ig.ProbeResult{
			Verdict:      reliability.Fail(reliability.StateAuthCheckpoint, "logged out"),
			ChallengeURL: "https://www.instagram.com/challenge/AB/CD/",
		}, nil
	}
	stdin = strings.NewReader("sessionid=17841400000000000%3Afake")

	if code := run([]string{"auth", "import", "--stdin"}); code != 1 {
		t.Fatalf("expected exit 1 on checkpoint, got %d", code)
	}
	if len(*kc) != 0 {
		t.Fatal("a checkpoint import must NOT save a bundle")
	}
}

func TestAuthImportRejectsPositionalArg(t *testing.T) {
	installAuthStubs(t)
	// A sessionid as a positional arg would leak into shell history.
	if code := run([]string{"auth", "import", "sessionid=leak"}); code != 2 {
		t.Fatalf("positional arg must be a usage error (exit 2), got %d", code)
	}
}

func TestAuthImportNoVerifyStoresWithoutProbe(t *testing.T) {
	kc := installAuthStubs(t)
	probeFn = func(context.Context, string, *session.WebBundle) (ig.ProbeResult, error) {
		t.Fatal("--no-verify must not probe")
		return ig.ProbeResult{}, nil
	}
	stdin = strings.NewReader("sessionid=17841400000000000%3Afake")
	if code := run([]string{"auth", "import", "--stdin", "--no-verify"}); code != 0 {
		t.Fatalf("no-verify import should exit 0, got %d", code)
	}
	if len(*kc) != 1 {
		t.Fatal("no-verify import should still store the bundle")
	}
}

func TestAuthStatusViaEnvOK(t *testing.T) {
	installAuthStubs(t)
	probeFn = okProbe("testuser", "17841400000000000")
	t.Setenv(session.EnvSessionID, "17841400000000000%3Afake")

	if code := run([]string{"auth", "status"}); code != 0 {
		t.Fatalf("env-resolved OK status should exit 0, got %d", code)
	}
}

func TestAuthStatusNoSessionExits1(t *testing.T) {
	installAuthStubs(t)
	// No env, no config, keychain empty → ErrNoSession.
	if code := run([]string{"auth", "status", "--offline"}); code != 1 {
		t.Fatalf("no session should exit 1, got %d", code)
	}
}

func TestAuthUnknownSubcommandIsUsage(t *testing.T) {
	installAuthStubs(t)
	if code := run([]string{"auth", "bogus"}); code != 2 {
		t.Fatalf("unknown auth subcommand should be usage (exit 2), got %d", code)
	}
}

func TestAuthLogoutExits0(t *testing.T) {
	installAuthStubs(t)
	if code := run([]string{"auth", "logout"}); code != 0 {
		t.Fatalf("logout should exit 0, got %d", code)
	}
}
