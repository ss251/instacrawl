package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ss251/instacrawl/internal/appmeta"
	"github.com/ss251/instacrawl/internal/dmsync"
	"github.com/ss251/instacrawl/internal/pace"
	"github.com/ss251/instacrawl/internal/session"
	"github.com/ss251/instacrawl/internal/store"
)

// syncArchiveFn is the archiver, injectable for tests.
var syncArchiveFn = dmsync.Archive

func cmdSync(args []string) error {
	fs := newAuthFlags("sync")
	fetcher := fs.String("fetcher", "web", "transport: web|instagrapi")
	includePending := fs.Bool("include-pending", true, "also archive message requests (pending inbox)")
	fullRecrawl := fs.Bool("full-recrawl", false, "re-crawl every thread from scratch")
	onlyThread := fs.String("thread", "", "archive only this thread id")
	maxInbox := fs.Int("max-inbox-pages", 0, "cap inbox pages (0 = default)")
	maxThread := fs.Int("max-thread-pages", 0, "cap per-thread pages (0 = default)")
	if err := fs.Parse(args); err != nil {
		return usageError{msg: err.Error()}
	}
	if fs.NArg() > 0 {
		return usageError{msg: fmt.Sprintf("sync takes no positional arguments, got %v", fs.Args())}
	}

	ctx := context.Background()
	cfg, err := session.LoadConfig(appmeta.ConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	bundle, _, err := session.ResolveBundle(cfg, getenvFn)
	if err != nil {
		if errors.Is(err, session.ErrNoSession) {
			fmt.Fprintln(os.Stderr, "instacrawl: no session configured — run `instacrawl auth import`")
			return errReported
		}
		return fmt.Errorf("resolve session: %s", session.Sanitize(err.Error()))
	}

	f, err := newFetcher(*fetcher)
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, appmeta.DatabasePath())
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = st.Close() }()

	sum, err := syncArchiveFn(ctx, dmsync.Deps{
		Fetcher:        f,
		Store:          st,
		Bundle:         bundle,
		Pacer:          pace.New(2*time.Second, 5*time.Second),
		MaxInboxPages:  *maxInbox,
		MaxThreadPages: *maxThread,
		IncludePending: *includePending,
		FullRecrawl:    *fullRecrawl,
		OnlyThread:     *onlyThread,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "instacrawl: %s\n", session.Sanitize(err.Error()))
		return errReported
	}

	if sum.State != "OK" {
		fmt.Fprintf(os.Stderr, "instacrawl: %s — %s\n", sum.State, sum.Reason)
		if sum.ChallengeURL != "" {
			fmt.Fprintf(os.Stderr, "  resolve the challenge here: %s\n  then re-run.\n", sum.ChallengeURL)
		}
		return errReported
	}
	fmt.Printf("OK — %d thread(s): %d archived, %d unchanged; %d new message(s).\n",
		sum.InboxThreads, sum.ThreadsFetched, sum.ThreadsSkipped, sum.MessagesNew)
	return nil
}
