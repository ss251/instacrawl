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

### Method notes
- `ds_user_id` and `sessionid` are httpOnly (not in `document.cookie`); read them
  via CDP `Network.getCookies` when needed. The authenticated `fetch` sends them
  automatically for same-origin `instagram.com` requests.
- Required header on every call: `x-ig-app-id: 936619743392459` (web app id).
