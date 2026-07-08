# Changelog

## [Unreleased]

### Added
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
