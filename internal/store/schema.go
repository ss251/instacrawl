package store

// SchemaVersion is stamped into crawlkit's schema_migrations table. Bumping it
// (with a matching migration) is how the archive schema evolves; crawlkit
// refuses to open a DB whose stamped version is NEWER than this, guarding
// against an old binary opening a future archive.
const SchemaVersion = 1

// Schema is the Phase 1 archive schema: just the owner account. DM/thread/media
// tables arrive in later phases. Every table that will hold fetched rows carries
// a `source` column (the reliability lane tag) so the multi-source reconciler
// can merge web/mobile/carve rows later without a migration.
const Schema = `
create table if not exists account (
  pk               text primary key,
  username         text not null,
  full_name        text,
  is_private       integer not null default 0,
  profile_pic_url  text,
  source           text not null default 'web',
  fetched_at       text not null,
  raw_json         text
);
`
