# instacrawl

**A local-first CLI that archives _your own_ Instagram data — DMs, posts, reels,
stories, saved and liked — to a searchable SQLite database on your own disk.**

instacrawl is part of the [crawlkit](https://github.com/openclaw/crawlkit)
local-first crawler family (gitcrawl, telecrawl, wacrawl, discrawl). It reuses
the session you are already logged into, stores everything locally, and uploads
nothing.

```
● local-only   ● no telemetry   ● no uploads   ● your own account only
```

## Status

The web-session lane is built end to end — import a session, archive your DMs
and own content with media, recover expired media from a local cache, and
full-text search the result. The mobile lane remains opt-in and stubbed.

| Phase | What | State |
|------:|------|-------|
| 0 | Control contract + reliability spine | ✅ done |
| 1 | Web-session auth (`auth import/status/logout`) | ✅ done |
| 2 | DM / inbox archiver | ✅ done |
| 3 | Own content — posts, reels, stories, saved, liked + media bytes | ✅ done |
| 4 | Cache-carve — recover expired media offline (walker; chromium/indexeddb stubbed) | ✅ done |
| 5 | Mobile private-API lane | ⏳ opt-in, stubbed |
| 6 | Full-text search + honest status | ✅ done |

See [`DECISIONS.md`](./DECISIONS.md) for the full architecture and the phase plan.

## What it does

- **Archives your own account.** DM/inbox threads, your posts/reels/stories/
  highlights, your saved collections and liked posts.
- **Keeps it local and searchable.** One SQLite file at `~/.instacrawl/`, media
  as content-addressed files on disk, full-text search over your messages and
  captions.
- **Recovers expired media offline.** Instagram's CDN URLs expire; `instacrawl
  carve` reads a local browser/app cache to backfill the bytes, with zero network
  access.
- **Fails loud, never silently empty.** A `200 OK` is not treated as success on
  its own — every response is classified (`OK` / `AUTH·CHECKPOINT` /
  `RATE·LIMITED` / `SCHEMA·DRIFT`) before anything is written, so a blocked or
  changed API produces a clear stop, not a quietly empty archive.

## Usage

```sh
# 1. Import the session from your logged-in browser's instagram.com cookies
#    (Cookie-Editor → JSON, or a cookies.txt export). The cookie value is read
#    from stdin or a file — never a command-line argument (shell-history leak).
pbpaste | instacrawl auth import --stdin
instacrawl auth status                 # OK — logged in as @you

# 2. Archive
instacrawl sync                        # DMs + own content + media
instacrawl sync --only feed --no-media # just the feed metadata, faster

# 3. Recover any media whose CDN URLs expired, from a local cache (offline)
instacrawl carve --dir ~/path/to/a/media/cache --dry-run

# 4. Search your archive (results go to your terminal only)
instacrawl search aurora
instacrawl search --type dm "trip to japan"
instacrawl search --type media sunset --limit 50

instacrawl status --json               # honest counts: messages, media, …
```

## Honest use — please read

instacrawl is built for **one purpose: letting you keep a personal, offline
archive of your own account.** Instagram does not offer a sanctioned bulk API
for this, so instacrawl reuses your logged-in session against Instagram's own
endpoints. That means:

- **Your account only.** instacrawl archives the account you are logged in as.
  It is not a tool for collecting other people's data.
- **This may be against Instagram's Terms of Service.** Automating requests
  against Instagram's private endpoints can violate the ToS and, in some cases,
  can get an account rate-limited, checkpointed, or banned. **You use instacrawl
  at your own risk.** Read Instagram's Terms before you run it, and prefer the
  official [Download Your Information](https://www.instagram.com/download/request/)
  export where it covers your needs.
- **No evasion, by design.** instacrawl does **not** use proxies, rotate
  accounts, or attempt to solve login challenges. If Instagram presents a
  checkpoint or 2FA challenge, instacrawl **stops** and hands the challenge URL
  to you to resolve in the normal app — it never tries to defeat it.
- **The mobile-API lane is opt-in and off by default.** The safe default is a
  web session using your real browser cookies (nothing fabricated). A richer
  mobile lane exists but fabricates a device fingerprint and carries higher
  checkpoint risk, so you must explicitly turn it on. See `DECISIONS.md §5`.

If you are not comfortable with these trade-offs, don't run it.

## Privacy & security

- **Nothing leaves your machine.** No telemetry, no uploads, no remote calls
  except to Instagram itself to fetch your own data. `carve` and `search` make
  no network calls at all.
- **Secrets live in your OS keychain**, never in plaintext config. Session
  cookies and tokens are redacted from all logs.
- **Your archive and config are git-ignored.** DM content is never committed or
  logged, and `search` never logs the query or the matched text — results print
  to your terminal only.

## Install

_Not yet released._ A Homebrew tap will follow the first tagged release. To
build from source:

```sh
go build -o instacrawl ./cmd/instacrawl
./instacrawl --help
```

Requires Go 1.26.4+.

## How it fits crawlkit

instacrawl satisfies the crawlkit `control` contract, so the `crawlctl`
scheduler can discover it, schedule periodic syncs, lock against overlap, and
log runs — without instacrawl shipping its own scheduler. crawlkit provides the
SQLite store, FTS5 search primitives, content-addressed media storage,
snapshots, and encrypted backup; instacrawl builds the Instagram-specific fetch,
pagination, verification, multi-source reconciliation, and the FTS index on top.
Details in [`DECISIONS.md`](./DECISIONS.md).

## License

[MIT](./LICENSE).
