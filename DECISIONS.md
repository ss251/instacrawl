# instacrawl — decisions & contracts (forcing-function doc)

_Locked 2026-07-08. This is the binding record: the four architecture forks and
the reliability / session / source-reconciler contracts every later phase must
honor. Change a decision here **before** changing code, not after. The scoping
rationale lives in `.lavish/scoping.html`; the loop-engineering principles this
doc encodes come from `~/Developer/claude-mission-control/docs/RESEARCH_loops{2,3,4}.md`,
`RESEARCH_evals_observability_code.md`, `RESEARCH_humanlayer_code.md`,
`RESEARCH_indydevdan_code.md`, and `RESEARCH_steipete.md`._

---

## 0. What instacrawl is (and is not)

A local-first CLI that archives **your own** Instagram account — DMs, your
posts/reels/stories/highlights, saved and liked — by reusing the session you are
already logged into, persisting to SQLite via the [crawlkit](https://github.com/openclaw/crawlkit)
Go substrate, and exposing itself to the `crawlctl` scheduler through the
crawlkit control contract.

It is **not** a growth tool, a scraper of other people's data, or an
evasion tool. It archives the owner's account only, stores everything on the
owner's disk, and uploads nothing. See §5 for the honesty posture and §6 for
what is explicitly out of scope.

---

## 1. The four locked forks

| fork | decision | consequence |
|---|---|---|
| **A · runtime** | **Hybrid** — Go control/store shell (crawlkit-native) + Python `instagrapi` fetch subprocess across crawlkit's opaque-subprocess seam. | Family-native distribution & crawlkit substrate for storage/scheduling/search; Python owns the **mobile** lane (instagrapi's mature device/challenge handling). The **web** lane can be Go- or Python-side. crawlkit runs the fetcher as an opaque OS subprocess, so the fetch language is free. |
| **B · API surface** | **Both** — desktop **web session** (default) ⊕ **mobile private API** (opt-in). | Web session (real browser cookies → web endpoints) is the family-native, no-fabricated-device **safe default**, always on. Mobile `i.instagram.com/api/v1` is an **opt-in** lane, **off by default**, for complete `direct_v2` DMs and richer story/reel metadata — it fabricates a device, so it carries the highest checkpoint risk. Both merge in the reconciler (§3); either is re-addable later at zero migration cost. |
| **C · cache-carve** | **Co-equal v1 source** (override — not deferred). | A zero-network parser for the client's local cache (browser IndexedDB / mobile app media cache) ships in v1 alongside the API, feeding the same source reconciler. Backfills media whose CDN URLs have expired. |
| **D · license & dist** | **MIT + Homebrew tap.** | Matches gitcrawl / telecrawl / wacrawl / discrawl. Permissive license; the name / README / OSS hygiene are fixed in Phase 0. |

**Provisional identifiers (changeable in Phase 0, before any release):**
- Module path: `github.com/ss251/instacrawl` — may move to `github.com/openclaw/instacrawl` if adopted into the family org.
- crawlkit pin: `v0.13.4-0.20260706080912-6703bbbb9d87` (exact HEAD, one commit past `v0.13.3`). Fetched from the public proxy — no `replace` directive. The `control` / `output` / `store` surfaces used are stable since `v0.13.1`.
- Toolchain: Go `1.26.4`+ (crawlkit's floor; local build on `1.26.5`).

---

## 2. Architecture — crawlkit is a thin substrate, not a framework

crawlkit exposes **no `Crawler` interface**. Its scheduler runs each tool's
`sync` as an opaque OS subprocess and only knows the tool through the `control`
CLI contract (`metadata --json` / `status --json`). Everything about *how*
instacrawl fetches, paginates, and verifies is instacrawl's own code.

**Inherited free from crawlkit:** SQLite store (WAL, single-writer, 0600,
schema-version stamp), FTS5 search helpers, SHA256 files-on-disk media,
snapshot+diff sync, age-encrypted backup, `crawlctl` scheduler, `output`
(format + exit codes), `progress`, `releasecheck`, optional `vector`/`embed`.

**Built by instacrawl:** the Instagram schema, the fetch/pagination/cursor
logic, the rate-limit/politeness engine (crawlkit has **none** — one 429 helper,
scoped to embeddings), the four-state verification gate, the source reconciler,
the session bundles, and media byte-fetch.

> **Risk flagged (RESEARCH_steipete.md):** none of the shipped family crawlers
> (wacrawl/telecrawl) face a live authenticated API — they read local app data
> offline — and `crawlctl` is described as the *future* cron piece, not a
> hardened one. **Do not assume crawlctl solves lock-safety or orphan
> reconciliation.** instacrawl builds those defensively (§4.C).

---

## 3. Source-reconciler contract

Three source lanes, all `source`-tagged, merge into one archive:

- `source='web'` — default lane, real browser cookies, lowest risk, always on.
- `source='mobile'` — opt-in lane, fabricated-device bundle, off by default.
- `source='carve'` — zero-network local-cache parser.

**Merge rule (birdclaw 4-tier precedence — by field-completeness/recency, NOT a
fixed source ladder):** for the same stable ID, the richest/most-recent row
wins whichever lane produced it. `source` is provenance + dedup, not priority.

**Named tie-break (required — RESEARCH_evals #9, ccusage):** when the same
stable ID reappears with conflicting field values, the winner is chosen by, in
order: (1) more non-null fields, then (2) higher `fetched_at`, then (3) lane
order web > mobile > carve as a final deterministic tiebreak. This rule is
written here so it is auditable, not improvised per call site.

**Two reconciliation planes (refined Phase 4 — carve made this precise):** a
media row has a *metadata* plane (the `media` identity row: code, caption,
type…) and a *bytes* plane (`media_file` → `media_blob`). They reconcile by
**different rules and must not be conflated**:
- **Metadata reconciles by richness/recency** — the tie-break above, executed by
  the `upsertMedia` `on conflict` WHERE clause. A leaner row never clobbers a
  richer one whatever lane produced it.
- **Bytes reconcile by fill-only-empty** — bytes are content-addressed, so they
  are only ever *present or absent*, never "richer". A lane may only transition a
  `media_file` from no-bytes (`downloaded=0`) to bytes; it may never replace an
  existing `blob_hash`. This is why the carve lane backfills through a guarded
  `downloaded=0` update (`store.BackfillMediaFile`) and **never** through the
  metadata upsert path — routing carve bytes through `upsertMediaFile` (which is
  last-writer-wins and lane-hardcoded) would let a carve pass clobber a live
  download. Byte provenance is recorded on `media_blob.source`; `media_file`'s
  own `source` stays the metadata provenance.

**Zero-migration corollary:** because every row is `source`-tagged and merged by
richness, adding or removing a lane later touches no schema and no existing
rows. This is *why* "both" (fork B) costs no new architecture. Carve confirmed
this: it added a whole source lane at `SchemaVersion` **3**, zero DDL.

---

## 4. Reliability spine — a 200 is not success

The load-bearing contract. Codified as real Go types in
`internal/reliability/` (not prose): every response from every lane is
classified by a gate **independent of the request layer** before a single row
is committed. Grounded in the loop-engineering principles below.

### A. The four-state gate (`reliability.State`)

A single scalar pass/fail cannot regulate a multi-mode failure space
(Ashby's Law / Good Regulator Theorem — RESEARCH_loops3). The gate is
four-valued and closed (an exhaustiveness test enforces the closed set):

| state | meaning | action |
|---|---|---|
| `OK(n)` | schema matched **and** cleared the semantic floor | commit — the only committable state |
| `AUTH·CHECKPOINT` | login/challenge/checkpoint detected | **hard stop**, hand the challenge URL to the user, **never auto-solve** 2FA/checkpoints |
| `RATE·LIMITED` | 429 / `feedback_required` soft block | back off, checkpoint cursor, stop — never grind a soft block into a hard block |
| `SCHEMA·DRIFT` | reachable & authorized but shape changed (incl. unknown DM `item_type`, or stale cursor `schema_version`) | stop, **dump raw JSON** for re-parse; unknown `item_type` is drift, never a silent skip |

- **The gate is a function distinct from the HTTP handler** (RESEARCH_loops4,
  ratchet-loop `claim_rejected`): a 200-with-empty-body is rejected by the gate,
  never trusted because the request layer said "ok."
- **Semantic floor inside `OK(n)`** (RESEARCH_loops3, `Floor.Apply`): a resource
  with *known* history that returns 0 records for N consecutive scheduled runs
  is escalated to `SCHEMA·DRIFT`, even though every page was HTTP 200 and
  shape-valid. A resource with no known history legitimately returns 0.
- **Fail-open / fail-closed split** (RESEARCH_indydevdan, `reliability.GateError`):
  an error *inside the gate* (a bug in our own validator) surfaces as a
  `GateError` value, logged and non-fatal — it is **not** a `SCHEMA·DRIFT`, so a
  checker bug never masquerades as the remote changing its API. A genuine remote
  contract violation is a `SCHEMA·DRIFT` verdict with a nil error.
- **Success is silent, failure is verbose** (RESEARCH_loops4, Osmani): `OK(n)`
  emits nothing; every other state emits a specific reason (resource, cursor,
  payload excerpt).

### B. Raw-JSON retention & lenient re-parse

Every response is stored raw alongside the parsed rows. A schema-drift fix later
**re-parses history without re-hitting the API**. The re-parse path is lenient
(RESEARCH_evals #13, ccusage): a single renamed/missing field degrades to
null/default, so a *second* future drift cannot fail an entire historical batch
over one field.

### C. Cursors, resume, idempotency, locks

- **Cursor rows carry a `schema_version`** (RESEARCH_evals #7, CodexBar):
  `SCHEMA·DRIFT` also fires on a cursor-schema mismatch, so a parser change
  auto-invalidates stale cursors instead of silently trusting them.
- **Three-way resume classification** (RESEARCH_evals #8, CodexBar), not a
  binary resume flag: per resource → *unchanged* (skip) / *grew* (fetch only the
  new tail) / *invalidated* (full re-crawl).
- **Idempotency ≠ locking** (RESEARCH_loops3): upsert-by-stable-ID is the
  idempotency key and must be **transactionally paired with cursor advancement**
  so a crash between fetch and commit can neither double-apply nor half-apply a
  page.
- **Pagination guard independent of the gate** (RESEARCH_loops3, termination
  poisoning — `reliability.PageGuard`): a hard max-pages-per-run cap **plus**
  non-advancing-cursor detection, structurally separate from the four-state gate,
  so a drifted response cannot poison the "am I done paginating" decision.
- **Stale-lock-safe overlap prevention** (RESEARCH_loops2/indydevdan): the
  per-run lock probes PID liveness (`kill(pid,0)`) / an owner token before
  treating an existing lock as stale — a crashed process must not wedge every
  future scheduled run.
- **Atomic checkpoints + orphan reconciliation** (RESEARCH_evals/humanlayer):
  cursor writes are tmp+rename / in-transaction; a scheduler restart sweeps any
  resource left "in-progress" whose process died and marks it resumable. `SIGINT`
  is a deliberate graceful path (flush cursor, exit) distinct from `SIGKILL`.
- **Dead-man's-switch** (RESEARCH_loops3): track last-cursor-advance per
  resource; no advance across N ticks is an alertable state independent of any
  single run's exit code.

---

## 5. Session & security contract

**Two session bundles, one per lane — never mixed:**

- **Web (default):** just the real browser cookie jar
  (`sessionid` / `ds_user_id` / `csrftoken` / `mid` / `rur` / `ig_did`) + a web
  UA. Nothing fabricated. Imported by cookie paste/file, or — the zero-friction
  path — by `--from-browser`, which reads the owner's own instagram.com cookies
  **directly from the local browser's on-disk store** and decrypts them
  in-process (`internal/browsercookie`, backed by the pure-Go `kooky`), then hands
  them straight to the OS keychain. This is the *purest* form of "just the real
  browser cookie jar, nothing fabricated" — it literally copies the live browser
  session. Scoped hard: the read is filtered to the `instagram.com` domain AND to
  those six cookie names (never a general cookie export), the raw value is never
  printed or logged (only the resolved `@username`), and on macOS a one-time
  Keychain consent prompt is expected (same mechanism as `yt-dlp
  --cookies-from-browser`). Supported: chrome/brave/edge/chromium/firefox; Arc and
  Safari are a documented fast-follow.
- **Mobile (opt-in):** the full fabricated-device bundle — stable device seed
  (regenerating it re-triggers a checkpoint), rotating `X-IG-WWW-Claim`,
  `Bearer IGT:` token, matched UA + `X-IG-App-ID`.

**Hard rules:**
- **Never mix bundles** — a browser cookie replayed against mobile endpoints is a
  signature mismatch → checkpoint.
- **Never run both lanes concurrently against one account** — two client
  signatures on one IP. The scheduler staggers them.
- **Secrets in the OS Keychain, never plaintext.** Every log **redacts**
  `sessionid` / cookies / `csrftoken`. The manifest declares
  `exports_secrets: false`.
- **DM / message content stays in-session** — never committed to git, never
  logged. `.gitignore` excludes the config and the entire archive dir.
- **No evasion** — no proxies, no multi-account, no challenge-solving.
  `AUTH·CHECKPOINT` is a hard stop that hands the challenge URL to the user; the
  tool never solves a 2FA or checkpoint itself.
- The manifest declares `contains_private_messages: true` — honest to any
  scheduler or UI that surfaces it.

---

## 6. Out of scope for v1

Target-profile watchlists · following other people's stories · any
multi-account / proxy / challenge-solving evasion. The **mobile lane ships in v1
but stays opt-in / off by default.**

---

## 7. Build phases (each gated on a semantic postcondition)

- **Phase 0 — decision doc + repo skeleton + control stub.** _(this doc, plus
  the Go module, `internal/reliability` spine types, and the
  `metadata`/`status` contract stub — **done**; gate: `instacrawl metadata --json`
  is valid `crawlkit.control.v1` and `status --json` reports honestly.)_
- **Phase 1 — session bundle + auth import (read-only probe).** _(**1a done** —
  `internal/session` bundle+keychain (go-keyring, discrawl pattern), `internal/ig`
  hybrid `Fetcher` seam + pure four-state classifier, `internal/igweb` Go-native
  fetcher (default), `internal/igmobile` stub (Phase-5), `internal/store` account
  upsert, and `auth import|status|logout`; all built, vetted, and tested offline.
  Transport is **hybrid**: Go-native web is the default, instagrapi is opt-in.
  **1b shape-validated 2026-07-09** — a live schema-only probe confirmed
  `/api/v1/users/{id}/info/` returns `{user:{pk, username, full_name, is_private,
  profile_pic_url}}` exactly as parsed. Full binary end-to-end (auth import →
  probe) still open; programmatic session extraction is gated by the safety
  classifier, so it's run by the owner.)_ Gate: a live run returns `OK(1)` for
  self-profile; a corrupted session returns `AUTH·CHECKPOINT`, not a crash.
- **Phase 2 — DM/inbox archiver (web lane, the wedge).** _(**2a done** —
  `internal/dm` (tagged-union model, lenient parsers, schema-versioned cursors,
  three-way resume, reconciler), `internal/store` DM tables v2 + transactional
  `CommitThreadPage`, `internal/dmsync` engine (inbox→thread→item with
  `PageGuard`/`Floor`/cursors, window-bounded unsend detection, pending pass,
  per-thread drift isolation), `internal/pace`, and the real `sync` command;
  offline idempotency gate test passes. **2b shape-validated 2026-07-09** — a
  live schema-only probe confirmed the inbox/thread endpoints and every parsed
  field (thread_id, thread_title, is_group, users, admin_user_ids,
  last_activity_at, items, oldest_cursor, has_older; item_id/user_id/timestamp/
  item_type/text; pk/username/full_name/profile_pic_url) match the live shapes;
  no retune needed. **Full binary end-to-end CLOSED 2026-07-09** — a real live
  run (session via `--from-browser`) archived 80 threads / 2,547 messages and
  they were searchable. It surfaced three real bugs the spine caught (not silent
  corruption): the DM endpoint needs the whole cookie jar incl. `datr` (fixed:
  `WebBundle.Extra` carries the full instagram.com jar); the web pending inbox is
  `/inbox/?folder=pending`, not `/pending_inbox/` (fixed + made best-effort); and
  real DMs use `clip`/`video_call_event`/`raven_media`/`profile` item types
  (each confirmed from retained raw before parse handling, never guessed), which
  also completed the §4B contract — an unknown item_type now commits leniently +
  flags rather than dropping the page. Two follow-ups closed the lane: **backfill
  now resumes across runs** (a thread carries independent head + tail frontiers,
  each advanced by its own crawl phase, so a page-capped run's leftover deep
  history is picked up next run instead of being skipped or clobbered), and the
  new **`instacrawl reparse`** verb re-decodes drifted messages from retained raw
  with no network — on the live archive it recovered 16/16 and drove
  `unknown_messages` to 0. See `recon/live-validation.md`.)_ Gate: two runs;
  run-2 fetches 0 new for unchanged fully-backfilled threads (idempotent); every
  row carries `source='web'`.
- **Phase 3 — own content + saved + liked.** _(**3a done** — `internal/feed`
  (media/story/collection domain, lenient big-int-safe parsers, schema-versioned
  feed cursors with three-way resume, richness-tiered reconciler), `internal/media`
  (content-addressed SHA256 byte-downloader, bounded pool, 403→missing, dedup),
  `internal/store` feed tables v3 + transactional `CommitFeedPage`/`CommitStorySet`,
  `internal/feedsync` engine (stories-first → own+saved → collections → liked, with
  `PageGuard`/`Floor`/download-in-pass/per-feed drift isolation), and the `sync`
  feed flags. Offline gates pass: idempotent re-run is a snapshot no-op, identical
  bytes dedup to one blob, an expired 403 is flagged missing without failing the
  run, carousels expand to child rows+blobs, an unknown `media_type` isolates that
  feed as drift. **3b shape-validated 2026-07-09** — a live schema-only probe
  confirmed own-feed `/feed/user/{id}/`, saved `/feed/saved/posts/`, collections
  `/collections/list/`, per-collection `/feed/collection/{id}/posts/`, and stories
  `/feed/user/{id}/story/` (reel==null = no active story); it caught three wrong
  guessed paths (`/feed/saved/`, `/feed/collection/{id}/`, and confirmed
  `/feed/liked/` 400s) before they shipped — saved/collection fixed to `/posts/`,
  liked handled as first-class "unavailable". `/users/{id}/info/` `media_count`
  feeds the count gate. Full binary end-to-end awaits the owner-run session
  import.)_ Gate: counts non-degenerate vs known account totals; re-run is a
  snapshot no-op.
- **Phase 4 — cache-carve source + reconciler.** Split into two gated halves so
  the phase stays honest about what is proven vs. what awaits real artifacts:
  - **4a — reconciler + format-agnostic walker (offline-provable).** The source
    reconciler (metadata plane = the completed `upsertMedia` richness/lane
    tie-break; bytes plane = `store.BackfillMediaFile`'s fill-only-empty guard),
    the pure `internal/carve` domain (a `Source` seam, CDN-basename attribution,
    magic-byte sniffing), a format-agnostic `internal/carvefs` directory walker
    (the one live carve source: a content-sniffing walk that yields media bytes
    keyed by filename), and the `internal/carvesync` orchestrator + `carve`
    command. Gate (fully offline, no live IG, no real cache): an API-expired
    (403) `media_file` is recovered from a fixture cache dir by URL-basename
    attribution; a carved stub never overwrites a richer row (and vice-versa);
    the merge is idempotent; carve never touches an already-downloaded row's
    bytes. **Recency is the artifact's capture time (file mtime), not run time**,
    so a stale cache can't out-rank a fresh fetch. Zero DDL (`SchemaVersion` stays
    3). Carve **imports no `net/http`** — a review-enforced, greppable invariant.
  - **4b — owner-artifact validation + format-specific extractors.** The
    Chromium `disk_cache`/Cache-Storage and IndexedDB extractors are **not**
    guessed at from a format spec; they ship as honest stubs (the `igmobile`
    pattern) behind `--format=chromium|indexeddb`, and are built only after the
    owner runs `carve --dry-run` against a copy of their real browser/app cache
    (the 1b/2b/3b live-probe pattern, applied to on-disk formats). Until then the
    walker is the default and only active source; `status --json` reports each
    extractor's state (`walker: active`, `chromium: stub`, …) so the tool never
    claims carve is "done" when only the format-agnostic path is proven.
- **Phase 5 — mobile API source (opt-in lane).** Built after the reconciler
  exists. Gate: enabled on a test account, mobile rows merge over web rows with
  no dupes; disabled, zero mobile headers are ever sent; the opt-in flag defaults
  off on a fresh install.
- **Phase 6 — search, incremental scheduler, release.** _(**search + status done**
  — two external-content FTS5 indexes (`dm_message_fts` over message text,
  `media_fts` over captions), kept in sync by triggers that fire inside the
  writing transaction so the index commits atomically with the rows + cursor.
  `SchemaVersion` bumped to **4**; opening a pre-search archive reads the stamped
  version first and rebuilds the (empty) indexes once. `store.Search` sanitizes
  every query through crawlkit's `FTS5TokenQuery` and binds it as a parameter
  (injection-safe); results carry only a bracketed `snippet()` + id/timestamp
  locators, never bulk content, and the query/content are never logged. The
  `search` command adds `--type dm|media|all`, `--limit`, `--json`, `--reindex`;
  `status --json` now reports real per-entity counts + last-sync time via a
  read-only open. FTS5 availability in the pure-Go `modernc.org/sqlite` driver
  was verified before building. **Deliberately fenced to post-v1:**
  `dm_user`/`account`/`collection` (people) search — usernames want infix
  matching, which is a `LIKE`+`EscapeLike` `--type people` follow-up, not FTS.
  The "scheduled run appends to JSONL history" half is `crawlctl`'s job — the
  tool's obligation is an exit-0 green sync + the now-honest `status`.)_ Gate:
  `instacrawl search` returns hits across DMs + posts; a scheduled run appends to
  JSONL history with a green status.
