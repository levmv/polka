-- Core library model:
--
--   works  = logical books as the user sees them in the library, including the
--            publication metadata (publisher, date, language, identifiers).
--   assets = concrete files on disk for a work.
--
-- A work may have multiple assets (for example EPUB and PDF files for the same
-- book); the filesystem reflects the asset level, each with its own asset_id and
-- storage_path. SQLite is the source of truth; storage_path is updated only
-- after the file physically exists at the new path.
--
-- Publication metadata lives directly on the work. We deliberately do NOT model
-- separate "editions" per work: in practice a work is always one publication,
-- and the layer only added indirection to every query. Different translations or
-- reissues are treated as different works (consistent with "different author
-- spellings are different authors"). If grouping related editions is ever
-- wanted, it is cheaper to add a grouping field/table then than to carry an
-- unused 1:N split now.

CREATE TABLE works (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    sort_title TEXT NOT NULL,
    -- Denormalized primary author sort key for large-library list sorting.
    -- The author relation remains authoritative; write paths refresh this
    -- column after changing work_authors or author sort_name.
    primary_author_sort TEXT NOT NULL DEFAULT '',
    series TEXT,
    series_index REAL,
    description TEXT,
    tags TEXT,
    publisher TEXT,
    published_date TEXT,
    language TEXT,
    identifiers TEXT,
    manual_overrides TEXT,
    -- Monotonic revision of user-owned work metadata. Imported files start at
    -- rev 0 and are considered born clean; edits bump this so write-back can
    -- tell which managed files need their embedded metadata refreshed.
    metadata_rev INTEGER NOT NULL DEFAULT 0,
    -- Dual-purpose cover token: 0 means the work has no stored original cover;
    -- >0 means a cover exists and the number is the browser cache-buster
    -- generation. It is deliberately not a metadata/write-back revision.
    cover_version INTEGER NOT NULL DEFAULT 0,
    -- created_at records when Polka created this row. added_at preserves the
    -- book's earlier collection chronology when import metadata or source file
    -- times provide it; otherwise both begin at the current time.
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    added_at INTEGER NOT NULL DEFAULT (unixepoch()),
    -- Soft-delete (trash). deleted_at NULL means the work is live; a non-NULL
    -- timestamp drops it from every normal projection (browse/search/cleanup/
    -- shelves) while the row and files stay until an admin purge. deleted_by
    -- records who trashed it, for a legible "Deleted by X — Restore?" trash view.
    deleted_at INTEGER,
    deleted_by TEXT REFERENCES users(id) ON DELETE SET NULL
);

-- Trash listing: only the soft-deleted works, newest-trashed first.
CREATE INDEX idx_works_deleted_at ON works(deleted_at) WHERE deleted_at IS NOT NULL;

-- Live-list indexes for the default browse sorts: author/title, newest-added,
-- title, publication date, and series order. All are partial on live works so
-- Trash does not bloat the hot browse paths.
CREATE INDEX idx_works_live_author_sort ON works(primary_author_sort, sort_title COLLATE NOCASE, title COLLATE NOCASE, id) WHERE deleted_at IS NULL;
CREATE INDEX idx_works_live_added ON works(added_at DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX idx_works_live_title ON works(sort_title COLLATE NOCASE, title COLLATE NOCASE, id) WHERE deleted_at IS NULL;
CREATE INDEX idx_works_live_pubdate ON works(published_date DESC, added_at DESC, id) WHERE deleted_at IS NULL;
CREATE INDEX idx_works_live_series_order ON works(
    series,
    CASE WHEN series_index IS NOT NULL AND series_index > 0 THEN 0 ELSE 1 END,
    CASE WHEN series_index IS NOT NULL AND series_index > 0 THEN series_index ELSE 0 END,
    title COLLATE NOCASE,
    id
) WHERE deleted_at IS NULL;

-- One row per file stored under the managed books/ tree. storage_path is
-- relative to the managed storage root and should always name the current
-- DB-known location of this exact asset. is_primary marks the default concrete
-- file for workflows that need one asset per work (reader, conversion, cover
-- extraction). Download routes address assets by id, not by path, so
-- relayout can move files without invalidating stable links.
CREATE TABLE assets (
    id TEXT PRIMARY KEY,
    work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    storage_path TEXT NOT NULL,
    filename TEXT NOT NULL,
    original_filename TEXT NOT NULL DEFAULT '',
    extension TEXT NOT NULL,
    -- Stable detected logical format key (for example epub, fb2, azw4,
    -- markdown). Unlike extension, this reflects content detection at import
    -- time and drives reader/conversion capability decisions.
    format TEXT NOT NULL DEFAULT 'unknown',
    is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
    -- Persisted reader capability for the current asset bytes. List/detail APIs
    -- read this directly; expensive container validation happens at import time.
    -- `polka check` reports stale can_read/format state caused by changed or
    -- corrupt current bytes, and `polka repair` recomputes it only when the
    -- current bytes are trusted by hash checks. This is not a pre-release
    -- compatibility/backfill path for old dev databases.
    can_read INTEGER NOT NULL DEFAULT 0 CHECK (can_read IN (0, 1)),
    -- original_sha256 is the hash of the bytes first imported. It never changes,
    -- even if polka later rewrites metadata into the managed file. current_sha256
    -- tracks the bytes currently on disk and changes after such rewrites.
    original_sha256 TEXT,
    current_sha256 TEXT,
    -- Sizes mirror the original/current byte identities and let UI/check paths
    -- avoid stat/hash work when SQLite already knows the served asset size.
    original_size INTEGER,
    current_size INTEGER,
    -- Lazy read-model over the exact bytes served through /download/{asset_id}
    -- for KOReader sync. The file remains authoritative; this can be
    -- recomputed whenever bytes change. KOReader's sampled hash is not unique:
    -- more than one asset may legitimately share it, and live-catalog mapping
    -- may proceed only when every live match belongs to one work.
    koreader_hash TEXT,
    -- Work metadata revision last embedded into this file. Non-writable formats
    -- keep the default; writable assets are dirty when writeback_rev is behind
    -- works.metadata_rev. writeback_error records the last failed attempt.
    writeback_rev INTEGER NOT NULL DEFAULT 0,
    writeback_error TEXT,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE UNIQUE INDEX idx_assets_original_sha256 ON assets(original_sha256);
CREATE INDEX idx_assets_current_sha256 ON assets(current_sha256);
CREATE INDEX idx_assets_koreader_hash ON assets(koreader_hash) WHERE koreader_hash IS NOT NULL AND koreader_hash <> '';
-- Keep this predicate in sync with format.MetadataWritebackFormatKeys().
CREATE INDEX idx_assets_writeback_dirty ON assets(work_id, writeback_rev) WHERE format IN ('epub', 'fb2', 'kepub');
CREATE INDEX idx_assets_work_id ON assets(work_id);
CREATE UNIQUE INDEX idx_assets_storage_path ON assets(storage_path);
CREATE UNIQUE INDEX idx_assets_one_primary_per_work ON assets(work_id) WHERE is_primary = 1;

-- One row per in-flight physical metadata write-back replacement. Dirty work
-- is still derived from assets.writeback_rev < works.metadata_rev; this table
-- only makes the overwrite temp/rename window inspectable and repairable.
CREATE TABLE metadata_writeback_attempts (
    asset_id TEXT PRIMARY KEY REFERENCES assets(id) ON DELETE CASCADE,
    metadata_rev INTEGER NOT NULL,
    storage_path TEXT NOT NULL,
    temp_path TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    size INTEGER NOT NULL,
    koreader_hash TEXT,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Application-wide settings that belong to this library database, not to one
-- user. books_root is resolved relative to the data directory when not absolute;
-- the default books folder is "books". book_path_template is the managed
-- book-file layout policy; metadata_writeback is the write-back mode.
CREATE TABLE app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- One cross-process writer lease for storage-mutating maintenance. It is a
-- heartbeat row rather than a filesystem lock so it still works on network
-- filesystems where flock semantics are unreliable.
CREATE TABLE writer_leases (
    name       TEXT PRIMARY KEY,
    owner      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

-- Author identity is intentionally simple for now: an exact display name string
-- plus a persistent sort_name. sort_name selects the canonical-path bucket and
-- author folder, so changing it relayouts affected primary-author files.
CREATE TABLE authors (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    sort_name TEXT NOT NULL
);

CREATE INDEX idx_authors_name ON authors(name);

-- author_order defines the display order and primary author. The first author
-- is also the author used by canonical path construction.
CREATE TABLE work_authors (
    work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    author_id TEXT NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    role TEXT,
    author_order INTEGER DEFAULT 0,
    PRIMARY KEY (work_id, author_id)
);

CREATE INDEX idx_work_authors_author_id ON work_authors(author_id);

-- Authentication identity. The library content (works/assets/authors) is shared
-- across all users; only per-user state (shelf ownership, future reading
-- progress and preferences) references users(id). The single-user case is simply
-- one row with role='admin' — there is no separate "no users" mode once the
-- server has been bootstrapped (the first admin is created via env/flag on first
-- serve, the `polka user` command, or the one-time first-run setup page).
--
-- Passwords are stored as a bcrypt hash, never in clear text. Usernames are
-- compared case-insensitively (stored lowercased + a unique index).
CREATE TABLE users (
    id            TEXT PRIMARY KEY,             -- u_...
    username      TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'reader' CHECK (role IN ('admin', 'member', 'reader')),
    -- content_scope='all' sees the whole live catalog. 'shelves' sees only the
    -- union of user_scope_shelves; an empty/missing assignment is fail-closed.
    content_scope TEXT NOT NULL DEFAULT 'all' CHECK (content_scope IN ('all', 'shelves')),
    created_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at    INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE UNIQUE INDEX idx_users_username ON users(username);

-- Browser sessions are persistent across server restarts, but remain small and
-- boring: the cookie carries a random token while SQLite stores only its
-- SHA-256 hash. Multi-device use is allowed; logout removes one session, and
-- password/admin user actions can revoke a user's other sessions.
CREATE TABLE sessions (
    token_hash   TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);

CREATE INDEX idx_sessions_user_id ON sessions(user_id);

-- Long-lived per-device credentials ("app passwords") for non-interactive
-- clients: OPDS readers and KOReader sync. Like sessions, only the
-- SHA-256 hash of the token is stored, so a DB dump holds no usable credential.
-- Unlike sessions there is no expiry — a device token lives until explicitly
-- revoked. Tokens are accepted only on the delivery surface (OPDS / download /
-- covers, and KOReader's own progress state), never on catalog-mutation or
-- admin APIs; that scope is enforced by the auth middleware's path segmentation.
-- name is unique per user so the CLI/UI can revoke by name.
CREATE TABLE app_tokens (
    id           TEXT PRIMARY KEY,             -- t_...
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    last_used_at INTEGER,
    UNIQUE (user_id, name)
);

-- Sidebar shelves. One entity covers both manual lists and saved searches:
-- kind='manual' has explicit shelf_books rows; kind='query' stores the same
-- search string accepted by /api/books?q=... and has no membership rows.
-- query_match is non-empty only when the complete query is an FTS expression
-- safe to reuse as a reader access boundary. Smart shelves with no:/status:
-- filters keep their raw query but an empty query_match.
-- owner_id is the account that owns the shelf. visibility controls whether the
-- shelf is personal to that owner or shared with the household.
CREATE TABLE shelves (
    id         TEXT PRIMARY KEY, -- s_...
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('manual', 'query')),
    query      TEXT,
    query_match TEXT,
    owner_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    visibility TEXT NOT NULL DEFAULT 'personal' CHECK (visibility IN ('personal', 'shared')),
    position   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    CHECK (
        (kind = 'manual' AND query IS NULL AND query_match IS NULL)
        OR
        (kind = 'query' AND query IS NOT NULL AND query_match IS NOT NULL)
    )
);

CREATE INDEX idx_shelves_visibility_position ON shelves(visibility, position);
CREATE INDEX idx_shelves_owner_visibility_position ON shelves(owner_id, visibility, position);

CREATE TABLE shelf_books (
    shelf_id TEXT NOT NULL REFERENCES shelves(id) ON DELETE CASCADE,
    work_id  TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    position INTEGER NOT NULL DEFAULT 0,
    added_at INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (shelf_id, work_id)
);

CREATE INDEX idx_shelf_books_work_id ON shelf_books(work_id);

-- One native Kobo connection per account. Its raw URL token is shown exactly
-- once; only the SHA-256 hash remains in SQLite. Replacing the connection is the
-- deliberately simple way to change shelves or revoke every device using the
-- old URL.
CREATE TABLE kobo_connections (
    id           TEXT PRIMARY KEY, -- kc_...
    user_id      TEXT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    shelf_id     TEXT NOT NULL REFERENCES shelves(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    revision     INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    last_used_at INTEGER
);

CREATE INDEX idx_kobo_connections_shelf_id ON kobo_connections(shelf_id);

-- Durable latest-value change feed for one Kobo connection. asset_id/work_id
-- intentionally are not foreign keys: a removed/purged book must leave a
-- tombstone until an offline device asks for revisions it missed. A present row
-- is always revalidated against live assets before metadata or bytes are served.
CREATE TABLE kobo_items (
    connection_id TEXT NOT NULL REFERENCES kobo_connections(id) ON DELETE CASCADE,
    asset_id       TEXT NOT NULL,
    work_id        TEXT NOT NULL,
    fingerprint    TEXT NOT NULL,
    present        INTEGER NOT NULL CHECK (present IN (0, 1)),
    revision       INTEGER NOT NULL CHECK (revision > 0),
    first_revision INTEGER NOT NULL CHECK (first_revision > 0),
    updated_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (connection_id, asset_id),
    UNIQUE (connection_id, revision)
);

-- Cleanup duplicate dismissals are detector bookkeeping, not book metadata. A
-- group is hidden only while its current live member set is covered by one of
-- these rows for the same detector reason/key; metadata edits and newly imported
-- copies naturally surface it again.
CREATE TABLE duplicate_dismissals (
    id           TEXT PRIMARY KEY,
    reason       TEXT NOT NULL,
    detector_key TEXT NOT NULL,
    work_ids     TEXT NOT NULL,
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    created_by   TEXT REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX idx_duplicate_dismissals_key ON duplicate_dismissals(reason, detector_key);

CREATE TABLE user_scope_shelves (
    user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    shelf_id TEXT NOT NULL REFERENCES shelves(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, shelf_id)
);

CREATE INDEX idx_user_scope_shelves_shelf_id ON user_scope_shelves(shelf_id);

-- General per-user UI/application preferences. Reader-engine-specific settings
-- stay in user_reader_preferences below.
CREATE TABLE user_settings (
    user_id               TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    theme                 TEXT NOT NULL DEFAULT 'system' CHECK (theme IN ('system', 'light', 'dark', 'sepia')),
    hide_continue_reading INTEGER NOT NULL DEFAULT 0 CHECK (hide_continue_reading IN (0, 1)),
    updated_at            INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Per-user reader state. Position is stored per concrete asset because EPUB/PDF
-- variants of the same work have independent locators. `locator` is a JSON
-- object opaque to SQLite (CFI/page/resource position/etc. belongs to the reader
-- engine); progress is a coarse normalized 0..1 value for browse/UI summaries.
CREATE TABLE user_asset_state (
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    asset_id     TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    progress     REAL NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 1),
    locator      TEXT NOT NULL DEFAULT '{}',
    last_read_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (user_id, asset_id)
);

CREATE INDEX idx_user_asset_state_last_read ON user_asset_state(user_id, last_read_at DESC);

-- A person's reading status belongs to the logical work, while the concrete
-- reader position above belongs to an asset. Keeping the current value in its
-- own small table makes status: searches cheap; the append-only event stream
-- below retains completion dates and repeat reads for future history views.
-- No row means unread, so importing a large library creates no per-user churn.
CREATE TABLE user_work_reading_state (
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    work_id      TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    status       TEXT NOT NULL CHECK (status IN ('unread', 'reading', 'finished', 'dropped')),
    last_event_id TEXT,
    updated_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (user_id, work_id)
);

CREATE INDEX idx_user_work_reading_state_status ON user_work_reading_state(user_id, status, updated_at DESC);

CREATE TABLE user_work_reading_events (
    seq          INTEGER PRIMARY KEY AUTOINCREMENT,
    id           TEXT NOT NULL UNIQUE,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    work_id      TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    -- Explicitly link one status history rather than deriving it from global
    -- insertion order. Duplicate merge can retain independent histories for
    -- the same resulting work without making Undo cross between them.
    previous_event_id TEXT REFERENCES user_work_reading_events(id) ON DELETE SET NULL,
    from_status  TEXT NOT NULL CHECK (from_status IN ('unread', 'reading', 'finished', 'dropped')),
    to_status    TEXT NOT NULL CHECK (to_status IN ('unread', 'reading', 'finished', 'dropped')),
    source       TEXT NOT NULL CHECK (source IN ('manual', 'web_reader', 'kosync')),
    occurred_at  INTEGER NOT NULL DEFAULT (unixepoch()),
    reverted_at  INTEGER,
    CHECK (from_status <> to_status)
);

CREATE INDEX idx_user_work_reading_events_history ON user_work_reading_events(user_id, work_id, seq DESC);
CREATE INDEX idx_user_work_reading_events_finished ON user_work_reading_events(user_id, to_status, occurred_at DESC)
    WHERE reverted_at IS NULL;
CREATE INDEX idx_user_work_reading_events_previous ON user_work_reading_events(previous_event_id);

-- Per-user web-reader annotations. Values are anchored to the concrete asset
-- with Foliate CFI because alternate formats have different document locators.
-- EPUB/FB2 files are never rewritten for web highlights; the DB is authoritative.
CREATE TABLE user_annotations (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    asset_id       TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL DEFAULT 'highlight' CHECK (kind IN ('highlight')),
    cfi            TEXT NOT NULL,
    quote          TEXT NOT NULL DEFAULT '',
    context_before TEXT NOT NULL DEFAULT '',
    context_after  TEXT NOT NULL DEFAULT '',
    note           TEXT NOT NULL DEFAULT '',
    color          TEXT NOT NULL DEFAULT 'yellow' CHECK (color IN ('yellow')),
    created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (user_id, asset_id, kind, cfi)
);

CREATE INDEX idx_user_annotations_asset ON user_annotations(user_id, asset_id, created_at);

-- KOReader locators remain separate from the web reader's user_asset_state.
-- When a hash maps to a catalog asset, its coarse percentage may still advance
-- the work-level reading status; unknown hashes remain pure sync records.
--
-- If a future metadata write-back changes an asset's KOReader document hash,
-- migrate progress per user by merging old/new hash rows: keep the newer
-- updated_at value and prefer the new-hash row on exact timestamp ties. Do not
-- blindly UPDATE document_hash; a user may already have real progress on both
-- hashes from different devices/files.
CREATE TABLE koreader_progress (
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    document_hash TEXT NOT NULL,
    progress      TEXT NOT NULL,
    percentage    REAL NOT NULL DEFAULT 0,
    device        TEXT NOT NULL DEFAULT '',
    device_id     TEXT NOT NULL DEFAULT '',
    updated_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (user_id, document_hash)
);

CREATE INDEX idx_koreader_progress_updated ON koreader_progress(user_id, updated_at DESC);

-- Per-user reader preferences. Keep this deliberately small: values here are
-- about how a person reads, not about any particular book or asset.
CREATE TABLE user_reader_preferences (
    user_id             TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    epub_flow           TEXT NOT NULL DEFAULT 'paginated' CHECK (epub_flow IN ('paginated', 'scrolled')),
    display_style       TEXT NOT NULL DEFAULT 'paper' CHECK (display_style IN ('original', 'paper', 'custom')),
    font_scale          INTEGER NOT NULL DEFAULT 0 CHECK (font_scale >= -4 AND font_scale <= 6),
    custom_column_width INTEGER NOT NULL DEFAULT 760 CHECK (custom_column_width >= 560 AND custom_column_width <= 920),
    custom_line_height  REAL NOT NULL DEFAULT 1.72 CHECK (custom_line_height >= 1.2 AND custom_line_height <= 2.2),
    updated_at          INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Denormalized FTS5 projection rebuilt by write paths that change searchable
-- metadata. The relational tables above remain authoritative.
CREATE VIRTUAL TABLE search USING fts5(
    work_id UNINDEXED,
    title,
    authors,
    series,
    tags,
    description,
    identifiers,
    filename
);
