# Installed steipete-family crawlers — recon for instacrawl

Sources: all four tools are Homebrew formulas in tap `openclaw/tap`
(`/opt/homebrew/Library/Taps/openclaw/homebrew-tap/Formula/{gitcrawl,telecrawl,wacrawl,discrawl}.rb`),
homepages at `github.com/openclaw/*` (not `steipete/tap` — that assumption was wrong).
All MIT-licensed, release-binary Homebrew installs (gitcrawl formula also supports `head` via
`go build ./cmd/gitcrawl`). Cloned into `~/Developer/reference-repos/{gitcrawl,telecrawl,wacrawl,discrawl}`.
Live DBs (`~/.telecrawl/telecrawl.db`, `~/.wacrawl/wacrawl.db`) inspected read-only via
`sqlite3 "file:<path>?mode=ro&immutable=1" ".schema"` (plain `-readonly` failed with
"unable to open database file" — the URI form is the reliable workaround). No writes, no row-level
SELECTs were run against either DB.

All four import `github.com/openclaw/crawlkit` (v0.13.1–v0.13.3 depending on the tool) — a shared
internal Go library. Confirmed shared packages actually used in source: `crawlkit/cache` (SQLite
snapshot-for-read-consistency helpers: `SnapshotSQLite`, `SQLiteModifiedAfter`) and, per go.mod,
an `internal/backup` (age-encrypted git backup) pattern replicated across telecrawl/wacrawl. Each
tool otherwise owns 90%+ bespoke code: its own sqlc-style `storedb`/`store` package, its own CLI
(cobra-style `internal/cli`), and — critically — its own data-source integration layer. crawlkit is
a utility toolbox (SQLite snapshotting, encrypted backup, DSN helpers), not a scraping/auth engine.

---

## 1. gitcrawl

**Data-source posture: official API, delegated auth.**
Uses the real GitHub REST/GraphQL API (no scraping). Zero bespoke OAuth: `internal/cli/github_token.go`
`resolveGitHubToken()` checks config/env first, then shells out to the user's already-authenticated
`gh` CLI (`gh auth token`, 3s timeout) via a resolved "real gh" binary path. `internal/cli/gh_path.go`
guards against gitcrawl recursively invoking itself when installed as a `gh`-compatible shim, using
SHA-256 content comparison of candidate binaries (`sameExecutableContents`). No 2FA handling of its
own — it rides whatever session `gh` already has (browser device-code OAuth done once by the user).

**Sync model:** cursor/checkpoint-based, resumable. `internal/store/schema.go` has `repo_sync_state`
(`last_full_open_scan_started_at`, `last_overlapping_open_scan_completed_at`,
`last_non_overlapping_scan_completed_at`, `last_open_close_reconciled_at`) plus a `sync_runs` table
(status/started_at/finished_at/stats_json/error_text) recording every run for audit/resume. Content
is de-duplicated via `thread_revisions` keyed on a `content_hash` (title+body+labels hash), so re-pulls
of unchanged issues/PRs are cheap no-ops.

**Schema:** `repositories`, `threads` (issues+PRs unified, `kind` check constraint), `comments`,
`pull_request_details/files/commits/checks/review_threads`, `github_workflow_runs`, plus an entire
second subsystem for semantic clustering: `documents`/`documents_fts` (FTS5, external-content on
`documents`), `code_snapshots`/`code_documents`/`code_documents_fts` (FTS5 over repo source),
`document_embeddings`/`thread_vectors` (OpenAI embeddings, JSON-stored vectors), `thread_fingerprints`
(simhash64 + token hashes for near-dup detection), and a full `clusters`/`cluster_groups`/
`cluster_memberships`/`cluster_events` graph for issue/PR deduplication. Media: none (text/code only,
diffs stored as blobs in a generic `blobs` table keyed by sha256, dedup'd across the whole DB).

**Rate limiting:** `internal/cli/gh_rate_limit.go` — persists a shared, cross-process rate-limit state
file per host+token-hash (`ghSharedRateLimitState`) recording remaining/limit/reset from every
`gh api` call's response headers or an explicit `gh api rate_limit` probe; other gitcrawl invocations
read this shared state and will serve stale cache rather than burn more quota when remaining drops
below a threshold (`GITCRAWL_GH_RATE_LIMIT_LOW_REMAINING`, default 250) and the state isn't too old
(`GITCRAWL_GH_RATE_LIMIT_MAX_AGE`, default 30m).

**crawlkit vs bespoke:** almost entirely bespoke — its own `internal/github` client wrapper around
`gh`, its own clustering/embedding pipeline, its own schema. No official GitHub Go SDK; auth and API
calls both go through the `gh` CLI subprocess.

---

## 2. telecrawl

**Data-source posture: local-app-state reading + session-hijack live calls (no fresh login).**
Two layers:
1. **Local decrypt (primary, offline).** Telegram Desktop stores chat history in an encrypted
   "Postbox" SQLite DB. `internal/telegramdesktop/postbox/key.go` reimplements Telegram Desktop's own
   local key-file format: reads the app's `tdata` "tempkey" file, AES-256-CBC decrypts it with a key/IV
   derived from SHA-512 of the local passcode (default: no passcode, tries `"no-matter-key"` and empty
   string — `DefaultPasscodes`), verifies via a murmur3-32 checksum, and recovers a 48-byte
   `dbKeyAndSalt`. That key feeds `postbox/sqlcipher.go`'s from-scratch **SQLCipher v4 decryptor**:
   page-size validated against the plaintext SQLite header on page 1, PBKDF2 "fast KDF" HMAC-key
   derivation (2 iterations, HMAC salt XORed with `0x3a`), then per-page AES-256-CBC decrypt +
   HMAC-SHA512 verification (80-byte reserved region: 16-byte IV + 64-byte MAC). All of this happens
   against the *already-authenticated* desktop app's files — no login, no OAuth, no 2FA prompt, ever.
2. **Live MTProto backfill (secondary, for media only).** `postbox_remote.go` extracts the app's own
   `NativeSession{AuthKey[256], DCID, Host, Port}` straight from the decrypted Postbox and replays it
   via `gotd/td` (`session.StorageMemory` seeded directly with the harvested auth key — literally
   session hijacking, not a fresh MTProto handshake). It authenticates using **Telegram Desktop macOS's
   own embedded public api_id/api_hash** (`telegramMacAPIID=9`, hash hardcoded, `gitleaks:allow`
   because it's Telegram's own non-secret client credential, not the user's), spoofs
   `DeviceConfig{DeviceModel:"Mac", AppVersion:"11.15", ...}` to look like genuine Desktop traffic, and
   issues only read-only RPCs (`ChannelsGetMessages`/`MessagesGetMessages`/`MessagesGetHistory`) to
   resolve media that isn't cached locally.

**Sync model:** full local-DB decrypt-and-diff each run (no incremental cursor over the encrypted
Postbox itself — SQLCipher pages are decrypted wholesale into a temp SQLite file each time); the live
media-backfill path is incremental by construction since it only targets messages whose `MediaPath`
is empty (`postboxRemoteMediaCandidateIndexes`).

**Schema (from live `~/.telecrawl/telecrawl.db`):** account/chat/message tables mirroring Telegram's
own model, with dedicated media-reference columns (local path + resolved-remote fallback), FTS5 over
message text, and `sync_state`-style bookkeeping. (Full `.schema` captured during this session; not
reproduced verbatim here for length — see prior schema dump if needed.)

**Rate limiting / anti-ban:** `flood_wait.go` — a `gotd/td` middleware (`telegramFloodWaitPolicy`)
that intercepts `tgerr.AsFloodWait`, retries up to 3 times, caps any single wait at 5 minutes (aborts
past that), adds a 1s safety buffer on top of Telegram's own requested delay, and reports progress.
This is a direct, spec-compliant implementation of Telegram's own backoff contract — not a custom
heuristic — because it's issuing traffic *as* the real Telegram Desktop client over its own live
session.

**crawlkit vs bespoke:** the SQLCipher decryptor, MTProto session-hijack layer, and flood-wait policy
are all fully bespoke (telecrawl-specific, security-sensitive code). crawlkit contributes generic
SQLite/backup helpers only.

---

## 3. wacrawl

**Data-source posture: pure local app-state reading, zero network calls.**
`internal/whatsappdb/desktop.go` reads WhatsApp Desktop's own macOS container directly:
`~/Library/Group Containers/group.net.whatsapp.WhatsApp.shared/{ChatStorage.sqlite,ContactsV2.sqlite}`
(CoreData-backed, tables `ZWACHATSESSION`, `ZWAMESSAGE`, `ZWAMEDIAITEM`, `ZWAGROUPINFO`,
`ZWAGROUPMEMBER`, `ZWAADDRESSBOOKCONTACT`, `ZWAPROFILEPUSHNAME`). No MTProto/Bot-API equivalent exists
for WhatsApp Desktop's local store — these are plain, unencrypted-at-this-layer SQLite files once the
Desktop app itself is unlocked/logged in, so there is no auth/2FA handling in wacrawl at all: it just
needs filesystem read access to files the already-running WhatsApp Desktop app owns. This is the
**most "app already did all the hard auth work" posture of the four** — no session-hijacking, no
key derivation, no protocol replay, just a straight `database/sql` read via `modernc.org/sqlite` in
readonly/query-only PRAGMA mode.

**Sync model:** snapshot-then-full-reimport, gated by change detection — *not* true incremental. Every
sync (`internal/cli/sync.go` → `whatsappdb.Import`) copies the live DB files into a temp dir via
crawlkit's `cache.SnapshotSQLite` (read consistency without locking the live app), fully re-extracts
all contacts/chats/groups/messages, and calls `store.ReplaceAll`. To avoid needless full rebuilds, an
"auto" sync mode (`archiveSyncAuto`, default) first checks `sourceAheadOfArchive()`: compares row
counts (`MessageRows`/`ContactRows`) and the newest-message timestamp against what's already archived,
plus `cache.SQLiteModifiedAfter` on the contacts DB — only re-imports if something actually advanced.
So: cheap idempotent no-op when nothing changed, full re-scan when it has — no cursor/pagination
inside a sync.

**Schema (from live `~/.wacrawl/wacrawl.db`):** contacts/chats/groups/participants/messages,
normalized off the CoreData `ZWA*` shape (JIDs, Apple-epoch timestamps converted to real time), with
`MediaPath`/`MediaURL`/`MediaType`/`MediaSize` columns per message and archive-relative media copying
with path-traversal guards (`archiveMediaPath` rejects paths escaping the media root).

**Rate limiting / anti-ban:** none needed — no network calls in the sync path at all.

**crawlkit vs bespoke:** the CoreData-schema reader, chat/participant merge logic (handling WhatsApp's
duplicate-JID CoreData quirks), and media path resolution are fully bespoke; crawlkit again
contributes only the generic snapshot/change-detection helpers (`cache.SnapshotSQLite`,
`cache.SQLiteModifiedAfter`).

---

## 4. discrawl

**Data-source posture: dual-mode — official Bot API *and* local Electron-cache forensics.**
- **API mode (primary, "official"):** `internal/discord/client.go` wraps `bwmarrin/discordgo` — a
  classic Discord bot token (`"Bot " + token`), resolved via `internal/config/discord_token.go`
  (`ResolveDiscordToken`: env var or OS keychain via `zalando/go-keyring`, never plaintext-in-config by
  default). Standard bot-scoped intents (`IntentsGuilds|IntentsGuildMessages|IntentsMessageContent|
  IntentsGuildMembers`). No OAuth flow, no 2FA — it's a static bot token the user provisions once via
  the Discord Developer Portal. Supports both REST pull (`Guilds`/`GuildChannels`/`ChannelMessages`,
  all correctly paginated with Discord's own cursor conventions: `before`/`after` snowflake IDs) and a
  live gateway "Tail" mode (`Client.Tail`) for real-time `MessageCreate/Update/Delete`,
  `ChannelCreate/Update`, and `GuildMember*` events, fanned out to a bounded worker pool with per-task
  timeout and panic recovery.
- **Local-cache mode ("wiretap", secondary/offline path):** `internal/discorddesktop/import.go` (1621
  lines) is a from-scratch **Electron-cache forensic scraper** for Discord Desktop — no bot token, no
  Discord API calls at all. `DefaultPath()` locates the OS-specific Electron cache dirs; regexes
  (`channelRouteRE`, `apiMessagesRouteRE`) recover guild/channel context from cached REST route strings
  embedded in raw Chromium `Cache_Data`/LevelDB files; `extractJSONObjects()`/`extractGzipPayloads()`
  carve JSON and gzip blobs directly out of the raw cache bytes; `parseChannel()`/`parseMessage()`
  reconstruct Discord-API-shaped records from whatever fragments happened to be cached by the running
  app; `reconcileMessages()`/`inferDirectMessageNames()` backfill missing `guild_id` context across
  multiple passes. This is the closest analog in this set to what an Instagram scraper would have to
  do, since it recovers structured data purely from what a real client already fetched and cached —
  no active network requests, no credentials of its own.

**Sync model:**
- API mode: cursor-based full-then-incremental per guild, with resumability. `channelCatalogMode`
  toggles full vs incremental channel enumeration; an incomplete-batch resume path
  (`syncGuildIncompleteBatches`, batches of 25 channels via `IncompleteMessageChannelIDs`) picks back
  up exactly where a prior full sync was interrupted. Member refresh is independently time-gated
  (`shouldRefreshMembers`, default 24h) and persisted via `SetSyncState`/`GetSyncState` keyed per guild
  (`guild:<id>:members:last_success`), so a killed run resumes cheaply instead of re-crawling members.
  A final `sync:last_success` timestamp marks whole-run completion.
- Wiretap mode: per-file fingerprinting (`fileFingerprint{Size, ModUnixNS, Status}`) persisted as
  `wiretap:file_index:v1` sync-state, with `wiretap:last_import` completion markers — incremental scan
  of only changed/new cache files by default, `FullCache` mode forces a complete rescan.

**Schema:** guild/channel/message/member tables shaped directly after Discord's own REST objects
(each record keeps a `raw_json` blob alongside normalized columns) — a `thread_` kind prefix
distinguishes threads from channels in stats. (Discrawl has no separate live DB inspected in this
session; schema inferred from `store.GuildRecord`/`ChannelRecord`/`MemberRecord` usage in `syncer.go`.)

**Rate limiting / anti-ban:** API mode relies entirely on `discordgo`'s own built-in per-route bucket
rate limiter (no custom flood-wait/backoff reimplementation the way telecrawl has one) — Discord's Bot
API rate limits are generous and officially documented, so this is lower-risk than the other three
tools' live paths. Wiretap mode has no rate limiting because it never makes a network call; risk is
purely "don't corrupt/lock the live Electron cache files while the real app is running" (handled via
read-only file access + fingerprint-based skip of unchanged files).

**crawlkit vs bespoke:** the wiretap forensic scraper and the Bot API syncer/tail pipeline are both
fully bespoke; no crawlkit usage was confirmed for discrawl's core data paths (worth re-checking its
go.mod directly if this matters — the tool clearly leans on `bwmarrin/discordgo` + `zalando/go-keyring`
as its main third-party dependencies instead).

---

## Which posture is the closest template for an Instagram crawler?

Instagram has no sanctioned bulk API (unlike GitHub/Discord's public REST APIs, and unlike Telegram/
WhatsApp which at least have official desktop apps whose *local* state can be read without touching
the network). That rules out gitcrawl's and discrawl's API-mode postures as templates — there's no
`gh`-equivalent CLI to delegate to, and no bot-token program equivalent to Instagram's Graph API for a
personal account's full history.

**wacrawl's local-app-state-reading posture is closest in spirit but doesn't map** — Instagram's
official mobile/desktop clients don't expose a comparably open local SQLite store the way WhatsApp
Desktop or Telegram Desktop do (Instagram's local caches are far more obfuscated/protobuf-heavy and
change often).

**The best template is a hybrid of telecrawl's session-reuse pattern and discrawl's wiretap
forensics:**
1. **Session-hijack, not fresh login** (telecrawl pattern): extract Instagram's own session
   cookies/tokens from an already-authenticated browser profile or the mobile app's local storage
   (the equivalent of telecrawl pulling the MTProto auth key out of Telegram Desktop's Postbox)
   rather than building a login/2FA flow. This avoids ever handling the user's password or triggering
   Instagram's bot-detection on login, and matches telecrawl's core insight: *reuse an auth artifact
   the official client already has, replay it with a client fingerprint that matches the real app.*
2. **Wiretap-style cache/response carving** (discrawl pattern) as the low-risk fallback/backfill path:
   if a private, closer-to-official endpoint (Instagram's internal mobile API, reverse-engineered) is
   too fragile or bannable to hit repeatedly, fall back to parsing what the official app already
   fetched and cached locally (web app's IndexedDB/Service-Worker cache, or the mobile app's on-disk
   cache), the same way discrawl recovers Discord messages from Electron's cache without ever calling
   Discord's API itself.
3. **telecrawl's flood-wait discipline** should be copied wholesale for any live-call path: bounded
   retries, a hard ceiling on wait time, device-fingerprint spoofing matched to a genuine official
   client (Instagram is far more aggressive about detecting non-genuine clients than Telegram, so this
   matters even more here).
4. **wacrawl's snapshot-then-diff sync model** (copy-for-consistency, cheap no-op when nothing
   advanced) is the right sync shape for whichever local-state source is chosen, since Instagram, like
   WhatsApp/Telegram Desktop, is a live app you don't want to lock or destabilize mid-read.

In short: **telecrawl is the primary architectural template** (session-reuse over stolen/extracted auth
state + strict rate-limit compliance mimicking the genuine client), with **discrawl's wiretap module as
the concrete implementation pattern** for the local-cache-carving half of the pipeline, and **wacrawl's
snapshot/diff sync loop** as the operational rhythm. gitcrawl's shared-rate-limit-state-file trick
(`ghSharedRateLimitState`, cross-process, keyed by token hash) is also worth borrowing regardless of
posture — it prevents multiple instacrawl invocations from independently exhausting whatever quota
Instagram's endpoint enforces.
