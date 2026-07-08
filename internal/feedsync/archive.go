// Package feedsync is the own-content archiver orchestrator: it wires the fetch
// transport (ig.Fetcher), the feed domain (internal/feed), the media byte
// downloader (internal/media), and persistence (internal/store) into the
// stories→own-feed→saved→collections→liked crawl. It lives in its own package
// (not internal/feed) so store can depend on feed's types without an import
// cycle — the same cut proven in Phase 2 (internal/dm ← store/dmsync).
//
// Crawl policy mirrors dmsync: each paginated feed uses three-way resume so an
// unchanged feed costs no new work on a re-run; stories are fetched FIRST
// because they are 24h-ephemeral; AUTH·CHECKPOINT / RATE·LIMITED halt the whole
// run; a per-feed SCHEMA·DRIFT is isolated (recorded, skipped) and the rest
// continue; the liked feed's 400 is a first-class "unavailable", never drift.
package feedsync

import (
	"context"
	"fmt"

	"github.com/ss251/instacrawl/internal/feed"
	"github.com/ss251/instacrawl/internal/ig"
	"github.com/ss251/instacrawl/internal/media"
	"github.com/ss251/instacrawl/internal/pace"
	"github.com/ss251/instacrawl/internal/reliability"
	"github.com/ss251/instacrawl/internal/session"
	"github.com/ss251/instacrawl/internal/store"
)

const (
	defaultMaxFeedPages = 200
	emptyPageLimit      = 3
	collectionListPages = 20
)

// Deps are the archiver's injected dependencies.
type Deps struct {
	Fetcher            ig.Fetcher
	Store              *store.Store
	Bundle             *session.WebBundle
	Pacer              *pace.Pacer
	Media              *media.Downloader
	DSUserID           string
	MaxFeedPages       int
	IncludeSaved       bool
	IncludeCollections bool
	IncludeStories     bool
	IncludeLiked       bool
	NoMedia            bool
	FullRecrawl        bool
}

// Summary reports the outcome of a run.
type Summary struct {
	Stories          int
	Collections      int
	MediaNew         int
	BlobsDownloaded  int
	BlobsMissing     int
	LikedUnavailable bool
	State            string
	Reason           string
	ChallengeURL     string
	DriftedResources []string
}

type terminal struct {
	verdict      reliability.Verdict
	challengeURL string
}

// outcome is a per-feed result: a terminal halts the whole run; a drift is
// isolated (recorded, skipped); the zero value means "OK, continue".
type outcome struct {
	terminal *terminal
	drift    *reliability.Verdict
}

// Archive runs the own-content crawl.
func Archive(ctx context.Context, d Deps) (Summary, error) {
	if d.MaxFeedPages <= 0 {
		d.MaxFeedPages = defaultMaxFeedPages
	}
	if d.Pacer == nil {
		d.Pacer = pace.New(0, 0)
	}

	sum := Summary{State: "OK"}
	beforeMedia, err := d.Store.CountMedia(ctx)
	if err != nil {
		return sum, err
	}
	beforeBlobs, err := d.Store.CountBlobs(ctx)
	if err != nil {
		return sum, err
	}

	// 1. Stories first (24h-ephemeral, single fetch).
	if d.IncludeStories {
		oc, n, missing, serr := archiveStories(ctx, d)
		if serr != nil {
			return sum, serr
		}
		if oc.terminal != nil {
			return terminalSummary(sum, oc.terminal), nil
		}
		sum.Stories = n
		sum.BlobsMissing += missing
	}

	// 2. Own feed, 3. Saved.
	for _, r := range coreFeeds(d) {
		oc, ferr := paginateFeed(ctx, d, r)
		if ferr != nil {
			return sum, ferr
		}
		if oc.terminal != nil {
			return terminalSummary(sum, oc.terminal), nil
		}
		if oc.drift != nil {
			sum.DriftedResources = append(sum.DriftedResources, r.Label)
		}
	}

	// 4. Collections (list → per-collection feed).
	if d.IncludeCollections {
		oc, cerr := archiveCollections(ctx, d, &sum)
		if cerr != nil {
			return sum, cerr
		}
		if oc.terminal != nil {
			return terminalSummary(sum, oc.terminal), nil
		}
	}

	// 5. Liked (best-effort; 400 → unavailable, not drift).
	if d.IncludeLiked {
		oc, lerr := archiveLiked(ctx, d, &sum)
		if lerr != nil {
			return sum, lerr
		}
		if oc.terminal != nil {
			return terminalSummary(sum, oc.terminal), nil
		}
	}

	afterMedia, err := d.Store.CountMedia(ctx)
	if err != nil {
		return sum, err
	}
	afterBlobs, err := d.Store.CountBlobs(ctx)
	if err != nil {
		return sum, err
	}
	sum.MediaNew = afterMedia - beforeMedia
	sum.BlobsDownloaded = afterBlobs - beforeBlobs
	if len(sum.DriftedResources) > 0 {
		sum.State = reliability.StateSchemaDrift.String()
		sum.Reason = fmt.Sprintf("%d feed(s) hit schema drift and were skipped; raw retained", len(sum.DriftedResources))
	}
	return sum, nil
}

// FeedResource describes one paginated feed to crawl.
type FeedResource struct {
	Entity   string
	Key      string
	Kind     string
	Label    string
	Build    func(maxID string) ig.Request
	Classify func(ig.RawResponse, reliability.Floor) feed.FeedResult
}

func coreFeeds(d Deps) []FeedResource {
	feeds := []FeedResource{{
		Entity: store.FeedEntityOwn, Key: d.DSUserID, Kind: store.FeedKindOwn, Label: "own_feed",
		Build:    func(maxID string) ig.Request { return ig.UserFeedRequest(d.DSUserID, maxID, 12) },
		Classify: feed.ClassifyUserFeed,
	}}
	if d.IncludeSaved {
		feeds = append(feeds, FeedResource{
			Entity: store.FeedEntitySaved, Key: "", Kind: store.FeedKindSaved, Label: "saved",
			Build:    ig.SavedFeedRequest,
			Classify: feed.ClassifySavedFeed,
		})
	}
	return feeds
}

// paginateFeed crawls one feed with three-way resume. The PageGuard gates the
// TRANSITION; the first fetch is unconditional.
func paginateFeed(ctx context.Context, d Deps, r FeedResource) (outcome, error) {
	prev, ok, err := d.Store.GetFeedCursor(ctx, r.Entity, r.Key)
	if err != nil {
		return outcome{}, err
	}
	if d.FullRecrawl {
		ok = false
	}
	hasHistory, err := d.Store.FeedHasHistory(ctx, r.Kind, r.Key)
	if err != nil {
		return outcome{}, err
	}

	guard := reliability.NewPageGuard(d.MaxFeedPages)
	maxID := ""
	newest := prev
	firstPage := true
	resolved := false
	stopAtPK := ""

	for {
		resp, ferr := d.Fetcher.Fetch(ctx, d.Bundle, r.Build(maxID))
		if ferr != nil {
			return outcome{}, fmt.Errorf("%s %s transport failure: %s", d.Fetcher.Name(), r.Entity, session.Sanitize(ferr.Error()))
		}
		floor := reliability.Floor{HasKnownHistory: hasHistory, ConsecutiveEmpty: newest.ConsecutiveEmpty, ConsecutiveEmptyLimit: emptyPageLimit}
		res := r.Classify(resp, floor)

		if res.Verdict.State == reliability.StateSchemaDrift {
			v := res.Verdict
			return outcome{drift: &v}, nil
		}
		if res.Verdict.State != reliability.StateOK {
			return outcome{terminal: &terminal{verdict: res.Verdict, challengeURL: res.ChallengeURL}}, nil
		}

		page := res.Page
		if !resolved {
			resolution := feed.ResolveResume(prev, ok, headPK(page))
			if resolution == feed.Unchanged {
				return outcome{}, nil // head unchanged → nothing new, stop
			}
			if resolution == feed.Grew {
				stopAtPK = prev.NewestPK
			}
			resolved = true
		}
		downloadPageMedia(ctx, d, page.Media)
		cur := advanceFeedCursor(newest, page, firstPage)
		if cerr := d.Store.CommitFeedPage(ctx, page, cur, r.Entity, r.Key, r.Kind); cerr != nil {
			return outcome{}, cerr
		}
		newest = cur
		firstPage = false

		if stopAtPK != "" && pageHasPK(page, stopAtPK) {
			return outcome{}, nil // Grew caught up to the known head
		}
		proceed, guardErr := guard.Next(page.NextMaxID)
		if !proceed {
			_ = guardErr
			return outcome{}, nil
		}
		maxID = page.NextMaxID
		if werr := d.Pacer.Wait(ctx); werr != nil {
			return outcome{}, werr
		}
	}
}

func archiveStories(ctx context.Context, d Deps) (outcome, int, int, error) {
	resp, err := d.Fetcher.Fetch(ctx, d.Bundle, ig.UserStoryRequest(d.DSUserID))
	if err != nil {
		return outcome{}, 0, 0, fmt.Errorf("%s stories transport failure: %s", d.Fetcher.Name(), session.Sanitize(err.Error()))
	}
	res := feed.ClassifyStory(resp, reliability.Floor{}, d.DSUserID)
	if res.Verdict.State == reliability.StateSchemaDrift {
		return outcome{}, 0, 0, nil // isolate: a drifted story page doesn't halt the run
	}
	if res.Verdict.State != reliability.StateOK {
		return outcome{terminal: &terminal{verdict: res.Verdict, challengeURL: res.ChallengeURL}}, 0, 0, nil
	}
	medias := make([]feed.Media, len(res.Set.Stories))
	for i := range res.Set.Stories {
		medias[i] = res.Set.Stories[i].Media
	}
	missing := downloadPageMedia(ctx, d, medias)
	for i := range res.Set.Stories {
		res.Set.Stories[i].Media = medias[i]
	}
	if err := d.Store.CommitStorySet(ctx, res.Set); err != nil {
		return outcome{}, 0, 0, err
	}
	return outcome{}, len(res.Set.Stories), missing, nil
}

func archiveCollections(ctx context.Context, d Deps, sum *Summary) (outcome, error) {
	guard := reliability.NewPageGuard(collectionListPages)
	maxID := ""
	var collections []feed.Collection
	for {
		resp, err := d.Fetcher.Fetch(ctx, d.Bundle, ig.CollectionsListRequest(maxID))
		if err != nil {
			return outcome{}, fmt.Errorf("%s collections transport failure: %s", d.Fetcher.Name(), session.Sanitize(err.Error()))
		}
		res := feed.ClassifyCollectionsList(resp, reliability.Floor{})
		if res.Verdict.State == reliability.StateSchemaDrift {
			sum.DriftedResources = append(sum.DriftedResources, "collections_list")
			return outcome{}, nil
		}
		if res.Verdict.State != reliability.StateOK {
			return outcome{terminal: &terminal{verdict: res.Verdict, challengeURL: res.ChallengeURL}}, nil
		}
		collections = append(collections, res.Page.Collections...)
		proceed, _ := guard.Next(res.Page.NextMaxID)
		if !proceed {
			break
		}
		maxID = res.Page.NextMaxID
		if err := d.Pacer.Wait(ctx); err != nil {
			return outcome{}, err
		}
	}
	sum.Collections = len(collections)
	for _, c := range collections {
		if err := d.Store.UpsertCollection(ctx, c); err != nil {
			return outcome{}, err
		}
		collID := c.ID
		oc, err := paginateFeed(ctx, d, FeedResource{
			Entity: store.FeedEntityCollection, Key: collID, Kind: store.FeedKindSaved, Label: "collection:" + collID,
			Build: func(maxID string) ig.Request { return ig.CollectionFeedRequest(collID, maxID) },
			Classify: func(rr ig.RawResponse, f reliability.Floor) feed.FeedResult {
				return feed.ClassifyCollectionFeed(rr, f, collID)
			},
		})
		if err != nil {
			return outcome{}, err
		}
		if oc.terminal != nil {
			return oc, nil
		}
		if oc.drift != nil {
			sum.DriftedResources = append(sum.DriftedResources, "collection:"+collID)
		}
	}
	return outcome{}, nil
}

func archiveLiked(ctx context.Context, d Deps, sum *Summary) (outcome, error) {
	resp, err := d.Fetcher.Fetch(ctx, d.Bundle, ig.LikedFeedRequest(""))
	if err != nil {
		return outcome{}, fmt.Errorf("%s liked transport failure: %s", d.Fetcher.Name(), session.Sanitize(err.Error()))
	}
	res := feed.ClassifyLiked(resp, reliability.Floor{})
	if res.Unavailable {
		sum.LikedUnavailable = true
		return outcome{}, nil // best-effort: not drift, not a halt
	}
	if res.Verdict.State == reliability.StateSchemaDrift {
		sum.DriftedResources = append(sum.DriftedResources, "liked")
		return outcome{}, nil
	}
	if res.Verdict.State != reliability.StateOK {
		return outcome{terminal: &terminal{verdict: res.Verdict, challengeURL: res.ChallengeURL}}, nil
	}
	// Liked was available (rare): paginate it like any other feed.
	oc, err := paginateFeed(ctx, d, FeedResource{
		Entity: store.FeedEntityLiked, Key: "", Kind: store.FeedKindLiked, Label: "liked",
		Build:    ig.LikedFeedRequest,
		Classify: feed.ClassifyLiked,
	})
	if err != nil {
		return outcome{}, err
	}
	if oc.drift != nil {
		sum.DriftedResources = append(sum.DriftedResources, "liked")
	}
	return oc, nil
}

// downloadPageMedia fetches bytes for every file of every media (and carousel
// child) in the page, folding the results back into the MediaFile structs in
// place before they are committed. Returns the count of missing (expired) files.
func downloadPageMedia(ctx context.Context, d Deps, medias []feed.Media) int {
	if d.NoMedia || d.Media == nil {
		return 0
	}
	var targets []media.Target
	var refs []*feed.MediaFile
	for mi := range medias {
		collectFiles(&medias[mi], &targets, &refs)
		for ci := range medias[mi].Carousel {
			collectFiles(&medias[mi].Carousel[ci], &targets, &refs)
		}
	}
	if len(targets) == 0 {
		return 0
	}
	results := d.Media.Download(ctx, targets)
	missing := 0
	for i, res := range results {
		switch {
		case res.Err != nil:
			continue // leave undownloaded; not fatal
		case res.Missing:
			refs[i].Missing = true
			missing++
		default:
			refs[i].Hash = res.Hash
			refs[i].LocalPath = res.LocalPath
			refs[i].Downloaded = true
		}
	}
	return missing
}

func collectFiles(m *feed.Media, targets *[]media.Target, refs *[]*feed.MediaFile) {
	for fi := range m.Files {
		f := &m.Files[fi]
		if f.URL == "" {
			continue
		}
		*targets = append(*targets, media.Target{URL: f.URL, Kind: f.Kind, Width: f.Width, Height: f.Height})
		*refs = append(*refs, f)
	}
}

func advanceFeedCursor(prev feed.FeedCursor, page *feed.FeedPage, firstPage bool) feed.FeedCursor {
	cur := prev
	cur.SchemaVersion = feed.FeedCursorSchemaVersion
	cur.MaxID = page.NextMaxID
	cur.Backfilled = !page.MoreAvailable
	if len(page.Media) == 0 {
		cur.ConsecutiveEmpty = prev.ConsecutiveEmpty + 1
	} else {
		cur.ConsecutiveEmpty = 0
	}
	if firstPage && len(page.Media) > 0 {
		cur.NewestPK = page.Media[0].PK
		cur.NewestTakenAtUS = page.Media[0].TakenAtUS
	}
	return cur
}

func headPK(page *feed.FeedPage) string {
	if page == nil || len(page.Media) == 0 {
		return ""
	}
	return page.Media[0].PK
}

func pageHasPK(page *feed.FeedPage, pk string) bool {
	if pk == "" {
		return false
	}
	for i := range page.Media {
		if page.Media[i].PK == pk {
			return true
		}
	}
	return false
}

func terminalSummary(sum Summary, t *terminal) Summary {
	sum.State = t.verdict.State.String()
	sum.Reason = t.verdict.Reason
	sum.ChallengeURL = t.challengeURL
	return sum
}
