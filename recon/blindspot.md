# instacrawl — Pre-Implementation Blindspot Pass

Personal, local-first archiver for the account owner's OWN Instagram data (DMs, own posts/reels/stories, saved collections, liked posts). Reuses an already-logged-in session against IG's private web/mobile API (`i.instagram.com/api/v1`). Local-only, no uploads, no telemetry. Think "Takeout for IG, run by the owner."

This document enumerates the unknown-unknowns and hard engineering realities the scoping doc MUST resolve before any code is written. Each item states **Risk → Why it bites → Decision to lock in**.

---

## 0. The one architectural fork to decide first: WEB private API vs MOBILE (`i.instagram.com`) private API

Everything downstream (headers, auth, endpoint shapes, DM access, rate behavior) depends on which of the two private surfaces you target. They are NOT the same API.

- **Web private API** (`www.instagram.com/api/v1/...` + some legacy GraphQL at `/graphql/query`): driven from a real browser session cookie. Easier auth (reuse the browser cookie jar), but `direct_v2` (DMs) coverage is thinner/flakier from web, and web has aggressive `x-ig-www-claim` / `x-web-session-id` handshakes.
- **Mobile private API** (`i.instagram.com/api/v1/...`): what the phone app calls. Richest surface — full `direct_v2` inbox, `feed`, `saved`, `liked`, story reels. Requires emulating an Android/iOS device: a stable device seed (`android_device_id`, `phone_id`, `uuid`, `advertising_id`), an app `User-Agent` string encoding app version/Android version/dpi/model, and the `X-IG-App-ID` for the app. This is the surface `instagrapi` uses.

**Decision to lock in:** Pick ONE primary surface for v1. Recommendation: **mobile private API** because DMs (the marquee feature) are far more complete there, and because a stable emulated device reduces "logged in from a new/unknown client" checkpoints vs. a headless-browser fingerprint. Document the web fallback only for endpoints the mobile path can't cover. Do NOT try to abstract over both in v1 — the header/auth/pagination models differ enough that a premature abstraction will leak.

---

## 1. Auth & session handling

### 1.1 What a logged-in session actually consists of
Risk: Treating "the session" as just a cookie. It is a **bundle** and every part must be captured together or requests silently 400/403.

The mobile private API session bundle:
- **`sessionid` cookie** — the primary bearer of auth. Also `ds_user_id`, `mid`, `ig_did`, `rur`, `csrftoken`.
- **`X-CSRFToken`** — mirrors the `csrftoken` cookie; required on POST (and some GET) or you get CSRF failures.
- **`X-IG-App-ID`** — the app identifier (web app-id `936619743392459` is the well-known web value; mobile uses its own). Wrong/absent app-id → 400.
- **Device identity headers** — `X-IG-Device-ID`, `X-IG-Android-ID` / iOS equivalent, `X-IG-Family-Device-ID`, `X-Pigeon-Session-Id`, `X-Bloks-Version-Id`. These must be **stable across the life of the session** — regenerating them per-run looks like a new device and triggers checkpoints.
- **`X-IG-WWW-Claim`** — a rotating token the server hands back in a response header (`x-ig-set-www-claim`) that you must echo on subsequent requests. Miss the echo → session degrades / 403. This is a classic silent-invalidation trap.
- **`User-Agent`** — must be internally consistent with the app-id and device headers (app version, Android/iOS version, device model, resolution, locale). Mismatched UA vs. app-id is a common ban/checkpoint trigger.
- **`Authorization: Bearer IGT:2:...`** — newer IG issues a bearer token on login (`ig-set-authorization` response header) that supplements/replaces cookie auth on some endpoints. Capture and persist it.

**Decision:** Define a `Session` value object that persists ALL of: cookie jar, csrf, www-claim (mutable, updated every response), bearer authorization (mutable), device seed (immutable per session), UA, app-id. Persist device seed so re-auth reuses the same device fingerprint.

### 1.2 How the session is obtained
Risk: Building a login flow that types the password → maximally checkpoint-prone and a credential-handling liability.

Preferred acquisition, least-risky first:
1. **Import from the user's real browser** — read cookies for `instagram.com` from the browser the user is already logged into (or have them paste a cookie export / a `sessionid`). This is the "reuse already-logged-in session" the brief calls for and avoids the app ever holding a password.
2. **Import from a device/app capture** — for the mobile surface, accept a session blob the user extracts (mirrors instagrapi's `dump_settings`/`load_settings`).
3. **Interactive username/password login** as a last-resort path — if built, it MUST handle 2FA and checkpoints (below) and MUST NOT be the default.

**Decision:** v1 default = cookie/session import. Password login is optional and gated behind explicit flags. Never log or echo the password; if a login path exists, read it from a prompt or env, never a CLI arg (shell history leak).

### 1.3 2FA / checkpoint / challenge
Risk: A challenge mid-run turns every subsequent request into a 400/checkpoint_required, and if you retry blindly you escalate to a temp ban.

Realities:
- Login or a "suspicious" request can return `challenge_required` / `checkpoint_required` with a challenge URL. Resolution requires the user to complete a code (SMS/email/authenticator) or an in-app "was this you" tap.
- 2FA login returns a `two_factor_required` payload with a `two_factor_identifier` that must be echoed with the code.
- Some checkpoints can ONLY be cleared in the official app/browser, not programmatically.

**Decision:** The tool NEVER auto-solves challenges. On any `challenge_required`/`checkpoint_required`/`two_factor_required`: **hard-stop, surface the exact status + challenge URL to the user, and instruct them to resolve it in the official app/browser, then re-run.** This is a first-class terminal state, not an error to retry. (Ties directly to the verification-gate principle: the tool must distinguish "the API told me to stop" from "I captured nothing.")

### 1.4 Session expiry & silent invalidation
Risk: The session keeps returning 200s with EMPTY or truncated data after it's been soft-invalidated (feed logic gate, www-claim drift, region rur mismatch) — the worst failure mode, because it looks like success.

**Decision:** Implement a **liveness probe** run before and periodically during a crawl: call a known endpoint with a known-nonzero expected shape (e.g. the account's own profile / `accounts/current_user`) and assert the authed user id matches the expected `ds_user_id`. If the probe returns logged-out/empty, STOP and report "session invalid" rather than writing empty archives. Also treat a sudden run of empty paginated pages where history is expected as a drift signal, not an end-of-data signal (see §2.4).

### 1.5 Secret storage
Risk: Dropping `sessionid` + cookies as plaintext JSON in the repo/cwd — a full account-takeover credential sitting in a file, easily committed to git or synced to cloud.

**Decision:**
- Store the session bundle in the **OS keychain** (macOS Keychain via `security`/`keyring`) by default. File fallback only with `chmod 600`, outside the repo, in an XDG/`~/Library` app dir, and with a big warning.
- Never write secrets into the archive output directory, logs, or error traces (redact `sessionid`, `authorization`, csrf, cookies in all logging).
- Ship a `.gitignore` that excludes the config/session dir and the archive dir. Document that `sessionid` == full account access.

---

## 2. The private API surface

### 2.1 Inbox / DMs (`direct_v2`) — the hard one
Known shape (mobile):
- `GET /api/v1/direct_v2/inbox/` — thread list, paginated by `cursor` and `seq_id`; params like `visual_message_return_type=unseen`, `thread_message_limit`, `persistentBadging`, `limit`.
- `GET /api/v1/direct_v2/threads/{thread_id}/` — one thread's messages, paginated **backwards in time** via a `cursor` (returned as `oldest_cursor`) — you page from newest toward oldest.
- Pending/requests inbox is a separate call (`direct_v2/pending_inbox/`) — easy to forget, so requests threads get silently dropped.

Risk: Assuming one inbox call returns everything. Threads paginate; within a thread messages paginate; and pending requests are a separate tree.

**Decision:** Model the crawl as: inbox pages → per-thread message pages → media within messages. Explicitly include `pending_inbox`. Persist `thread_id` + per-thread `oldest_cursor` so a thread can be resumed independently.

### 2.2 Own feed / posts / reels
- `GET /api/v1/feed/user/{user_id}/` — own timeline media, paginated by `max_id`. Reels appear here as `media_type`/`product_type` variants (`clips`).
- Stories: `GET /api/v1/feed/reels_media/` or `feed/user/{user_id}/story/` — **only returns currently-live stories** (last 24h). Historical stories are NOT retrievable via story endpoints; only the **archive** (`archive/reel/day_shells/` + `feed/reels_tray/` for own archive) exposes past stories, and only if the user has story archiving enabled.

Risk: Believing you can back-fill all historical stories. You cannot — expired, non-archived stories are gone server-side.

**Decision:** Scope stories as "capture currently-live + whatever is in the user's own Story Archive." State plainly in docs that stories not saved to archive and already expired are unrecoverable. Prioritize story capture speed (see §4.2).

### 2.3 Saved & liked
- Saved: `GET /api/v1/feed/saved/` (all saved) and `GET /api/v1/feed/collection/{collection_id}/` per collection; collections list via `GET /api/v1/collections/list/`. Paginated by `max_id`.
- Liked: `GET /api/v1/feed/liked/` — paginated by `max_id`. Note IG has restricted/removed the liked-feed at times; treat its availability as uncertain and detect absence rather than assume.

**Decision:** Enumerate collections first, then page each; treat `liked` as best-effort with explicit "endpoint unavailable" reporting if it 4xxs.

### 2.4 Pagination / cursor model
Risk: Cursor semantics differ per endpoint (`max_id` for feeds, `cursor`/`oldest_cursor` for direct, `next_max_id` in body, `more_available` boolean). Mixing them = infinite loops or premature stop. And a **soft-blocked** session returns `more_available:false` or empty pages early, which the naive loop reads as "done."

**Decision:** Per-endpoint cursor adapters. Loop terminates ONLY on the endpoint's explicit end signal (`more_available:false` / missing `next_max_id`). Additionally record page count + item count per resource so a run that ends "too early" vs. a prior run is flagged (drift/soft-block detection), not silently accepted.

### 2.5 GraphQL vs REST
Risk: Copying web-GraphQL (`query_hash`/doc_id) recipes into a mobile tool. IG has been deprecating anonymous GraphQL `query_hash` endpoints and moving to authenticated `doc_id` POST GraphQL; the private mobile REST (`/api/v1/...`) is more stable for an authed owner.

**Decision:** Standardize on private mobile REST (`i.instagram.com/api/v1`). Avoid GraphQL unless an endpoint has no REST equivalent; if used, isolate it behind an adapter with its own drift test.

### 2.6 Endpoint churn & breakage detection (verification gate)
Risk: IG changes response shapes, header requirements, or retires endpoints with no notice. A resilient archiver must **fail loud**, never capture-nothing-silently.

**Decision — this is a core design principle, not a nicety:**
- Every endpoint has a **response schema contract** (required keys, expected types). A 200 whose body violates the contract is treated as **breakage**, surfaced, and halts that resource — NOT written as "empty."
- Distinguish four terminal states explicitly and report them: `OK(n items)`, `AUTH/CHECKPOINT (stop)`, `RATE-LIMITED (backoff/stop)`, `SCHEMA-DRIFT/BREAKAGE (stop + dump raw body for triage)`.
- A run summary MUST assert non-zero captures where history is known to exist, and refuse to overwrite a good prior archive with a suspiciously-empty new one.

---

## 3. Reliability / politeness realities

Risk: Behaving like a scraper (bursts, parallelism, fresh device each run) → rate limits, temporary action blocks, or account flagging. For a personal tool the user's OWN account is at stake — the safety bar is high.

Realities:
- IG rate-limits per endpoint and globally; 429 and "feedback_required"/"please wait a few minutes" are the signals. `direct_v2` is especially sensitive.
- Parallel requests from one account look non-human.
- A fresh device fingerprint each run reads as a login-from-new-device.

**Decisions to lock in (conservative defaults):**
- **Single session, single-threaded, serialized requests.** No concurrency against the API in v1. (Media *downloads* from CDN can be lightly parallel — different host, see §4.3.)
- **Human-like pacing:** randomized jittered delays between calls (e.g. multi-second, not fixed), longer pauses between resources. Make the rate a config with a conservative default and document that lowering it risks the account.
- **Backoff:** exponential backoff with jitter on 429/`feedback_required`; on repeated rate-limit or ANY checkpoint, **stop the run** rather than grind (grinding through a block is what escalates to a ban).
- **Stable device seed** persisted across runs.
- **Resume-after-interrupt:** persist cursors per resource + a run manifest so a killed/failed run resumes where it left off instead of restarting (restarting = double the traffic).
- **Idempotent re-runs / dedup:** content addressed by stable IDs (`item_id`/`message_id`/`media_pk`/`thread_id`), media by a content hash. Re-running skips already-captured items; only fetches the delta. This is also what makes incremental daily backups cheap.

---

## 4. Media

### 4.1 What media appears where
DM media is the messiest: photos, videos, **voice messages** (audio), animated stickers/GIFs, **shared posts/reels/profiles** (a message whose payload is another user's media), link previews, "media_share", "clip", "felix_share" (IGTV), story replies (a reply referencing a story that may already be expired). Each `item_type` in `direct_v2` has a different media locator.

Risk: Writing a photo-only extractor and silently dropping voice notes, shared reels, and story-reply context.

**Decision:** Enumerate every `direct_v2` `item_type` seen and handle (or explicitly log-as-unhandled) each. Never silently skip an unknown item_type — count and report it (schema-drift signal).

### 4.2 Story expiry — 24h, capture fast
Risk: Stories vanish in 24h. A backup run scheduled daily can miss stories entirely.

**Decision:** Treat live stories as **highest-priority, time-critical** capture. Document that reliable story archiving requires frequent runs (or enabling IG's native story archive). A once-a-week run WILL miss most stories — say so plainly.

### 4.3 Expiring CDN URLs — must download promptly
Risk: IG media URLs (`scontent.cdninstagram.com` / `fbcdn.net`) are **signed and expire** (embedded expiry + hash). Storing the URL and downloading later = dead links. This is the single most common archiver data-loss bug.

**Decision:** **Download bytes immediately** during the crawl, in the same pass that fetches the metadata — never persist a CDN URL as the archive-of-record. Store the local file path; keep the original URL only as provenance metadata (known-dead). Verify each download (content-length / hash) before marking the item captured.

### 4.4 Sizes, quality, versions
Risk: IG returns multiple `image_versions2.candidates` / `video_versions` at different resolutions; grabbing the first/thumbnail loses quality.

**Decision:** Select the **highest-resolution** candidate deterministically (max width×height). Record which version was chosen.

### 4.5 Storage footprint
Risk: No estimate → surprise disk-fill mid-run (a full DM+posts history with video can be many GB to tens of GB).

**Decision:** Provide a **dry-run/estimate mode** that counts items and sums declared media sizes before downloading; check free disk space before and during; stream large videos to disk (no full-in-memory buffering). Document rough footprint math (e.g. reels/video dominate).

### 4.6 On-disk layout
**Decision to lock in a stable, human-browsable, resumable layout**, e.g.:
```
<archive_root>/
  manifest.json                     # run history, cursors, versions, counts
  account/<user_id>/profile.json
  dms/<thread_id>/thread.json       # messages + reactions + metadata
  dms/<thread_id>/media/<message_id>.<ext>
  posts/<media_pk>/media.json + files
  stories/<yyyy-mm-dd>/<pk>/...
  saved/<collection>/<media_pk>/...
  liked/<media_pk>/...
  _raw/<endpoint>/<cursor>.json     # optional raw-response capture for re-parse
```
Store **raw JSON responses** alongside normalized data so schema-drift fixes can re-parse historical raw captures without re-hitting the API. IDs in paths → idempotent, git-diffable, resumable.

---

## 5. Data model gotchas

- **Mixed content types in a thread** (§4.1) — model messages as a tagged union over `item_type`, not a flat text field.
- **Replies** — a message can reference a parent (`replied_to_message`); the parent may be older than your current page or already unsent. Store the reference even if the parent isn't (yet) captured; resolve on a second pass.
- **Reactions** (likes/emoji on messages) — live in `reactions.likes`/`reactions.emojis` on the item; easy to miss. Capture them.
- **Unsends (server-side delete)** — a message the sender deleted is simply ABSENT from future fetches; IG sends a realtime `unsend` event you won't see via REST polling. **Policy question the doc must answer:** does the archive KEEP a message captured before it was unsent? For a personal backup the sane answer is **yes, keep it and mark `unsent_observed` if a later run notices its disappearance** — but this must be a stated, deliberate policy, and it means the archive can contain messages the sender later retracted (own-account, personal-use framing makes this acceptable; document it).
- **Group vs 1:1** — threads have `is_group`, multiple participants, admins, thread renames, add/remove events. Model participants as a list; capture thread events, not just text.
- **Disappearing / vanish-mode messages** — ephemeral by design; may not be fetchable via normal thread history at all, or only while unseen. Treat as best-effort; do not promise capture.
- **Non-user participants** — accounts that are deactivated/blocked/deleted show as partial/anonymized participant stubs; handle missing usernames/pfps gracefully.
- **Shared posts/reels-in-DM** — the shared media has its own CDN URLs that also expire (§4.3) and belongs to another user; download for the owner's personal archive but note provenance. (Ties to §6: don't redistribute.)
- **Message ordering / clocks** — use `timestamp` (µs) for ordering, but be aware of server-clock vs. `item_id` ordering mismatches; dedup on `item_id`.

**Decision:** Lock the message data model as a tagged union with explicit fields for reply-ref, reactions, sender, timestamp, item_type, media-ref, and lifecycle flags (`unsent_observed`, `ephemeral`). Second-pass reference resolution. State the unsend-retention policy explicitly.

---

## 6. Legal / ToS honest framing (README)

Risk: Overclaiming legality, or (worse) advising ToS evasion. Also: silently enabling capture of OTHER people's content (the other side of a DM).

**Decision — README must state plainly, without advising evasion:**
- **ToS reality:** Automated access to Instagram via its private/undocumented API is **against Instagram's Terms of Use**, regardless of intent. Using this tool may put your account at risk of rate-limiting, temporary blocks, or suspension. There is no "safe" guarantee.
- **Owner-only scope:** This tool is for the **account owner archiving their OWN data**, analogous to a personal data export. It is not for scraping others, bulk collection, surveillance, or accessing accounts you don't control.
- **Personal use, no redistribution:** DMs and shared media necessarily include content authored by **other people** (the other side of a conversation). Keep it private; do not publish, redistribute, or repurpose others' content. Respect their privacy.
- **No evasion advice:** The tool defaults to conservative, single-session, human-paced access and stops on any block. It does **not** implement, and the docs do **not** advise, ban-evasion, proxy rotation, multi-account, or CAPTCHA/challenge-solving. Prefer IG's official **"Download Your Information"** export where it suffices; this tool exists for what that export omits or delivers too slowly (e.g. media at quality, fast story capture, structured DMs).
- **Account-risk disclaimer + no warranty:** Use at your own risk; the authors are not liable for account actions taken by Instagram. Consider the official export first.
- **Data handling:** Local-only, no uploads, no telemetry — and say how secrets are stored (§1.5).

---

## 7. Testing a tool that hits a live private API

Risk: Tests that hit the real API → hammer the account, flaky, and can trigger the very blocks the tool avoids. And no way to catch schema drift before it silently breaks captures in the field.

**Decisions:**
- **Fixture/replay architecture:** All parsing/normalization/pagination logic runs against **recorded fixtures** (sanitized real JSON responses with secrets/PII scrubbed) — never the live API. Use a VCR-style record/replay layer (record once against a throwaway/real session with explicit consent, then replay in CI). CI hits the network **never**.
- **Golden fixtures per endpoint** covering: normal page, last page (`more_available:false`), empty inbox, a thread with every `item_type`, a rate-limited response, a `checkpoint_required` response, and a schema-drift/malformed response. Each drives a test asserting the correct terminal state (§2.6).
- **Schema-drift detection as a first-class test + runtime check:** the response-contract validators (§2.6) are unit-tested against fixtures AND run in production; a `--diagnose` mode can hit ONE page of each endpoint (rate-limited, opt-in) and diff the live shape against the contract to catch drift early without a full crawl.
- **Pagination-loop tests** with fixtures that would trip infinite loops / premature stops.
- **Media-download tests** against a local fixture server, including an expired-URL (403) case to prove the "download-immediately, verify, fail-loud" path.
- **No secrets in fixtures or test env;** scrub `sessionid`/cookies/UA-identifying/PII from all recorded fixtures at record time.

---

## Cross-cutting principles the whole design must honor

1. **Fail loud, never capture-nothing-silently.** Every resource ends in one of four explicit states (OK / auth-stop / rate-stop / drift-stop). A 200 is not success; a contract-valid, count-sane 200 is.
2. **Download media bytes in the fetch pass** — CDN URLs are expiring artifacts, not storage.
3. **One human, one session, conservative pace, stop on any block.** The user's own account is the asset at risk.
4. **Idempotent, resumable, incremental** via persisted cursors + ID/hash dedup + raw-response retention.
5. **Owner-only, personal, non-redistributed, ToS-honest** — stated plainly, no evasion.
