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

> [!IMPORTANT]
> **Status: Phase 0 scaffold.** The control contract (`metadata` / `status`) and
> the reliability spine are implemented and tested. `sync` — the actual fetch —
> is not implemented yet. See [`DECISIONS.md`](./DECISIONS.md) for the full
> architecture and the phase plan.

## What it does

- **Archives your own account.** DM/inbox threads, your posts/reels/stories/
  highlights, your saved collections and liked posts.
- **Keeps it local and searchable.** One SQLite file at `~/.instacrawl/`, media
  as content-addressed files on disk, full-text search over your messages and
  captions.
- **Fails loud, never silently empty.** A `200 OK` is not treated as success on
  its own — every response is classified (`OK` / `AUTH·CHECKPOINT` /
  `RATE·LIMITED` / `SCHEMA·DRIFT`) before anything is written, so a blocked or
  changed API produces a clear stop, not a quietly empty archive.

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
  except to Instagram itself to fetch your own data.
- **Secrets live in your OS keychain**, never in plaintext config. Session
  cookies and tokens are redacted from all logs.
- **Your archive and config are git-ignored.** DM content is never committed or
  logged.

## Install

_Not yet released._ A Homebrew tap will follow the first tagged release. To
build the current scaffold from source:

```sh
go build -o instacrawl ./cmd/instacrawl
./instacrawl metadata --json    # prints the crawlkit control manifest
./instacrawl status --json      # prints archive status (empty until you sync)
```

Requires Go 1.26.4+.

## How it fits crawlkit

instacrawl satisfies the crawlkit `control` contract, so the `crawlctl`
scheduler can discover it, schedule periodic syncs, lock against overlap, and
log runs — without instacrawl shipping its own scheduler. crawlkit provides the
SQLite store, FTS5 search, content-addressed media storage, snapshots, and
encrypted backup; instacrawl builds the Instagram-specific fetch, pagination,
verification, and multi-source reconciliation on top. Details in
[`DECISIONS.md`](./DECISIONS.md).

## License

[MIT](./LICENSE).
