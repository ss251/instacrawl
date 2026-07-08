package store

import "github.com/openclaw/crawlkit/state"

// SchemaVersion is stamped into crawlkit's schema_migrations table. Bumping it
// (with matching additive DDL) is how the archive schema evolves; crawlkit
// refuses to open a DB whose stamped version is NEWER than this. All DDL is
// `create table if not exists`, so opening a v1 (Phase 1) DB simply adds the DM
// tables and re-stamps to 2 — no destructive migration, account data intact.
const SchemaVersion = 3

// Schema is the full archive schema: the account (Phase 1), crawlkit's cursor
// table (state.CursorSchema — reused for per-resource resume), the DM tables
// (Phase 2), and the feed/media tables (Phase 3). Every fetched table carries a
// `source` column (the reliability lane tag) so the multi-source reconciler
// merges web/mobile/carve later with no migration, and a `raw_json` column so a
// parser fix can re-derive rows.
const Schema = accountSchema + "\n" + state.CursorSchema + "\n" + dmSchema + "\n" + feedSchema

const accountSchema = `
create table if not exists account (
  pk               text primary key,
  username         text not null,
  full_name        text,
  is_private       integer not null default 0,
  profile_pic_url  text,
  media_count      integer,
  source           text not null default 'web',
  fetched_at       text not null,
  raw_json         text
);`

const dmSchema = `
create table if not exists dm_thread (
  thread_id        text primary key,
  title            text,
  is_group         integer not null default 0,
  is_pending       integer not null default 0,
  admin_ids        text,
  last_activity_us integer,
  source           text not null default 'web',
  fetched_at       text not null,
  raw_json         text
);

create table if not exists dm_user (
  user_id          text primary key,
  username         text,
  full_name        text,
  profile_pic_url  text,
  field_count      integer not null default 0,
  source           text not null default 'web',
  fetched_at       text not null
);

create table if not exists dm_membership (
  thread_id        text not null,
  user_id          text not null,
  is_admin         integer not null default 0,
  source           text not null default 'web',
  primary key (thread_id, user_id)
);

create table if not exists dm_message (
  item_id            text primary key,
  thread_id          text not null,
  user_id            text,
  timestamp_us       integer not null,
  item_type          text not null,
  item_type_known    integer not null default 1,
  text               text,
  media_json         text,
  shared_json        text,
  link_json          text,
  reply_to_item_id   text,
  reply_resolved     integer not null default 0,
  unsent_observed    integer not null default 0,
  unsent_observed_at text,
  source             text not null default 'web',
  fetched_at         text not null,
  raw_json           text
);

create table if not exists dm_reaction (
  item_id      text not null,
  user_id      text not null,
  emoji        text not null,
  timestamp_us integer,
  source       text not null default 'web',
  primary key (item_id, user_id, emoji)
);

create table if not exists dm_thread_event (
  event_id     text primary key,
  thread_id    text not null,
  kind         text,
  actor_id     text,
  target_id    text,
  timestamp_us integer,
  source       text not null default 'web',
  fetched_at   text not null,
  raw_json     text
);

create index if not exists idx_dm_message_thread_ts on dm_message(thread_id, timestamp_us);
create index if not exists idx_dm_message_reply     on dm_message(reply_to_item_id);
create index if not exists idx_dm_thread_activity   on dm_thread(last_activity_us desc);`

// feedSchema is the Phase 3 own-content schema: a generic `media` identity table
// (upsert-by-pk = row idempotency) + `media_blob` (conflict-by-hash = byte
// idempotency) + thin junction tables recording which feed a pk appeared in, so
// a pk present in own-feed AND saved is ONE media row with two junction rows —
// not two copies. (Fable Decision 1.)
const feedSchema = `
create table if not exists media (
  pk                    text primary key,
  code                  text,
  media_type            integer not null,
  media_type_known      integer not null default 1,
  product_type          text,
  owner_id              text,
  taken_at_us           integer,
  caption_text          text,
  caption_created_at_us integer,
  field_count           integer not null default 0,
  source                text not null default 'web',
  fetched_at            text not null,
  raw_json              text
);

create table if not exists media_child (
  parent_pk text not null,
  child_pk  text not null,
  position  integer not null,
  primary key (parent_pk, position)
);

create table if not exists media_blob (
  hash         text primary key,
  byte_size    integer,
  downloaded_at text,
  source       text not null default 'web'
);

create table if not exists media_file (
  media_pk     text not null,
  kind         text not null,
  position     integer not null,
  width        integer,
  height       integer,
  original_url text,
  blob_hash    text,
  downloaded   integer not null default 0,
  missing      integer not null default 0,
  source       text not null default 'web',
  primary key (media_pk, kind, position)
);

create table if not exists own_post (
  media_pk      text primary key,
  feed_position integer,
  source        text not null default 'web',
  fetched_at    text not null
);

create table if not exists saved_item (
  media_pk      text not null,
  collection_id text not null default '',
  source        text not null default 'web',
  fetched_at    text not null,
  primary key (media_pk, collection_id)
);

create table if not exists liked_item (
  media_pk   text primary key,
  source     text not null default 'web',
  fetched_at text not null
);

create table if not exists story_item (
  media_pk       text primary key,
  expiring_at_us integer,
  taken_at_us    integer,
  source         text not null default 'web',
  fetched_at     text not null,
  raw_json       text
);

create table if not exists collection (
  collection_id  text primary key,
  name           text,
  collection_type text,
  cover_media_pk text,
  media_count    integer,
  source         text not null default 'web',
  fetched_at     text not null,
  raw_json       text
);

create index if not exists idx_media_owner        on media(owner_id);
create index if not exists idx_media_taken        on media(taken_at_us desc);
create index if not exists idx_media_file_blob     on media_file(blob_hash);
create index if not exists idx_saved_collection    on saved_item(collection_id);`
