# Recon: steipete/openclaw crawler suite — mined for instacrawl (Instagram export-first design)

Date: 2026-07-08
Repos cloned to `~/Developer/reference-repos/{slacrawl,notcrawl,birdclaw,wacli}`.

**Provenance correction**: three of the four clone cleanly from `steipete/<name>` or
`openclaw/<name>`. One does not fit the "steipete repo" framing at all:

| repo | actual clone URL | true author (per README credits) | notes |
|---|---|---|---|
| slacrawl | `github.com/steipete/slacrawl` (mirror, no 404) | **Vincent Koc** (`github.com/vincentkoc`) — README footer "Built by Vincent Koc", Homebrew tap `vincentkoc/tap`, Go module `github.com/vincentkoc/slacrawl` | steipete/slacrawl is a fork/mirror, not the origin. Treat slacrawl as external-pattern reference only, not a steipete design artifact. |
| notcrawl | `github.com/steipete/notcrawl` **404s** | **openclaw org** — correct clone is `github.com/openclaw/notcrawl` (found via `gh search repos notcrawl`) | Same org as wacrawl/wacli/gitcrawl. |
| birdclaw | `github.com/steipete/birdclaw` | steipete (README: "Built by @steipete") | The priority target — full local-first Twitter workspace. |
| wacli | `github.com/steipete/wacli` | Created by @steipete, currently maintained by @dinakars777 | Confirmed separate from wacrawl (see §3). |

---

## 1. birdclaw — Twitter/X archive-ZIP ingestion pipeline (priority)

Source root: `src/lib/archive*.ts`, `src/lib/archive/*.ts`, `src/lib/streaming-ingestion.ts`.

### 1.1 Entry point and shape

`importArchive(archivePath, options)` → `importArchiveEffect` (Effect-based, wrapped
in a `Promise` at the outer boundary) in `src/lib/archive-import.ts`. Single function
orchestrates the whole pipeline as a linear `Effect.gen`:

1. `listArchiveEntriesEffect` — list ZIP entries (see 1.2)
2. `selectedSlices(options)` — resolve which of the 7 `ARCHIVE_IMPORT_SLICES`
   (`tweets`, `likes`, `bookmarks`, `directMessages`, `profiles`, `followers`,
   `following`) are in scope. `null` selection = import everything.
3. `selectArchiveSliceEntries` — regex-match ZIP entry paths to slice categories
   (see 1.3).
4. Read `data/account.js` (required — throws if missing) and `data/profile.js`
   (optional) to build an `ArchiveAccountPayload`.
5. Parse each slice into an in-memory `ArchiveImportPlan` (mutable accumulator:
   `tweets[]`, `profiles: Map`, `conversations: Map`, `dmMessages[]`,
   `followers[]/following[]`, plus a cross-slice `mentionDirectory: Map` used to
   backfill display names for users only ever seen as a `@mention` or DM sender).
6. Extract bundled media files into a local originals cache (see 1.4).
7. Reconcile follow-graph profiles once all follower/following slices are parsed.
8. `applyArchiveImportPlanEffect` — a single SQLite write transaction that upserts
   everything (see 1.5).
9. Returns `ImportedArchiveSummary` with per-kind counts.

### 1.2 ZIP access — no zip library, shells out to system `unzip`

`src/lib/archive/reader.ts` never fully extracts the archive to disk. Every read is a
subprocess call to the system `unzip` binary:

- `listArchiveEntriesEffect`: `unzip -Z1 <archive>` → newline list of entry paths.
- `listArchiveEntryDetailsEffect`: `unzip -Z -l <archive>` → parses the `ls`-style
  listing (splits on whitespace, validates 10+ columns and a leading `-`/`d`) to get
  `{path, size}` for every entry — used purely to size-check media files for
  incremental skip (see 1.4).
- `readArchiveEntryEffect(path, entry)`: `unzip -p <archive> <entry>` → stdout is the
  raw file content (used for the small `data/*.js` files: account, profile,
  followers, following).
- `processArchiveEntryRecordsEffect` / `streamArchiveArrayRecords`: for the *big*
  slices (tweets, DMs, likes, bookmarks — files that can be tens of MB of one giant
  JS array), spawns `unzip -p` directly via `child_process.spawn` (not through the
  buffered `runSubprocessEffect` wrapper) and pipes `child.stdout` into a hand-rolled
  streaming JSON-array parser (`streamAssignedJsonArray` in
  `src/lib/streaming-ingestion.ts`) instead of buffering the whole file and calling
  `JSON.parse`.

This is the load-bearing decision for a large Instagram export: never buffer an
entire multi-hundred-MB `.js`/`.json` slice in memory. `unzip -p` is a streaming pipe;
`streamAssignedJsonArray` is a tiny bracket/quote/escape-tracking state machine that
yields one array element (one tweet/DM-conversation/like) at a time as soon as its
closing `}`/`]` is seen at depth 0. It does not require the array to close before
yielding — genuinely incremental.

`ingestSourcesInBatchesEffect` / `ingestStreamInBatchesEffect` (same file) then batch
those yielded records (default `batchSize=250`) and call a `processBatch` callback —
this is what backpressures the per-record `processRecord` calls in
`processArchiveEntryRecordsEffect`, and is the hook point for a future resumable/
checkpointed import (`IngestionCheckpoint.processed`, `resumeAfter` param already
exist but nothing in birdclaw's archive pipeline currently persists/passes
`resumeAfter` — it's plumbing for streaming batches within one entry file, not
cross-run resume of a whole import).

### 1.3 Twitter's `window.YTD.foo.part0 = [...]` format → JSON

Twitter/X archive `data/*.js` files are not valid JSON — they're a JS assignment
(`window.YTD.tweets.part0 = [ ... ]`). `extractArchiveJson` in
`src/lib/archive/parsing.ts` handles this with one regex:

```ts
const ARCHIVE_JSON_PAYLOAD = /=\s*(\[[\s\S]*\]|\{[\s\S]*\})/s;
```

— finds the first `= <value>` and takes everything after the `=` up to end of
string, matching either a `[...]` or `{...}` payload, then `JSON.parse`s it. This is
used only for the *small* full-buffer reads (account.js, profile.js,
follower/following files); the *big* streamed slices instead strip only the leading
`[` character in `streamAssignedJsonArray` (`started` flag) and never care about the
trailing `window.YTD... =` prefix beyond that.

Entry-path → slice mapping is centralized in `src/lib/archive/slices.ts` as regexes
against normalized (`\` → `/`) entry paths, e.g.:
- tweets: `(?:^|/)data/(?:tweets|community-tweet)(?:-part\d+)?\.js$`
- note-tweets (long-form "Twitter Notes"): `data/note-tweet(?:-part\d+)?\.js$`
- likes/bookmarks/DMs/followers/following: analogous, all tolerating a
  `-part\d+` suffix for archives Twitter split into multiple files.

Each slice has a dedicated parser module: `tweet-slices.ts` (tweets +
note-tweets — note-tweets get synthesized as home-timeline tweets),
`dm-slice.ts` (direct messages, most complex — see 1.6), `collection-slice.ts`
(likes/bookmarks), `follow-slice.ts` (followers/following).

### 1.4 Media extraction — selective, resumable-by-size, atomic writes

`src/lib/archive/media.ts`: media lives in fixed archive directories
(`tweets_media`, `direct_messages_media`, `community_tweet_media`,
`deleted_tweets_media`, `profile_media`, `moments_tweets_media`,
`direct_messages_group_media`), mapped 1:1 to an `ArchiveMediaKind` enum.
`selectedArchiveMediaKinds(selection)` derives which media kinds to extract from
which *content* slices are selected (e.g. `directMessages` slice pulls in both
`dms` and `dmGroup` media kinds) — media extraction piggybacks on the same
slice-selection mechanism rather than being its own toggle.

For each matching entry: destination path is
`~/.birdclaw/media/originals/archive/<kind>/<ownerId>/<filename>`, where
`ownerId` is parsed off the filename itself (`<id>-<restofname>`, split on first
`-`). **Incremental import**: before copying, if `destinationPath` already exists
*and* its `statSync(...).size === entry.size` (size from the earlier
`unzip -Z -l` listing), the entry is skipped entirely — this is the whole
incremental-reimport story for media: no manifest/hash file, just a cheap
existence+size check against the ZIP's own listed size. Copy itself is
`spawn("unzip", ["-p", archivePath, entryPath])` piped through
`node:stream/promises pipeline` into a `<dest>.<pid>.<uuid>.tmp` file, then
`renameSync` to the final path (atomic on the same filesystem) — errors
(`kill()` + `unlinkSync` the temp file) can't leave a half-written file at the
real path.

Media *metadata* (not bytes) is captured earlier during tweet parsing:
`extractTweetMedia` in `parsing.ts` pulls `entities.media` /
`extended_entities.media`, normalizes `type` (`photo→image`,
`video`/`animated_gif→video`/`gif`), picks the largest `sizes.large` dimensions,
and for videos extracts every `video_info.variants[]` with
`content_type === "video/mp4"`, sorted by bitrate descending (highest-bitrate
first) — stored as JSON on the tweet row (`media_json`). A separate `media
fetch` CLI command (documented in README, not read in depth here) later
reconciles that metadata against bytes already pulled from the archive
(`reused_from_archive` counter) before falling back to a live CDN download —
i.e. archive-extracted bytes are preferred over re-fetching from
`pbs.twimg.com`/`video.twimg.com`.

### 1.5 Apply phase — one write transaction, `ON CONFLICT` merge semantics

`src/lib/archive/apply.ts` is entirely SQL (`better-sqlite3`-style prepared
statements) wrapped in a single `databaseWriteEffect` transaction. Every insert
is `INSERT ... ON CONFLICT(...) DO UPDATE SET ...` with deliberately asymmetric
merge rules per column, e.g. for tweets:
- `text`: keep existing non-empty text unless the incoming param flag says
  otherwise, else prefer incoming if non-empty, else keep existing — protects
  against overwriting fuller text (e.g. previously live-hydrated) with a
  archive stub.
- `is_replied` / `like_count` / `media_count`: `max(existing, incoming)` —
  monotonic counters never regress.
- `entities_json` / `media_json`: only overwritten if the incoming value is
  non-trivial (`<> '{}'` / `<> '[]'`).
- `quoted_tweet_id`: `coalesce(incoming, existing)`.

This "never let a re-import silently regress previously-known-good data" pattern
recurs for profiles, follow edges, and DM conversations. **Selective slice
reimport** (`applyArchiveImportPlanEffect`'s `selection` param) is implemented
as scoped `DELETE` statements before the writes — e.g. re-importing just
`likes` runs `clearSelectedLikes` (deletes only
`tweet_collections WHERE kind='likes' AND source IN ('archive','legacy')`) then
rewrites, and a follow-up `deleteOrphanTweets` GC pass removes tweets no longer
referenced by any collection/edge/reply/quote — so a narrower slice re-import
can prune stale rows without a full wipe. Full (unselected) imports instead call
`repository.clearArchiveImport()` + `clearMentionSyncState()` up front — a
clean-slate reset.

Follow graph handling (`importFollowRows`) is snapshot-based: each reimport
computes a stable `snapshot_id` (`follow_snapshot_archive_<account>_<direction>`),
diffs the ordered member list against the previous snapshot
(string-join comparison of `index:profileId:externalUserId` lines) to decide
if per-member rows need rewriting, and emits `follow_event` rows
(`started`/`ended`) for edges that flipped `current` state between imports —
this is how birdclaw derives "who unfollowed you" over time from repeated
archive imports, not just a point-in-time snapshot.

### 1.6 Profile identity reconciliation — the hardest part of the pipeline

`src/lib/archive/profile-reconciler.ts`. Archive data only ever gives you a
numeric `accountId`/`userId` for anyone who isn't you — no handle, no display
name — for followers, following, and DM participants. Three profile-quality
"tiers" are ranked (`archive_follow_stub` < `archive_dm_stub` <
`archive_mention_inferred` < `live_or_hydrated`) via `classifyProfile`, and
`merge()` only overwrites an existing profile row if the incoming data is
*at least as good* a tier — so a profile already hydrated from live API data is
never clobbered by an anonymous archive stub, but a bare numeric-ID stub *is*
upgraded if the same user later turns up with a resolvable handle (via
`mentionDirectory` — built by scanning every tweet's `user_mentions` and
`in_reply_to_user_id`/`in_reply_to_screen_name` pairs across the whole tweets
slice before DMs are parsed, so DM participants who were never explicitly
named in the DM JSON can still get a real handle if they were ever @mentioned
or replied-to elsewhere in the same archive).
Handle collisions across re-imports are resolved via `uniqueHandle`
(`<handle>_archive`, `<handle>_archive_2`, ...) keyed off an
`existingProfilesByHandle` index built once per import from current DB state.

### 1.7 Archive auto-discovery (macOS only)

`src/lib/archive-finder.ts`: `findArchivesEffect()` scans `~/Downloads` by
filename glob (`twitter-*.zip`, `x-*.zip`, `*archive*.zip`) plus three
`mdfind` (Spotlight) queries, dedupes by path, sorts by mtime, returns
`{path, name, size, sizeFormatted, modifiedTime, dateFormatted}`. 5-minute
in-memory cache + in-flight-promise dedupe (`archiveDiscoveryCache`,
`archiveDiscoveryInFlight`) so concurrent UI calls collapse to one filesystem
scan. Hard `process.platform !== "darwin"` early return — this whole module is
a no-op off macOS. **Directly transferable to Instagram**: Instagram's export
ZIP filename convention (`instagram-<username>-<date>-<hash>.zip` observed in
practice) would need its own glob/Spotlight query set, but the caching/dedup
shell is copy-paste reusable.

### 1.8 What's directly reusable for instacrawl (Instagram export ZIP)

- The `unzip -p`/`-Z1`/`-Z -l` subprocess-only approach — no npm zip
  dependency, no full in-memory extraction. Instagram's JSON export files
  (unlike Twitter's) are already valid JSON, so `extractArchiveJson`'s
  regex-strip step is *not* needed — but `streamAssignedJsonArray`'s
  depth/string/escape-tracking streaming array parser is still valuable for
  Instagram's large `messages/inbox/<thread>/message_1.json`,
  `posts_1.json`, `stories.json` files, which are big and array-shaped.
- The slice/selection/regex-per-content-type architecture
  (`ARCHIVE_IMPORT_SLICES`, `selectArchiveSliceEntries`) maps cleanly onto
  Instagram's own export categories (posts, stories, messages, likes/saved,
  followers/following, comments, profile).
- The media-extraction size-check-skip + atomic-temp-then-rename pattern is
  directly portable; Instagram exports bundle media alongside JSON the same
  way Twitter does (`your_instagram_activity/media/...`).
- The profile-quality-tier reconciliation pattern (never downgrade a
  richer profile with a stub) is the right shape for Instagram's own
  anonymous-numeric-ID-until-resolved participants in DM/thread exports.
- The `ON CONFLICT` monotonic-merge SQL pattern for avoiding regression on
  reimport, plus scoped-slice-delete-then-rewrite for selective reimport, is a
  strong template for an Instagram equivalent.

---

## 2. slacrawl / notcrawl — API-token pagination + rate-limit patterns

**Caveat**: slacrawl's real author is Vincent Koc, not steipete (see provenance
table above) — evaluate it purely as an external pattern reference, same as any
non-steipete OSS project, not as "house style."

### 2.1 slacrawl (`internal/slackapi/api.go`, Go, wraps `github.com/slack-go/slack`)

- **Pagination**: standard cursor loop — `cursor := ""` then
  `for { page, nextCursor, err := c.getConversations(...); ...; if nextCursor
  == "" { break }; cursor = nextCursor }`. Same shape repeated for channels,
  conversation history, and (via `ResponseMetaData.NextCursor`) thread replies.
  A local `conversationHistoryPage{Messages, NextCursor}` wrapper type
  normalizes the slack-go SDK's differently-shaped paginated responses to one
  internal shape.
- **Rate-limit handling**: slack-go's SDK already surfaces
  `*slack.RateLimitedError{RetryAfter}` on HTTP 429; slacrawl's own HTTP
  fallback path additionally parses the raw `Retry-After` header itself
  (`resp.StatusCode == http.StatusTooManyRequests`) and constructs that same
  SDK error type by hand for endpoints hit outside the SDK's own retry
  wrapper. A generic retry loop (`for attempt := 1; attempt <= attempts-1;
  attempt++ { ...; if !errors.As(err, &rateLimited) || attempt ==
  attempts-1 { return err }; sleeper(ctx, rateLimited.RetryAfter) }`) is
  injected via an interface field (`sleep func(context.Context,
  time.Duration) error`) — swappable for a no-op in tests (avoids real sleeps
  in the 18-file test suite).
- **Workspace enumeration**: multi-workspace via `[[workspaces]]` TOML config
  array, each with its own token set; sync cursors keyed by
  `(workspaceID, channelID)` (`ChannelSyncCursors(ctx, workspaceID)` →
  `latestByChannel map[string]string`) — this is how `--latest-only`
  incremental sync knows where each channel left off, independent of full
  history backfill (`--full`).
- **Dual-mode ingestion**: same tool supports API-token sync *and*
  reading Slack Desktop's local cache directly (macOS-only "desktop mode") —
  same dual-source idea birdclaw uses for archive-vs-live, and notcrawl uses
  for desktop-vs-API-vs-mcp.

### 2.2 notcrawl (`internal/notionapi/api.go`, Go, official Notion API)

- **Pagination**: identical cursor-loop shape at 5 separate call sites (pages,
  blocks, databases/data-sources, comments, users) — `start_cursor` sent
  either as a query param (GET-with-cursor endpoints) or a JSON body field
  (POST/query endpoints), response read via `next_cursor`; loop exits when
  `next_cursor == ""`.
- **Rate-limit / retry**: more thorough than slacrawl's — a single
  `doRequest` wrapper (used by all endpoints) runs a `for attempt := 1;
  attempt <= maxAPIAttempts` loop; on any non-2xx response it builds a
  structured `notionAPIError{StatusCode, Code, Message, RetryAfter,
  Retryable}` by parsing both the `Retry-After` header (seconds-format or
  HTTP-date format, `http.ParseTime` fallback) *and* the JSON error body's own
  `retry_after`/`retryable` fields (Notion's API can flag an error retryable
  even without a 429). `shouldRetry` treats 429, explicit-`Retryable`, 502,
  503, 504, *and 524* (Cloudflare upstream timeout — a specific
  battle-tested addition, comment: "returned by Notion for transient
  upstream stalls") as retry-worthy. `waitBeforeRetry` respects `ctx.Done()`
  during the sleep so a cancelled sync doesn't block on a long Retry-After.
- **Three-source model**: `desktop` (read-only local cache), `api` (official
  token, rate-limit-aware), `notion-mcp` (repair pass through a *pre-authed
  Codex/ChatGPT connector* rather than a raw token) — the third source is
  notable as a "repair holes left by the other two sources" pass, explicitly
  scoped (`--page`/`--query`, or defaults to pages with missing bodies/blocks)
  rather than a full crawl.
- **Shared library**: both slacrawl (`crawlkit v0.7.0`) and notcrawl
  (`crawlkit v0.13.1`, newer) depend on `github.com/openclaw/crawlkit` — a
  shared Go module providing `control` (the "crawlkit control metadata"
  surfaced via each tool's `metadata` command), `scheduler`, `snapshot`,
  `mirror`, `backup`, `remote` (contract for git-share subscribe/publish),
  `embed`, `vector`, and a shared `tui` package. This is the actual reusable
  cross-crawler infrastructure in this family — version drift between
  consumers (0.7.0 vs 0.13.1) suggests it's actively evolving and each CLI
  pins independently rather than always tracking latest.

---

## 3. wacli — live protocol client, deliberately NOT crawlkit-based

`wacli` (`github.com/openclaw/wacli`, created by @steipete, now maintained by
@dinakars777) is architecturally a different animal from wacrawl (the tool
described in the user's own memory/CLAUDE.md as the WhatsApp *Desktop-DB*
importer):

- **wacli** is a live WhatsApp Web protocol client built on
  `github.com/tulir/whatsmeow` — QR-pairs as a *linked device*, holds a live
  WebSocket session, and both *sends* (text/media/stickers/reactions/status)
  and *syncs* opportunistically-pushed messages into local SQLite
  (`go-sqlite3` + FTS5, cgo required — note the Homebrew caveat
  `CGO_ENABLED=0` binary breakage). Package layout: `internal/wa/` holds the
  whatsmeow session/client/media/backfill/appstate/lid-migration glue (test
  files include `fake_wa_test.go` — a fake protocol layer for testing without
  a live WhatsApp connection); `internal/app/` holds sync orchestration;
  `internal/store/` (with generated `sqlc`) is the SQLite layer.
- **No `crawlkit` dependency at all** (`go.mod` has no `openclaw/crawlkit`
  line) — confirms this is deliberately outside the "mirror an
  already-existing local archive into SQLite" crawlkit family that
  slacrawl/notcrawl/wacrawl/gitcrawl/telecrawl/discrawl belong to. wacli is a
  *transport*, not a *crawler*; wacrawl (per user's own memory notes) is the
  crawlkit-style tool that reads WhatsApp Desktop's already-decrypted local
  Group Container DB read-only, no live protocol, no QR, no whatsmeow.
  `docs/integrations.md`/README never mention wacrawl — the two tools don't
  share code, only a maintainer and a naming convention.
- **History backfill** is its own explicit on-demand-request-to-primary-device
  flow (`wacli history backfill --chat <jid> --requests N --count M`, capped
  `count<=500`, `requests<=100`), anchored on the oldest locally-stored
  message per chat — this is *not* pagination against a REST API, it's
  requesting more of WhatsApp's own encrypted history sync from your phone,
  a fundamentally different "we can't just page harder, we have to ask the
  paired device" incremental-import shape than any of the other three tools.
- **Safety rails absent from the crawlkit family**: `--read-only` global flag,
  owner-only DB file permissions, store locks with owner reporting,
  `WACLI_SYNC_MAX_MESSAGES`/`WACLI_SYNC_MAX_DB_SIZE` stop-conditions — because
  wacli can *write* to a live account (send messages), it has a write-safety
  posture none of the read-only mirror tools need.

---

## 4. Cross-cutting observations

### 4.1 Naming / UX conventions (the `*crawl(ctl)` family)
- Every crawler: `init` → `doctor` (diagnostics: config/token/db/desktop-source
  checks) → `status`/`report` → `sync [--source ...] [--full|--latest-only]` →
  content-specific query commands → `sql <query>` (raw read-only SQL escape
  hatch) — this exact command skeleton appears near-verbatim in slacrawl and
  notcrawl READMEs, and matches the shape the user's own memory describes for
  wacrawl/telecrawl/discrawl/gitcrawl.
  A `<toolname>crawl(ctl)` naming pattern, `~/.<toolname>/` default storage
  root, SQLite + FTS5 as the canonical local store, and a `--json` flag for
  scriptability recur everywhere including wacli (protocol client, not a
  crawlkit consumer, but same UX conventions).
- Update-check convention: notcrawl's `check-update` + cached daily
  interactive notice + `NOTCRAWL_NO_UPDATE_CHECK`/`CRAWLKIT_NO_UPDATE_CHECK`
  dual env-var opt-out (tool-specific alias plus a shared crawlkit-wide one)
  is a nice small pattern for a future instacrawl release-check feature.
- Git-backed sharing: notcrawl (compressed JSONL git-share snapshots),
  slacrawl (git-backed archive publishing/subscription with read-time
  auto-refresh), birdclaw (Git-friendly text backups, yearly tweet shards,
  per-conversation DM shards) all converge on "the archive is portable via a
  plain git repo of normalized text," independently arrived at three times —
  a strong signal this is the right default for instacrawl too if
  cross-machine/no-lock-in portability matters (echoes the gbrain
  "no lock-in, markdown/git" posture already in the user's memory).

### 4.2 crawlkit usage depth
Only the two Go crawlers examined here (slacrawl, notcrawl) actually depend on
`github.com/openclaw/crawlkit` as a library (v0.7.0 and v0.13.1 respectively);
per user's memory, wacrawl/gitcrawl/telecrawl/discrawl are presumably also
consumers (not independently verified in this pass — not cloned). crawlkit
provides: `control` (metadata/introspection contract), `scheduler`
(recurring-sync discovery/config), `snapshot`/`mirror`/`backup` (the git-share
mechanics), `remote` (a `contract.go` defining the publish/subscribe shape),
`embed` (a vector-embedding provider abstraction), `vector` (a "turbovec"
implementation — worth a closer look if instacrawl ever wants local embeddings
for semantic search over Instagram captions/DMs), and a shared `tui` package.
birdclaw and wacli are **not** part of this shared-library family — both are
TypeScript/Effect and cgo-Go respectively, and each reimplements its own
CLI/sync/store layer from scratch. If instacrawl is Go, `crawlkit` is worth
evaluating directly as a dependency rather than reinventing scheduler/
snapshot/backup/remote-contract pieces; if instacrawl is TypeScript (to mirror
birdclaw, the closer architectural template for "official archive export"
ingestion), there's no equivalent shared library to reach for — birdclaw's
`src/lib/archive/*` module split is itself the template to copy structurally.

### 4.3 Test strategy comparison
| repo | test file count | shape |
|---|---|---|
| birdclaw | 110 | Vitest unit tests (`src/**/*.test.ts`, jsdom env, 85%/85%/80%/85% line/fn/branch/stmt coverage gate) + separate Playwright e2e suite (`playwright/`, single chromium worker, isolated `.playwright-home` profile dir) — the exported `__test__` object in `archive-import.ts` deliberately re-exposes internal pure functions (`parseTwitterDate`, `extractTweetMedia`, ...) for direct unit testing without going through the full Effect pipeline. |
| wacli | 86 | Go `_test.go`, includes a hand-written **fake WhatsApp protocol layer** (`fake_wa_test.go`) so sync/backfill/media-retry logic can be tested without a live whatsmeow connection — plus dedicated regression tests (`locking_regression_test.go`, `lid_migration` implied). |
| slacrawl | 18 | Go `_test.go`, concentrated in `internal/slackapi` (API client, DM handling) and `internal/store`; rate-limit retry tested via the injectable `sleep` field standing in for real time. |
| notcrawl | 11 | Go `_test.go`, lightest of the four — API client tests only skim-verified in this pass. |

Takeaway: birdclaw's test investment is by far the heaviest (proportionate to
being the most complex/most actively developed repo here), and its pattern of
exporting pure-function internals for direct unit testing around an
Effect-based orchestration layer is a good template if instacrawl also adopts
Effect or a similar typed-error-effect pattern for its own ingestion pipeline.
