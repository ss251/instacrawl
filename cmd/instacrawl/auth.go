package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/openclaw/crawlkit/output"

	"github.com/ss251/instacrawl/internal/appmeta"
	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/igmobile"
	"github.com/ss251/instacrawl/internal/igweb"
	"github.com/ss251/instacrawl/internal/reliability"
	"github.com/ss251/instacrawl/internal/session"
	"github.com/ss251/instacrawl/internal/store"
)

// Seams — overridable in tests so the whole auth flow runs offline with no live
// IG and no real keychain.
var (
	// probeFn runs a read-only probe through the chosen transport.
	probeFn = func(ctx context.Context, fetcherKind string, b *session.WebBundle) (ig.ProbeResult, error) {
		f, err := newFetcher(fetcherKind)
		if err != nil {
			return ig.ProbeResult{}, err
		}
		return ig.Probe(ctx, f, b)
	}
	storeBundleFn            = session.StoreBundle
	deleteBundleFn           = session.DeleteBundle
	getenvFn                 = os.Getenv
	stdin          io.Reader = os.Stdin
)

func newFetcher(kind string) (ig.Fetcher, error) {
	switch strings.TrimSpace(kind) {
	case "", "web":
		return igweb.New()
	case "instagrapi", "mobile":
		return igmobile.New(), nil
	default:
		return nil, usageError{msg: fmt.Sprintf("unknown --fetcher %q (want web|instagrapi)", kind)}
	}
}

func cmdAuth(jsonFlag bool, args []string) error {
	if len(args) == 0 {
		return usageError{msg: "auth needs a subcommand: import | status | logout"}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "import":
		return cmdAuthImport(rest)
	case "status":
		return cmdAuthStatus(jsonFlag, rest)
	case "logout":
		return cmdAuthLogout(rest)
	default:
		return usageError{msg: fmt.Sprintf("unknown auth subcommand %q (want import|status|logout)", sub)}
	}
}

func newAuthFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own usage errors
	return fs
}

func cmdAuthImport(args []string) error {
	fs := newAuthFlags("auth import")
	fromFile := fs.String("from-file", "", "read the session/cookies from a file")
	useStdin := fs.Bool("stdin", false, "read the session/cookies from stdin")
	noVerify := fs.Bool("no-verify", false, "store without probing (offline import)")
	fetcher := fs.String("fetcher", "web", "transport: web|instagrapi")
	browser := fs.String("browser", "", "import from a logged-in browser (not yet implemented)")
	if err := fs.Parse(args); err != nil {
		return usageError{msg: err.Error()}
	}
	if fs.NArg() > 0 {
		return usageError{msg: "auth import takes no positional arguments — never pass a sessionid on the command line (it leaks into shell history); use --stdin or --from-file"}
	}
	if strings.TrimSpace(*browser) != "" {
		return usageError{msg: "--browser import is not implemented in this phase; export your instagram.com cookies and use --from-file or --stdin"}
	}
	_ = useStdin // stdin is the default when --from-file is absent; the flag is accepted for explicitness

	raw, acquired, err := readImportInput(*fromFile)
	if err != nil {
		return err
	}
	bundle, err := session.Parse(raw, acquired)
	if err != nil {
		return fmt.Errorf("parse session: %s", session.Sanitize(err.Error()))
	}

	if *noVerify {
		if err := saveBundle(bundle); err != nil {
			return err
		}
		fmt.Printf("Stored session for ds_user_id=%s WITHOUT verification (--no-verify). Run `instacrawl auth status` to verify.\n", bundle.DSUserID)
		return nil
	}

	ctx := context.Background()
	pr, perr := probeFn(ctx, *fetcher, bundle)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "instacrawl: could not verify session: %s\n", session.Sanitize(perr.Error()))
		return errReported
	}
	if pr.Verdict.State != reliability.StateOK {
		printTerminal(pr)
		return errReported
	}
	if err := persistOnOK(ctx, bundle, pr); err != nil {
		return err
	}
	fmt.Printf("OK — logged in as @%s (ds_user_id=%s). Session saved to the OS keychain.\n", pr.Account.Username, pr.Account.PK)
	return nil
}

func cmdAuthStatus(jsonFlag bool, args []string) error {
	fs := newAuthFlags("auth status")
	offline := fs.Bool("offline", false, "do not hit the network; report configured session only")
	fetcher := fs.String("fetcher", "web", "transport: web|instagrapi")
	// --json is hoisted globally by run(); accept it here too for tolerance.
	localJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return usageError{msg: err.Error()}
	}
	jsonOut := jsonFlag || *localJSON

	cfg, err := session.LoadConfig(appmeta.ConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	bundle, source, err := session.ResolveBundle(cfg, getenvFn)
	if err != nil {
		if errors.Is(err, session.ErrNoSession) {
			fmt.Fprintln(os.Stderr, "instacrawl: no session configured — run `instacrawl auth import`")
			return errReported
		}
		return fmt.Errorf("resolve session: %s", session.Sanitize(err.Error()))
	}

	if *offline {
		return printStatus(statusView{State: "unverified", Source: source, Verified: false, Session: bundle.Redacted()}, jsonOut)
	}

	ctx := context.Background()
	pr, perr := probeFn(ctx, *fetcher, bundle)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "instacrawl: %s\n", session.Sanitize(perr.Error()))
		return errReported
	}
	if pr.Verdict.State != reliability.StateOK {
		printTerminal(pr)
		return errReported
	}
	// Re-affirm the account row (idempotent no-op beyond fetched_at).
	if err := upsertAccount(ctx, pr); err != nil {
		return err
	}
	return printStatus(statusView{
		State:    "OK",
		Source:   source,
		Verified: true,
		Username: pr.Account.Username,
		Session:  bundle.Redacted(),
	}, jsonOut)
}

func cmdAuthLogout(args []string) error {
	fs := newAuthFlags("auth logout")
	if err := fs.Parse(args); err != nil {
		return usageError{msg: err.Error()}
	}
	if err := deleteBundleFn(session.KeychainService, session.KeychainAccount); err != nil {
		return fmt.Errorf("logout: %s", session.Sanitize(err.Error()))
	}
	// Best-effort: record that no session is configured.
	_ = session.SaveConfig(appmeta.ConfigPath(), session.Config{Session: session.SessionMeta{Source: "none"}})
	fmt.Println("Logged out — session removed from the OS keychain.")
	return nil
}

// --- helpers ---

func readImportInput(fromFile string) (raw, acquired string, err error) {
	if strings.TrimSpace(fromFile) != "" {
		data, rerr := os.ReadFile(fromFile)
		if rerr != nil {
			return "", "", fmt.Errorf("read %s: %w", fromFile, rerr)
		}
		return string(data), "cookiefile", nil
	}
	data, rerr := io.ReadAll(stdin)
	if rerr != nil {
		return "", "", fmt.Errorf("read stdin: %w", rerr)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", "", usageError{msg: "no input — pipe cookies to stdin or pass --from-file"}
	}
	return string(data), "paste", nil
}

func saveBundle(b *session.WebBundle) error {
	if err := storeBundleFn(session.KeychainService, session.KeychainAccount, b); err != nil {
		return fmt.Errorf("save session: %s", session.Sanitize(err.Error()))
	}
	cfg := session.Config{Session: session.SessionMeta{
		Source:  "keyring",
		Service: session.KeychainService,
		Account: session.KeychainAccount,
	}}
	if err := session.SaveConfig(appmeta.ConfigPath(), cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func persistOnOK(ctx context.Context, b *session.WebBundle, pr ig.ProbeResult) error {
	if err := saveBundle(b); err != nil {
		return err
	}
	return upsertAccount(ctx, pr)
}

func upsertAccount(ctx context.Context, pr ig.ProbeResult) error {
	if pr.Account == nil {
		return nil
	}
	st, err := store.Open(ctx, appmeta.DatabasePath())
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.UpsertAccount(ctx, pr.Account, "web", nil); err != nil {
		return err
	}
	return nil
}

func printTerminal(pr ig.ProbeResult) {
	fmt.Fprintf(os.Stderr, "instacrawl: %s — %s\n", pr.Verdict.State, pr.Verdict.Reason)
	if pr.ChallengeURL != "" {
		fmt.Fprintf(os.Stderr, "  resolve the challenge here: %s\n  then re-run.\n", pr.ChallengeURL)
	}
	if pr.Verdict.State == reliability.StateSchemaDrift && pr.RawBody != "" {
		fmt.Fprintln(os.Stderr, "  (raw response retained for triage)")
	}
}

type statusView struct {
	State    string                 `json:"state"`
	Source   string                 `json:"source"`
	Verified bool                   `json:"verified"`
	Username string                 `json:"username,omitempty"`
	Session  session.RedactedBundle `json:"session"`
}

func printStatus(v statusView, jsonOut bool) error {
	if jsonOut {
		return output.Write(os.Stdout, output.JSON, "auth_status", v)
	}
	verified := "not verified"
	if v.Verified {
		verified = "verified OK"
	}
	who := v.Session.DSUserID
	if v.Username != "" {
		who = "@" + v.Username + " (ds_user_id=" + v.Session.DSUserID + ")"
	}
	fmt.Printf("%s — %s [source=%s]\n", who, verified, v.Source)
	return nil
}
