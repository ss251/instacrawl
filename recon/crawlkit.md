# crawlkit recon (for instacrawl)

Repo analyzed: `~/Developer/reference-repos/crawlkit` (cloned/pulled). Go module, pure-Go SQLite (`modernc.org/sqlite`, no cgo).

**Correction to task framing**: the task described this as "steipete's local-first crawler family," but the repo does not live at `github.com/steipete/crawlkit` (404 — repository not found there). It lives at **`github.com/openclaw/crawlkit`**. Evidence chain: Homebrew formula → GitHub homepage field → go.mod dependency chain, all pointing at the `openclaw` org, not `steipete`. Confirmed independently by memory obs 774/778 ("Steipete crawler suite maintenance moved to openclaw (not steipete as org)"; "Crawlkit is a modular Go toolkit, publicly available from openclaw (v0.13.3)"). Use `openclaw/crawlkit` for any future clone/pull/dependency-pin commands for instacrawl.

This document is code-grounded — every claim below cites a specific file (and mostly specific functions/lines) actually read in the repo, not README paraphrase.

---

## Version provenance and drift (coordinator follow-up)

Local checkout: `git describe --tags` → **`v0.13.3-1-g6703bbb`** (commit `6703bbb`, 2026-07-06), remote confirmed as `origin https://github.com/openclaw/crawlkit`.

Consumers in the family pin at different points on the tag history:

| consumer | pinned tag | relative to HEAD (v0.13.3+1) |
|---|---|---|
| slacrawl | v0.7.0 | **6 minor versions behind** |
| notcrawl | v0.13.1 | 2 patch versions behind |
| wacrawl / telecrawl | (pre-v0.10.0, exact tag not given) | old enough to predate `scheduler`/`control` |

**The v0.7.0 gap is architecturally significant, not cosmetic.** Both the `scheduler` package (`crawlctl` job engine) and the `control` package (`metadata --json`/`status --json` contract) were introduced together at **v0.10.0**:
- `scheduler/`: first commit `d41253c "feat: add crawlctl scheduler"`.
- `control/`: first commit `1f00196 "feat(control): add app metadata and status contracts"`.
- Both first appear in tag v0.10.0.

This means **slacrawl (pinned v0.7.0) and the pre-v0.10.0 wacrawl/telecrawl builds were written before the `control` contract existed at all** — they cannot implement `metadata --json` because that API surface didn't exist yet at the version they're built against. This is exactly why `scheduler/discover.go`'s `Discover()` carries a hardcoded `LegacyManifest(binary)` fallback specifically naming `wacrawl`/`telecrawl`/`slacrawl` (see Q3 above) — it's not a laziness shortcut, it's a compatibility shim for consumers frozen before the contract shipped. Any future crawlkit upgrade for those three apps would need actual code changes (adding `metadata --json`/`status --json` subcommands) before they could drop out of the legacy-manifest path.

notcrawl (pinned v0.13.1) postdates v0.10.0, so it should already implement the `control` contract properly. Diff from v0.13.1 to current HEAD (`git diff --stat v0.13.1..HEAD`) touches only `snapshot/snapshot.go` (+77/-19, adding "safe merge import plans," commit `f4cb025` "feat(snapshot): add safe merge import plans (#42)"), plus dependency bumps (`go-toml` 2.4.2→2.4.3, `modernc.org/sqlite`→1.53.0) and CI/changelog files. **No `store`, `scheduler`, `control`, `output`, or `backup` API changed between v0.13.1 and HEAD** — so notcrawl's drift is low-risk; the only functional delta is the new snapshot merge-import-plan capability, which notcrawl simply doesn't have yet.

**Implication for instacrawl**: pin to current HEAD (v0.13.3, or track the `v0.13.4` in-progress changelog) to get `control`/`scheduler` plus the new safe-merge snapshot import plans from day one — there's no reason for a brand-new crawler to start behind the `control` contract the way the legacy three did. When instacrawl later needs to bump its crawlkit dependency, the `store`/`scheduler`/`control`/`output` surfaces used throughout this doc have been stable since at least v0.13.1, so near-term upgrades should be safe; the main thing to watch is the `snapshot` package where merge-import-plan behavior is actively evolving (v0.13.2 added it).

---

## Q1. Package layout — what each package provides

| package | what it does |
|---|---|
| `store` | Thin SQLite wrapper: `Open`/`OpenReadOnly`, WAL pragmas, connection-pool sizing, schema-version stamping, FTS5 helpers, `LIKE`-escaping, safe identifier quoting. This is the DB primitive every app builds on. |
| `config` | XDG-style platform-native directory resolution (config/cache/data/state) with legacy flat-path fallback per field; TOML load/save via `pelletier/go-toml/v2`. |
| `control` | Standardized `metadata --json` / `status --json` CLI contracts (`crawlkit.control.v1`) so any tool (scheduler, other apps) can introspect an app without knowing its internals. |
| `state` | Canonical `sync_state` cursor/freshness table + adapter shapes for apps with legacy per-app schemas. |
| `snapshot` | Export/import system: JSONL+gzip sharded table exports with SHA256 fingerprints, `manifest.json`, tail-shard incremental-import planning (exact-mirror vs monotonic-merge), and `SyncSidecarTree` for atomically mirroring an on-disk directory (e.g. media files) into a snapshot with fingerprinting + stale-file pruning (`snapshot/sidecar.go`). |
| `mirror` | Git-backed cross-machine sync: clone/init/pull/push, stash-then-rebase-then-unstash conflict handling, path-scoped commits, atomic branch+tag pushes, immutable snapshot tags. |
| `scheduler` | TOML job config + app auto-discovery (via subprocess `metadata --json`, with a hardcoded legacy-manifest fallback) + per-app default job templates + a PID-lock-protected subprocess job runner with interval gating + JSONL run-history + OS-native installers (launchd/systemd/schtasks/cron-dry-run). This is `crawlctl`'s engine. |
| `cmd/crawlctl` | Reference CLI binary built on `scheduler` + `output`: `init/discover/run/status/logs/install/uninstall`. |
| `backup` | age-encrypted (`filippo.io/age`), gzip-compressed, content-addressed (SHA256) backup system — both table-shard export/import and a separate file-blob dedup/restore system. |
| `output` | Shared `Format` enum (text/json/log) + `Write()` dispatcher + `UsageError`/`IsUsage()` for consistent CLI exit codes. |
| `progress` | slog-based progress `Tracker`, gated by min-delta and time-interval to avoid CI log spam. |
| `releasecheck` | GitHub-releases self-update notifier with local JSON cache, 24h interval, terminal-only notification (skips CI/JSON/non-tty), semver comparison. |
| `embed` | `Provider` interface for embedding generation (OpenAI-compatible/Ollama/llama.cpp), `HTTPError`/`IsRateLimitError` (429 detection) — narrowly scoped to embedding calls. |
| `vector` | Float32 blob encode/decode for SQLite storage, `CosineSimilarity`, `Norm`, generic `TopK`, generic `ReciprocalRankFusion` (multi-ranking RRF). |
| `remote` | `BaseContract()` — HTTP route table + auth-tier definitions for a hosted (Cloudflare Worker + D1) remote-archive service. crawlkit owns only the provider-neutral Go client/contract; the Worker/D1 deployment is explicitly documented as living outside crawlkit. |
| `tui` | Charmbracelet Bubble Tea/Bubbles/Lipgloss shared terminal UI (~5.3k lines, not deeply read — lower priority; backend/sync architecture was the ask). |

---

## Q2. SQLite persistence pattern

From `store/store.go` (full read):

- **Driver**: `modernc.org/sqlite` (pure Go, no cgo) — imported blank (`_ "modernc.org/sqlite"`).
- **Write DSN** (`writeDSN`, line 217-219): `_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=mmap_size(268435456)&_pragma=busy_timeout(5000)` — WAL mode, 256MB mmap, 5s busy timeout, FK enforcement on.
- **Read-only DSN** (`readOnlyDSN`, line 221-223): adds `mode=ro&_pragma=query_only(1)` on top of the same pragma set (minus journal/synchronous), and `OpenReadOnly` forces `SetMaxOpenConns(1)`/`SetMaxIdleConns(1)`.
- **Connection pool sizing** (`configurePool`, line 280-291): defaults `MaxOpenConns`/`MaxIdleConns` to **1** unless the caller overrides — i.e. single-writer-by-default posture, appropriate for a local desktop-crawler DB, not a server.
- **File creation** (`ensureDBFile`, line 246-268): `os.MkdirAll` parent dir (0o755), then `O_CREATE|O_EXCL|O_WRONLY` at **0o600** — refuses to silently overwrite, and immediately locks down permissions.
- **Perm tightening** (`tightenDBFilePerms`, line 270-278): re-chmods to 0o600 after open (skipped on Windows/`:memory:`).
- **Schema versioning is NOT a migration framework** — it's a single `schema_migrations(version integer)` table (`EnsureSchemaVersion`, line 134-160): creates the table if missing, reads current max version, **refuses to open if the DB's stored version is newer than the caller's supported version** (forward-compat guard), and if older/absent, clears and inserts the new version in one write. There is no per-version-step migration runner in this package — schema evolution is left to the calling app (each app applies its own idempotent `CREATE TABLE IF NOT EXISTS ...` schema string via the `Options.Schema` field passed to `Open`, then stamps the version).
- **DB location convention**: not hardcoded in `store` itself — `config.App{Name, PlatformDirs}` resolves the platform-appropriate data dir, and each app (per `scheduler/discover.go`'s `DefaultJobForApp` naming, e.g. `wacrawl`/`telecrawl`) is expected to place its DB at `~/.<toolname>/<toolname>.db` (legacy convention) or the platform-native data dir when `PlatformDirs: true`.
- **FTS5 helpers** (`store/fts.go`, full read): `FTS5Phrase` (quote + escape doubled quotes), `FTS5Terms(value, operator)` (whitespace-split → quoted phrases joined by AND/OR, rejects unsupported operators), `FTS5TokenQuery` (strips all punctuation that could be parsed as FTS operators, keeping only letters/digits/marks/underscore per token — this is the "safe user search input" entry point), and `OptimizeFTS5(ctx, db, table)` (runs the FTS5 `'optimize'` special command with identifier-quoting validation).
- **LIKE escaping** (`store/query.go`): `EscapeLike` escapes `\`, `%`, `_` with backslash; caller must append `ESCAPE '\'`.
- **Generic query helper**: `Store.Query` runs arbitrary SQL and returns both a columnar `QueryResult.Rows` and a keyed `QueryResult.Values []map[string]any` — used by the `control`/remote-query surfaces for ad hoc introspection.
- **Identifier safety**: `QuoteIdent` panics on empty/embedded-quote/NUL identifiers rather than silently producing broken SQL — used everywhere a table/column name is interpolated (e.g. `OptimizeFTS5`).

---

## Q3. Incremental-sync / crawl-loop abstraction

This is the most important finding for instacrawl, and it's not what the task framing implied. **There is no `Crawler` interface with `Fetch()`/`Cursor()`/`NextPage()` methods that a new app implements against crawlkit.** Instead:

- `scheduler/run.go` (409 lines, full read) implements `Run(ctx, RunOptions)` — a **generic subprocess job runner**, not a per-item crawl loop. It:
  - Acquires a PID-based lock file (`acquireLock`) to prevent concurrent runs; detects stale locks via `syscall.Signal(0)` process-existence probing (`lockIsStale`/`processExists`).
  - Reads a JSONL run-history log.
  - For each `Job` in the TOML config, calls `jobDue()` — skips if the last recorded run succeeded and the configured `Every` interval hasn't elapsed yet.
  - Runs the job's `Command` as an **actual OS subprocess** (e.g. `wacrawl sync`), piping stdout/stderr through a size-capped `limitedLogWriter` into a per-run log file.
  - Appends a `RunRecord` (start/end time, exit status, log path) to the JSONL history.
- `scheduler/discover.go` (162 lines, full read): `Discover(ctx, binaries)` shells out to `<binary> metadata --json` to learn what commands/jobs an app supports; falls back to a **hardcoded `LegacyManifest`** for `wacrawl`/`telecrawl`/`slacrawl` (apps that predate the `metadata --json` contract). `DefaultJobForApp` supplies hardcoded default job templates per known app name (gitcrawl every 5m w/ `{repo}` placeholder expansion for multi-repo jobs, discrawl every 10m, wacrawl every 15m, telecrawl every 30m, slacrawl every 10m), falling back generically to `app.Manifest.Commands["sync"]` for anything not hardcoded.
- `scheduler/native.go` (319 lines, full read): installs the *whole crawlctl binary itself* as a single OS-native periodic job (launchd plist / systemd timer / Windows schtasks / cron-dry-run-print) that just re-invokes `crawlctl run` on an interval — crawlctl's own internal `jobDue()` gating is what actually spaces out each app's sync command from there.

**What this means for a new crawler (Q7 detail):** the incremental-sync cursor logic (pagination state, "what's new since last run," idempotent upsert) is **entirely the new app's own responsibility**, using the `state` package's `sync_state` table as a conventional place to persist a cursor/freshness timestamp. crawlkit's `scheduler` only handles *when to invoke the app's sync command* and *whether it succeeded last time* — it treats the app as an opaque CLI subprocess. The `control` package's `metadata --json`/`status --json` contract is the only "interface" a new crawler must actually implement to be discoverable/schedulable by `crawlctl` — not a Go interface, but a CLI contract (specific JSON shape on specific subcommands).

---

## Q4. Rate limiting, retry, and politeness primitives

**There is effectively none at the general crawl/provider-API level.** Exhaustive search across the repo turned up exactly one rate-limit-related primitive:

- `embed/provider.go`: `HTTPError` type + `IsRateLimitError()` which checks for HTTP 429 — and this is scoped **narrowly to embedding-provider HTTP calls** (OpenAI-compatible/Ollama/llama.cpp endpoints for generating vector embeddings), not to general web/API crawling.

No backoff/retry/jitter helper, no token-bucket, no concurrency limiter, no per-host politeness delay exists anywhere in crawlkit. **This confirms: rate limiting and politeness toward the Instagram API/web surface is entirely instacrawl's own responsibility to build** — crawlkit provides zero help here. This is a real gap to plan for explicitly (Instagram is aggressive about rate-limiting/blocking), likely worth a small dedicated internal package in instacrawl itself rather than expecting crawlkit to grow one.

---

## Q5. Media / blob storage approach

Definitively **files-on-disk, not SQLite BLOBs** — from `backup/backup.go`, `backup/crypto.go`, `backup/files.go` (all fully read):

- `backup.WriteSnapshotWithFiles` handles two parallel systems: table-shard export (JSONL rows → gzip → age-encrypt, `writeShard`) and a **separate file-blob path** delegated to `files.go`.
- `CollectFiles(ctx, root, prefix)` walks a directory tree (rejects symlinks outright), sorts deterministically.
- `writeFiles` hashes each file with SHA256, checks a `reusableFileEntry` against both already-processed-this-run and prior-manifest hashes — **content-addressed dedup**: unchanged files are never re-written.
- New/changed content is encrypted (age) + gzip'd into randomly-named shards at `data/files/objects/<hex>.gz.age`.
- An encrypted JSONL index (`_backup_files` / `data/files/index.jsonl.gz.age`) maps logical path → hash → size.
- `RestoreFilesUnder` decrypts, verifies SHA256+size before writing, uses atomic temp-file-then-rename, with extensive path-escape guards (`cleanFilePath`, `safeRestoreTarget`, `ResolveShardPath` confines all writes under `data/`) and TOCTOU protection (`os.SameFile` before/after open in `openSourceFile`).
- Separately, `snapshot/sidecar.go`'s `SyncSidecarTree` is the **non-encrypted, non-backup** analog used for regular (not backup) snapshot syncing of a managed directory tree — same idea (walk, fingerprint via SHA256, atomic copy via temp+rename, prune stale files not in the keep-set) but plaintext, for mirroring things like media caches between machines via the `mirror`/git path rather than the encrypted `backup` path.

**For instacrawl**: downloaded Instagram media (images/video/thumbnails) should live as files under the app's data directory, tracked by relative path + SHA256 in whatever manifest/state table instacrawl defines, following either the `backup` package's encrypted content-addressed pattern (if cross-machine encrypted backup matters) or the simpler `snapshot.SyncSidecarTree` plaintext-mirror pattern (if it's just local sync/dedup). Do not put binary blobs in SQLite.

---

## Q6. CLI / UX conventions shared across the family

From `output/output.go`, `progress/progress.go`, `releasecheck/releasecheck.go`, `cmd/crawlctl/main.go` (all fully read):

- **Output format enum**: `Format` = Text / JSON / Log, resolved via `Resolve(format, jsonFlag)`. `Write(w, format, label, value)` dispatches: JSON → indented `json.Encoder`; "log" → CI-safe `label=<json>` single-line records (with label-charset validation); Text → plain `Fprintln`.
- **Exit codes**: `output.UsageError{Err}` + `IsUsage()` — flag/usage errors map to exit code **2**, all other errors to exit code **1**. `cmd/crawlctl/main.go` mirrors this with its own `usageError` type and a `jobFailureError` for "ran but a job failed" (still exit 1, but distinguishable from a crash).
- **Global flag hoisting**: `hoistGlobalFlags` in `cmd/crawlctl/main.go` lets `--json`/`--config`/`--version` appear either before or after the subcommand name — a shared UX nicety worth replicating.
- **Subcommand shape** (crawlctl as the reference): `init` (discover apps + write default job config, `--repo`/`--app` repeatable flags), `discover`, `run`, `status`, `logs --tail N` (reads a JSONL history entry's log file), `install`/`uninstall` (OS-native scheduler registration).
- **FTS-backed search**: apps expose search via the `store` package's `FTS5*` helpers — safe token/phrase quoting so user search input can't break FTS5 query syntax or inject operators unexpectedly.
- **Progress reporting**: `progress.Tracker` (slog-based) logs at 0%, 100%, on a min-delta threshold, or at least every 30s — designed to avoid flooding CI/log output during long syncs, while still showing liveness.
- **Self-update notification**: `releasecheck` polls GitHub Releases with a 24h-cached check, silently skipped in CI/JSON-output/non-terminal contexts, opt-out via `<APPNAME>_NO_UPDATE_CHECK` env var.

---

## Q7. Exactly what a new crawler (instacrawl) must implement vs. inherits for free

**Inherited for free (use directly from crawlkit):**
- `store` — SQLite open/pragmas/pooling/schema-version-stamp/FTS5 helpers/LIKE-escaping/identifier-quoting.
- `config` — platform-native directory resolution + TOML config load/save.
- `output` — text/json/log formatting + exit-code convention.
- `progress` — CI-safe progress logging.
- `releasecheck` — self-update notification.
- `snapshot` — export/import + `SyncSidecarTree` for mirroring the media directory.
- `mirror` — git-backed cross-machine sync of the exported snapshot.
- `backup` — encrypted content-addressed backup of both DB tables and media files.
- `vector`/`embed` (optional) — if instacrawl wants semantic search over captions/comments, these give embedding generation + cosine/topk/RRF search for free; SQLite storage of the vectors as float32 blobs is via `vector.EncodeFloat32`.
- `scheduler`/`cmd/crawlctl` — instacrawl does NOT need to build its own scheduler; once it satisfies the `control` contract below, `crawlctl` can discover, schedule, lock, log, and OS-natively install periodic runs of it automatically.

**Must implement itself (crawlkit gives no help):**
1. **The `control` contract** — `metadata --json` and `status --json` subcommands in the shape `scheduler/discover.go` expects (or accept being added to crawlctl's hardcoded `LegacyManifest` fallback, though that's a stopgap, not the intended path for a new app). This is the one required "interface," and it's a CLI contract, not a Go interface.
2. **The actual Instagram fetch/pagination/cursor logic** — crawlkit has no `Crawler` interface; the sync command instacrawl exposes (e.g. `instacrawl sync`) is invoked as an opaque subprocess by the scheduler. Cursor/"since last run" state should be persisted via the `state` package's `sync_state` convention, but the fetch loop itself, pagination handling, and idempotent upsert logic are 100% instacrawl's own code.
3. **Rate limiting / retry / backoff / politeness toward Instagram** — no primitive exists anywhere in crawlkit outside the embedding-provider's narrow 429 check. This needs to be built from scratch in instacrawl, and given Instagram's aggressive anti-scraping posture, should probably be designed carefully up front (token bucket, exponential backoff w/ jitter, maybe session/proxy rotation) rather than bolted on later.
4. **The DB schema itself** — table definitions, indices, FTS5 virtual tables for caption/comment search — instacrawl defines its own schema string passed to `store.Open(ctx, Options{Schema: ..., SchemaVersion: N})`; crawlkit only provides the version-stamp guard rail, not migration steps between versions (if instacrawl needs multi-step migrations later, that logic must be hand-rolled on top of the version-check, e.g. `if current < 2 { ALTER TABLE ... }` chains inside the app before calling `EnsureSchemaVersion`).
5. **Auth/session handling with Instagram** — entirely out of scope for crawlkit (which has zero opinion about any specific data source); this is pure instacrawl domain logic.
6. **Media download + file layout under the data dir** — instacrawl chooses the directory structure; crawlkit's `snapshot.SyncSidecarTree`/`backup` files.go only mirror/back up whatever tree instacrawl already maintains.

---

## Files read (for reference / follow-up)

Fully read: `store/store.go`, `store/fts.go`, `store/query.go`, `snapshot/sidecar.go`, `scheduler/discover.go`, `scheduler/config.go`, `scheduler/run.go`, `scheduler/native.go`, `cmd/crawlctl/main.go`, `backup/backup.go`, `backup/crypto.go`, `backup/files.go`, `output/output.go`, `progress/progress.go`, `releasecheck/releasecheck.go`, `embed/provider.go`, `vector/vector.go`, `remote/contract.go`.

Not read / lower priority (not needed to answer the 7 questions; flagged for completeness): `embed/ollama.go`, `embed/openai_compatible.go` (concrete provider impls), `vector/search.go`, `vector/turbovec.go` (search backend impl detail), `remote/remote.go`, `remote/sqlite_bundle.go` (HTTP client mechanics beyond the contract table), `tui/tui.go` (~5.3k-line shared terminal UI, out of scope for backend/sync architecture), `config/*`, `control/*`, `state/*`, `mirror/*` in full depth (read/summarized from earlier pass in this same research thread, per prior context, but not re-verified line-by-line in this pass).

Note: two other background subagents were running in parallel on related instacrawl recon ("Analyze installed crawlers' source" and "Analyze uninstalled crawler repos") — this document covers crawlkit itself only, and should be read alongside their outputs rather than duplicating them.
