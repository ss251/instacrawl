package store

import "github.com/openclaw/crawlkit/state"

// SchemaVersion is stamped into crawlkit's schema_migrations table. Bumping it
// (with matching additive DDL) is how the archive schema evolves; crawlkit
// refuses to open a DB whose stamped version is NEWER than this. All DDL is
// `create table if not exists`, so opening a v1 (Phase 1) DB simply adds the DM
// tables and re-stamps to 2 — no destructive migration, account data intact.
const SchemaVersion = 2

// Schema is the full archive schema: the account (Phase 1), crawlkit's cursor
// table (state.CursorSchema — reused for per-thread resume), and the DM tables
// (Phase 2). Every fetched table carries a `source` column (the reliability
// lane tag) so the multi-source reconciler merges web/mobile/carve later with
// no migration, and a `raw_json` column so a parser fix can re-derive rows.
const Schema = accountSchema + "\n" + state.CursorSchema + "\n" + dmSchema

const accountSchema = `
create table if not exists account (
  pk               text primary key,
  username         text not null,
  full_name        text,
  is_private       integer not null default 0,
  profile_pic_url  text,
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
