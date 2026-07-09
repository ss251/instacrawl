# Changelog

## [Unreleased]

### Fixed
- Direct messages now archive against live Instagram (validated end to end). Three
  issues the reliability gate caught on the first real run: the web session now
  carries the full instagram.com cookie jar (the DM endpoint needs `datr`, which
  the self-profile endpoint doesn't); the message-requests inbox uses
  `/inbox/?folder=pending` (the old `/pending_inbox/` path 404s on web) and is now
  best-effort so it can't halt the main archive; and the `clip` (shared Reels),
  `video_call_event`, `raven_media` (view-once media), and `profile` (shared
  profile) message types are recognized. An unrecognized message type is now
  stored (flagged, with its raw JSON) instead of dropping the page, so adding the
  type later re-parses it with no re-fetch.
- Thread backfill now resumes across runs. A thread has two independent frontiers
  — its newest message and its backward-backfill point — and a page-capped or
  interrupted run left the deep-history frontier stranded (and a later run could
  clobber it). Each thread is now crawled in a head catch-up + a tail backfill
  that advance their own frontier, so a capped run's remaining history is picked
  up on the next run instead of being skipped, while a fully-backfilled unchanged
  thread still costs zero fetches.

### Added
- `instacrawl reparse` re-parses messages that were archived with an item type an
  earlier build didn't recognize, from their retained raw JSON, with no network —
  so a parser update recovers old messages without re-crawling. `status` now
  reports an `unknown_messages` count (and a warning) so unrecognized types are
  visible without reading a run's exit code.
- Zero-friction session import: `instacrawl auth import --from-browser chrome`
  (or `auto`) reads your logged-in instagram.com session **straight from the
  browser's own cookie store** — no Cookie-Editor export, no copy/paste — like
  `yt-dlp --cookies-from-browser`. The cookie is decrypted in-process and written
  directly to the OS keychain; it is never printed or logged. Reads are locked to
  the instagram.com domain and the known session cookies only. Supports
  chrome/brave/edge/chromium/firefox, auto-detects the browser/profile holding a
  live session, and disambiguates when multiple accounts are logged in. On macOS
  a one-time "Allow" keychain prompt is expected.
- Full-text search: `instacrawl search <terms>` searches your archived message
  text and captions (`--type dm|media|all`, `--limit`, `--json`, `--reindex`),
  backed by SQLite FTS5 indexes kept in sync by triggers. Ranked by relevance,
  with a bracketed snippet per hit; unsent messages stay searchable and are
  flagged. The query and matched content are never logged — results go to your
  terminal only. `status --json` now reports honest archive counts (messages,
  media, blobs, …) and the last-sync time.
- Cache-carve recovery: a new `carve` command recovers the bytes of media whose
  Instagram CDN URLs have expired (the 403s `sync` records as "missing"), reading
  them back from a local browser/app cache with **zero network access**. Point it
  at a cache directory (`carve --dir <path>`, `--dry-run` to preview) and it
  attributes files to your archive by CDN filename, verifies each by content, and
  fills only the byte slots that are empty — it never overwrites bytes you already
  have, and re-running is a no-op. The source reconciler that underpins the whole
  archive now merges every lane (web / mobile / carve) by richness then recency
  then lane, so a stale cache can never displace a fresher fetch. The
  format-specific Chromium/IndexedDB cache extractors are stubbed until validated
  against a real cache; the format-agnostic directory walker is the default.
- Own-content archiver: `sync` now also captures your posts, reels, saved posts,
  saved collections, and currently-live stories, and downloads the media bytes
  into a content-addressed store on disk (deduplicated by SHA256; CDN URLs are
  fetched in the same pass because they expire). Feeds resume incrementally — an
  unchanged feed costs zero fetches on a re-run — and an expired media URL is
  flagged missing rather than failing the run. The (usually restricted) liked
  feed is best-effort: it reports "unavailable" instead of pretending it's empty.
  Flags: `--only feed`, `--include-saved`, `--include-collections`,
  `--include-stories`, `--include-liked`, `--no-media`, `--max-feed-pages`.
- Direct-message archiver: `sync` walks the inbox (and message requests),
  archives each thread with three-way resume so an unchanged thread costs zero
  fetches on a re-run, and stores messages/threads/participants/reactions/events
  to SQLite. Idempotent by stable id, cursor advance is transactional with the
  row upserts, unsends are flagged (never deleted), and unknown message types
  surface as drift instead of being skipped.
- Web-session authentication: `auth import | status | logout`. The session
  bundle is stored in the OS keychain, never in plaintext, and cookies are
  redacted from all logs.
- Hybrid fetch seam behind a single `Fetcher` interface — a Go-native web
  client (the default, with no fabricated device) and an instagrapi subprocess
  (opt-in, stubbed until the mobile phase).
- Reliability spine: a four-state response gate — `OK` / `AUTH·CHECKPOINT` /
  `RATE·LIMITED` / `SCHEMA·DRIFT` — that treats a 200 as success only when both
  the response shape and the account identity check out.
- crawlkit control contract (`metadata --json` / `status --json`) so the
  `crawlctl` scheduler can discover the tool.
- Local archive store (SQLite via crawlkit) with an idempotent `account` upsert.
- MIT license, ToS-honest README, and the design record in `DECISIONS.md`.
