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

**Zero-migration corollary:** because every row is `source`-tagged and merged by
richness, adding or removing a lane later touches no schema and no existing
rows. This is *why* "both" (fork B) costs no new architecture.

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
  (`sessionid` / `ds_user_id` / `csrftoken` / `mid` / `rur`) + a web UA. Nothing
  fabricated. Imported by cookie paste or browser-harness.
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
  **1b pending** — needs the owner's real session to close the live gate.)_
  Gate: a live run returns `OK(1)` for self-profile; a corrupted session returns
  `AUTH·CHECKPOINT`, not a crash.
- **Phase 2 — DM/inbox archiver (web lane, the wedge).** _(**2a done** —
  `internal/dm` (tagged-union model, lenient parsers, schema-versioned cursors,
  three-way resume, reconciler), `internal/store` DM tables v2 + transactional
  `CommitThreadPage`, `internal/dmsync` engine (inbox→thread→item with
  `PageGuard`/`Floor`/cursors, window-bounded unsend detection, pending pass,
  per-thread drift isolation), `internal/pace`, and the real `sync` command;
  offline idempotency gate test passes. **2b pending** — retune the guessed
  `direct_v2` endpoints/fields against the owner's live session.)_ Gate: two
  runs; run-2 fetches 0 new for unchanged threads (idempotent); every row
  carries `source='web'`.
- **Phase 3 — own content + saved + liked.** Gate: counts non-degenerate vs
  known account totals; re-run is a snapshot no-op.
- **Phase 4 — cache-carve source + reconciler.** Gate: an API-expired (403)
  media file present in cache is recovered; a carved stub never overwrites a
  richer API row; merge is idempotent.
- **Phase 5 — mobile API source (opt-in lane).** Built after the reconciler
  exists. Gate: enabled on a test account, mobile rows merge over web rows with
  no dupes; disabled, zero mobile headers are ever sent; the opt-in flag defaults
  off on a fresh install.
- **Phase 6 — search, incremental scheduler, release.** Gate: `instacrawl search`
  returns hits across DMs + posts; a scheduled run appends to JSONL history with
  a green status.
