-- Custodian ledger schema.
--
-- A current, queryable mirror of Stash's scene library, plus run history,
-- operator decisions and the mutation log. Rows are never deleted here — a
-- scene or file missing from a full sync is tombstoned (gone_run set), not
-- removed, so the mutation log always has something to point at.

PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;

CREATE TABLE IF NOT EXISTS schema_version (
  version INTEGER PRIMARY KEY
);

-- Current state of every scene Custodian has ever seen, upserted by Stash id.
CREATE TABLE IF NOT EXISTS scene (
  id          TEXT PRIMARY KEY,
  title       TEXT NOT NULL DEFAULT '',
  code        TEXT NOT NULL DEFAULT '',
  date        TEXT NOT NULL DEFAULT '',
  studio_id   TEXT NOT NULL DEFAULT '',
  studio_name TEXT NOT NULL DEFAULT '',
  organized   INTEGER NOT NULL DEFAULT 0,
  rating100   INTEGER,
  play_count  INTEGER NOT NULL DEFAULT 0,
  updated_at  TEXT NOT NULL DEFAULT '',
  seen_run    INTEGER NOT NULL,
  gone_run    INTEGER
);

CREATE INDEX IF NOT EXISTS idx_scene_gone_run ON scene(gone_run);
CREATE INDEX IF NOT EXISTS idx_scene_updated_at ON scene(updated_at);

-- Child tables below are replaced wholesale on every upsert of their scene.
CREATE TABLE IF NOT EXISTS scene_performer (
  scene_id     TEXT NOT NULL REFERENCES scene(id) ON DELETE CASCADE,
  performer_id TEXT NOT NULL,
  name         TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_scene_performer_scene ON scene_performer(scene_id);

CREATE TABLE IF NOT EXISTS scene_tag (
  scene_id TEXT NOT NULL REFERENCES scene(id) ON DELETE CASCADE,
  tag_id   TEXT NOT NULL,
  name     TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_scene_tag_scene ON scene_tag(scene_id);

CREATE TABLE IF NOT EXISTS scene_url (
  scene_id TEXT NOT NULL REFERENCES scene(id) ON DELETE CASCADE,
  url      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_scene_url_scene ON scene_url(scene_id);

CREATE TABLE IF NOT EXISTS scene_stash_id (
  scene_id TEXT NOT NULL REFERENCES scene(id) ON DELETE CASCADE,
  endpoint TEXT NOT NULL,
  stash_id TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_scene_stash_id_scene ON scene_stash_id(scene_id);

-- One row per file Stash reports, keyed by Stash's own file id. Files
-- migrate between scenes on merge, so scene_id is not part of the key.
CREATE TABLE IF NOT EXISTS file (
  id          TEXT PRIMARY KEY,
  scene_id    TEXT NOT NULL REFERENCES scene(id) ON DELETE CASCADE,
  path        TEXT NOT NULL DEFAULT '',
  basename    TEXT NOT NULL DEFAULT '',
  size        INTEGER NOT NULL DEFAULT 0,
  mod_time    TEXT NOT NULL DEFAULT '',
  width       INTEGER NOT NULL DEFAULT 0,
  height      INTEGER NOT NULL DEFAULT 0,
  duration    REAL NOT NULL DEFAULT 0,
  video_codec TEXT NOT NULL DEFAULT '',
  audio_codec TEXT NOT NULL DEFAULT '',
  frame_rate  REAL NOT NULL DEFAULT 0,
  bit_rate    INTEGER NOT NULL DEFAULT 0,
  is_primary  INTEGER NOT NULL DEFAULT 0,
  oshash      TEXT,
  md5         TEXT,
  phash       TEXT,
  seen_run    INTEGER NOT NULL,
  gone_run    INTEGER
);

CREATE INDEX IF NOT EXISTS idx_file_scene ON file(scene_id);
CREATE INDEX IF NOT EXISTS idx_file_oshash ON file(oshash);
CREATE INDEX IF NOT EXISTS idx_file_phash ON file(phash);
CREATE INDEX IF NOT EXISTS idx_file_path ON file(path);
CREATE INDEX IF NOT EXISTS idx_file_gone_run ON file(gone_run);

-- History and audit only — never the source of a listing.
CREATE TABLE IF NOT EXISTS run (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at    TEXT NOT NULL,
  finished_at   TEXT,
  kind          TEXT NOT NULL,             -- 'full' | 'incremental'
  since         TEXT,
  scenes_seen   INTEGER NOT NULL DEFAULT 0,
  files_seen    INTEGER NOT NULL DEFAULT 0,
  scenes_gone   INTEGER NOT NULL DEFAULT 0,
  stash_version TEXT NOT NULL DEFAULT '',
  notes         TEXT NOT NULL DEFAULT ''
);

-- Operator overrides, keyed by the set of file oshashes (falls back to
-- stash_id), never by scene id: scene ids die on merge and change on
-- rescan. No logic reads this yet beyond Put/Get.
CREATE TABLE IF NOT EXISTS decision (
  key        TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  value      TEXT NOT NULL,
  keeper     TEXT,
  decided_at TEXT NOT NULL,
  notes      TEXT NOT NULL DEFAULT ''
);

-- Every write Custodian made: what, when, from and to.
CREATE TABLE IF NOT EXISTS mutation_log (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id     INTEGER,
  at         TEXT NOT NULL,
  kind       TEXT NOT NULL,
  target     TEXT NOT NULL,
  from_value TEXT NOT NULL DEFAULT '',
  to_value   TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT ''
);
