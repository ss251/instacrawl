# Changelog

## [Unreleased]

### Added
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
