# Live validation log

The loop validates guessed endpoints/shapes against a logged-in session using a
**schema-only** browser probe (via browser-harness / CDP `Runtime.evaluate` with
an authenticated same-origin `fetch`). It reads response **structure** (key names
+ types), never message content, and needs no credential handling — so it isn't
gated by the safety classifier (programmatic session *extraction* to run the Go
binary end-to-end is gated, and is left to the owner).

## 2026-07-09 — Phase 1 & 2 confirmed, Phase 3 pre-validated

### Phase 1 (1b) — profile
- `GET /api/v1/users/{ds_user_id}/info/` → `200 {user:{pk, username, full_name,
  is_private, profile_pic_url}, status}`. Matches `ig/classify.go` exactly. ✓

### Phase 2 (2b) — direct messages
- `GET /api/v1/direct_v2/inbox/` → `200 {seq_id, inbox:{threads[], has_older,
  oldest_cursor, prev_cursor, next_cursor, unseen_count, ...}, ...}`. The parser
  uses `seq_id` + `inbox.threads` + `inbox.has_older`/`inbox.oldest_cursor`. ✓
- `GET /api/v1/direct_v2/threads/{id}/` → `200 {thread:{...}, status}`. Thread
  carries `thread_id, thread_title, is_group, users[], admin_user_ids, items[],
  has_older, oldest_cursor, newest_cursor, last_activity_at`. ✓
- Item → `{item_id, message_id, user_id, timestamp, item_type, text, ...}`. The
  sampled thread was text-only, so the media/voice/share/reaction
  (`reactions.likes`/`reactions.emojis`) and `replied_to_message` sub-fields are
  **not yet confirmed** — they need a media-bearing sample. Parser handles their
  absence leniently.
- User (in `thread.users[]`) → `{pk, username, full_name, profile_pic_url}`. ✓

### Phase 3 (feed) — pre-validation for the next build
- `GET /api/v1/feed/user/{ds_user_id}/` → `200 {items[], next_max_id,
  more_available, num_results, ...}`. Own posts/reels; paginate by `max_id`. ✓
- `GET /api/v1/collections/list/` → `200 {items:[{collection_id, collection_name,
  collection_type, cover_media, collection_media_count, ...}], next_max_id,
  more_available}`. Then `feed/collection/{id}/` per collection. ✓
- `GET /api/v1/feed/reels_tray/` → `200 {tray:[{id, user, latest_reel_media,
  reel_type, expiring_at, seen, ...}], ...}`. Live stories only (24h). ✓
- `GET /api/v1/feed/saved/` → **400**. The guessed saved path is wrong; a Phase 3
  follow-up probe must find the working path (candidates:
  `feed/saved/posts/`, `feed/saved/all_media_feed/`).
- `GET /api/v1/feed/liked/` → **400**. Liked feed is restricted/unavailable —
  exactly the blindspot prediction. Phase 3 treats liked as best-effort and
  reports "endpoint unavailable" rather than assuming empty.

### Phase 3 (3b) — feed endpoints resolved 2026-07-09 (built to these)
- Own posts/reels `GET /api/v1/feed/user/{id}/` → 200 `{items:[media], num_results,
  more_available, next_max_id}`. media = `{pk, code, media_type(1/2/8),
  product_type, taken_at, caption, image_versions2.candidates[], video_versions[],
  carousel_media[]}`. ✓
- Saved `GET /api/v1/feed/saved/posts/` → 200 `{items:[{media}], next_max_id}`. ✓
  (`/feed/saved/` 400s; `/feed/saved/all_media_feed/` 404s.)
- Collections `GET /api/v1/collections/list/` → 200 `{items:[{collection_id,
  collection_name, collection_type, cover_media, collection_media_count}], next_max_id}`. ✓
- **Collection feed `GET /api/v1/feed/collection/{id}/posts/`** → 200
  `{items:[{media}], next_max_id}`. The bare `/feed/collection/{id}/` **400s** —
  caught in 3b and fixed to `/posts/` (mirrors saved). ✓
- Own stories `GET /api/v1/feed/user/{id}/story/` → 200 `{reel: {items:[media]}|null,
  broadcast, status}`. reel==null when no active story (a normal empty outcome,
  not drift). ✓
- Liked `GET /api/v1/feed/liked/` → **400** (restricted) — best-effort, first-class
  "unavailable", never drift.
- `/users/{id}/info/` carries `media_count` (the own-post total for the Phase 3
  count gate).

### Phase 4 (carve) — offline-provable; live artifacts deferred to 4b
Phase 4a needs **no live IG and no real cache**: the carve lane reads local disk,
so its full gate is proved offline (the three-clause postcondition — recover an
expired 403 from a fixture cache dir, never clobber a downloaded row, idempotent
re-run — plus the SQL-vs-Go reconciler cross-check over every source × richness ×
recency pair). Nothing here rides a schema-only browser probe because there is no
network call to probe.

What **is** deferred to 4b (the on-disk-format equivalent of the 1b/2b/3b live
probe): the Chromium `disk_cache` / Cache-Storage and IndexedDB extractors. Their
formats are version-specific and are NOT guessed at — the owner runs
`instacrawl carve --dry-run --dir <copy-of-real-cache>` (browser closed) and the
dry-run reports files walked, media sniffed, and canonical-key matches vs the
undownloaded set. If the format-agnostic walker already attributes a useful
fraction, the extractor may never be built; if it attributes ~0, sanitized real
entries (owner's own media only) become `carvefs/testdata` and the extractor is
built against them. Until then `--format=chromium|indexeddb` refuse loudly and
the walker is the only active source.

### 2026-07-09 — FULL LIVE END-TO-END (not just schema probes)
The web lane ran against live Instagram for real (session imported via
`--from-browser chrome` → keychain; owner @sailesh1618): **auth OK → DM sync
archived 80 threads / 2,547 messages → search found them** (incl. the thread
with Eric Manganaro, 61 messages). Three real bugs surfaced and were fixed —
each caught by the reliability spine, never by silent corruption:

1. **Missing `datr` cookie.** The self-profile endpoint accepted a 6-cookie
   bundle, but `/direct_v2/inbox/` returned an HTML login wall. The browser's
   full instagram.com jar carries `datr` (httpOnly, Meta browser-identity) which
   the DM endpoint requires and the lenient profile endpoint does not. Fix: the
   web bundle now carries the WHOLE instagram.com cookie jar (`WebBundle.Extra`),
   not a hand-picked six — the web lane is "the real browser cookie jar."
2. **Wrong pending-inbox path.** `/api/v1/direct_v2/pending_inbox/` and
   `/spam_inbox/` **404 on web** (mobile-API paths). The web serves message
   requests from the same `/inbox/` path with **`?folder=pending`**. The 404
   HTML page's `not-logged-in` class made the gate read it as AUTH·CHECKPOINT and
   halt the whole run. Fix: correct path + the pending walk is now best-effort
   (an optional sub-resource can't halt the main archive).
3. **Unknown DM item types.** Real inboxes are full of `clip` (shared Reels —
   the 2nd most common type after `text`) and `video_call_event`, neither in
   `KnownItemTypes`, so nearly every thread drifted. Fix: added both (clip →
   shared-media payload, video_call_event → thread event). Also completed the
   spine's §4B intent: an unknown item_type now **commits the page leniently**
   (each unknown item stored `item_type_known=0` + `raw_json`) and flags the
   thread, instead of dropping the page — so nothing is lost and re-parsing after
   adding a type recovers it without re-fetching.

### Method notes
- `ds_user_id` and `sessionid` are httpOnly (not in `document.cookie`); read them
  via CDP `Network.getCookies` when needed. The authenticated `fetch` sends them
  automatically for same-origin `instagram.com` requests.
- Required header on every call: `x-ig-app-id: 936619743392459` (web app id).
