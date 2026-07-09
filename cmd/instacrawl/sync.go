package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ss251/instacrawl/internal/appmeta"
	"github.com/ss251/instacrawl/internal/dmsync"
	"github.com/ss251/instacrawl/internal/feedsync"
	"github.com/ss251/instacrawl/internal/media"
	"github.com/ss251/instacrawl/internal/pace"
	"github.com/ss251/instacrawl/internal/session"
	"github.com/ss251/instacrawl/internal/store"
)

// Injectable archiver seams for tests.
var (
	syncArchiveFn = dmsync.Archive
	syncFeedFn    = feedsync.Archive
)

func cmdSync(args []string) error {
	fs := newAuthFlags("sync")
	fetcher := fs.String("fetcher", "web", "transport: web|instagrapi")
	only := fs.String("only", "all", "scope: all|dms|feed")
	// DM flags.
	includePending := fs.Bool("include-pending", true, "also archive message requests (pending inbox)")
	onlyThread := fs.String("thread", "", "archive only this DM thread id")
	maxInbox := fs.Int("max-inbox-pages", 0, "cap inbox pages (0 = default)")
	maxThread := fs.Int("max-thread-pages", 0, "cap per-thread pages (0 = default)")
	// Feed flags.
	includeSaved := fs.Bool("include-saved", true, "archive saved posts")
	includeCollections := fs.Bool("include-collections", true, "archive saved collections")
	includeStories := fs.Bool("include-stories", true, "archive currently-live stories (24h)")
	includeLiked := fs.Bool("include-liked", false, "attempt the (usually restricted) liked feed")
	noMedia := fs.Bool("no-media", false, "skip downloading media bytes")
	maxFeed := fs.Int("max-feed-pages", 0, "cap per-feed pages (0 = default)")
	// Shared.
	fullRecrawl := fs.Bool("full-recrawl", false, "re-crawl everything from scratch")
	if err := fs.Parse(args); err != nil {
		return usageError{msg: err.Error()}
	}
	if fs.NArg() > 0 {
		return usageError{msg: fmt.Sprintf("sync takes no positional arguments, got %v", fs.Args())}
	}
	doDMs := *only == "all" || *only == "dms"
	doFeed := *only == "all" || *only == "feed"
	if !doDMs && !doFeed {
		return usageError{msg: fmt.Sprintf("unknown --only %q (want all|dms|feed)", *only)}
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

	pacer := pace.New(2*time.Second, 5*time.Second)

	if doDMs {
		sum, aerr := syncArchiveFn(ctx, dmsync.Deps{
			Fetcher: f, Store: st, Bundle: bundle, Pacer: pacer,
			MaxInboxPages: *maxInbox, MaxThreadPages: *maxThread,
			IncludePending: *includePending, FullRecrawl: *fullRecrawl, OnlyThread: *onlyThread,
		})
		if aerr != nil {
			fmt.Fprintf(os.Stderr, "instacrawl: %s\n", session.Sanitize(aerr.Error()))
			return errReported
		}
		if sum.State != "OK" {
			return reportTerminal("dms", sum.State, sum.Reason, sum.ChallengeURL)
		}
		pending := ""
		if sum.PendingUnavailable {
			pending = " (message-requests inbox unavailable)"
		}
		fmt.Printf("DMs — %d thread(s): %d archived, %d unchanged; %d new message(s)%s.\n",
			sum.InboxThreads, sum.ThreadsFetched, sum.ThreadsSkipped, sum.MessagesNew, pending)
	}

	if doFeed {
		var dl *media.Downloader
		if !*noMedia {
			dl = media.New(appmeta.MediaDir())
		}
		sum, ferr := syncFeedFn(ctx, feedsync.Deps{
			Fetcher: f, Store: st, Bundle: bundle, Pacer: pacer, Media: dl,
			DSUserID:     bundle.DSUserID,
			MaxFeedPages: *maxFeed, IncludeSaved: *includeSaved, IncludeCollections: *includeCollections,
			IncludeStories: *includeStories, IncludeLiked: *includeLiked, NoMedia: *noMedia, FullRecrawl: *fullRecrawl,
		})
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "instacrawl: %s\n", session.Sanitize(ferr.Error()))
			return errReported
		}
		if sum.State != "OK" {
			return reportTerminal("feed", sum.State, sum.Reason, sum.ChallengeURL)
		}
		liked := ""
		if sum.LikedUnavailable {
			liked = " (liked feed unavailable)"
		}
		fmt.Printf("Feed — %d new media, %d bytes downloaded, %d stories, %d collections%s.\n",
			sum.MediaNew, sum.BlobsDownloaded, sum.Stories, sum.Collections, liked)
	}

	// Best-effort: merge the FTS index b-trees after a fully successful sync. A
	// failure here is a warning, never a sync failure (the index self-heals and
	// `search --reindex` can rebuild it).
	if err := st.OptimizeSearch(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "instacrawl: warning: optimizing search index: %v\n", err)
	}
	return nil
}

func reportTerminal(scope, state, reason, challengeURL string) error {
	fmt.Fprintf(os.Stderr, "instacrawl: %s: %s — %s\n", scope, state, reason)
	if challengeURL != "" {
		fmt.Fprintf(os.Stderr, "  resolve the challenge here: %s\n  then re-run.\n", challengeURL)
	}
	return errReported
}
