-- A book is a catalog entry for a publication; its assets are concrete
-- files (for example EPUB and PDF). Publication metadata belongs to the book;
-- different translations or reissues are separate books.
-- SQLite is authoritative. storage_path changes only after the asset's file
-- exists at its new location.
--
-- Timestamps are Unix seconds unless noted otherwise. Reading activity uses
-- Unix milliseconds; publication dates and local calendar days are text.

CREATE TABLE books (
    -- Stable catalog identity and FTS address; deleted identities are not reused.
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    sort_title TEXT NOT NULL,
    -- Denormalized primary author sort key for large-library list sorting.
    -- The author relation remains authoritative; write paths refresh this
    -- column after changing book_authors or author sort_name.
    primary_author_sort TEXT NOT NULL DEFAULT '',
    series TEXT,
    series_index REAL,
    description TEXT,
    tags TEXT, -- Comma-separated values; bookmeta.ParseTagList defines equality.
    publisher TEXT,
    published_date TEXT,
    language TEXT,
    identifiers TEXT, -- Unstructured identifier text, not JSON.
    manual_overrides TEXT, -- JSON object of manually edited field names -> true.
    -- Metadata edits advance this revision. Imported files start at revision 0
    -- and need no write-back until the book's metadata changes.
    metadata_rev INTEGER NOT NULL DEFAULT 0,
    -- 0 means no stored original cover; positive values are cache generations,
    -- independent of the metadata/write-back revision.
    cover_version INTEGER NOT NULL DEFAULT 0,
    -- created_at records when Polka created this row. added_at preserves the
    -- book's earlier collection chronology when import metadata or source file
    -- times provide it; otherwise both begin at the current time.
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    added_at INTEGER NOT NULL DEFAULT (unixepoch()),
    -- NULL means live. Trashed books are excluded from normal catalog queries;
    -- their rows and files remain until purge.
    deleted_at INTEGER,
    deleted_by INTEGER REFERENCES users(id) ON DELETE SET NULL
);

-- Trash listing: only the soft-deleted books, newest-trashed first.
CREATE INDEX idx_books_deleted_at ON books(deleted_at) WHERE deleted_at IS NOT NULL;

-- Browse indexes exclude Trash and follow the list queries' ordering.
-- books.id is already stored as each index's implicit rowid tie-break.
CREATE INDEX idx_books_live_author_sort ON books(primary_author_sort, sort_title COLLATE NOCASE, title COLLATE NOCASE) WHERE deleted_at IS NULL;
CREATE INDEX idx_books_live_added ON books(added_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_books_live_title ON books(sort_title COLLATE NOCASE, title COLLATE NOCASE) WHERE deleted_at IS NULL;
CREATE INDEX idx_books_live_pubdate ON books(published_date DESC, added_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_books_live_series_order ON books(
    series,
    CASE WHEN series_index IS NOT NULL AND series_index > 0 THEN 0 ELSE 1 END,
    CASE WHEN series_index IS NOT NULL AND series_index > 0 THEN series_index ELSE 0 END,
    title COLLATE NOCASE
) WHERE deleted_at IS NULL;

-- storage_path is relative to the managed storage root. Access resolves the
-- current path by asset ID, so relayout preserves links. is_primary selects
-- the default file for workflows that need one asset per book.
CREATE TABLE assets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    storage_path TEXT NOT NULL,
    filename TEXT NOT NULL, -- Basename of the current storage_path.
    original_filename TEXT NOT NULL DEFAULT '', -- Basename at import.
    extension TEXT NOT NULL,
    -- Detected content format (for example epub, fb2, azw4, markdown),
    -- independent of the filename extension. Drives reader/conversion support.
    format TEXT NOT NULL DEFAULT 'unknown',
    is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
    -- Reader capability checked at import, so listing needs no file validation.
    -- Repair can recompute it only after verifying the current file's identity.
    can_read INTEGER NOT NULL DEFAULT 0 CHECK (can_read IN (0, 1)),
    -- original_sha256 is the hash of the bytes first imported. It never changes,
    -- even if polka later rewrites metadata into the managed file. current_sha256
    -- tracks the bytes currently on disk and changes after such rewrites.
    original_sha256 BLOB NOT NULL CHECK (typeof(original_sha256) = 'blob' AND length(original_sha256) = 32),
    current_sha256 BLOB NOT NULL CHECK (typeof(current_sha256) = 'blob' AND length(current_sha256) = 32),
    -- Byte sizes for the same original/current identities; avoid stat on lists.
    original_size INTEGER,
    current_size INTEGER,
    -- Cached KOReader sampled hash of the current file bytes. It is not unique:
    -- catalog mapping is valid only when all live matches belong to one book.
    koreader_hash TEXT,
    -- Book metadata revision last embedded into this file. Non-writable formats
    -- keep the default; writable assets are dirty when writeback_rev is behind
    -- books.metadata_rev. writeback_error records the last failed attempt.
    writeback_rev INTEGER NOT NULL DEFAULT 0,
    writeback_error TEXT,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE UNIQUE INDEX idx_assets_original_sha256 ON assets(original_sha256);
CREATE INDEX idx_assets_current_sha256 ON assets(current_sha256);
CREATE INDEX idx_assets_koreader_hash ON assets(koreader_hash) WHERE koreader_hash IS NOT NULL AND koreader_hash <> '';
-- Keep this predicate in sync with format.MetadataWritebackFormatKeys().
CREATE INDEX idx_assets_writeback_dirty ON assets(book_id, writeback_rev) WHERE format IN ('epub', 'fb2', 'kepub');
CREATE INDEX idx_assets_book_id ON assets(book_id);
CREATE UNIQUE INDEX idx_assets_storage_path ON assets(storage_path);
CREATE UNIQUE INDEX idx_assets_one_primary_per_book ON assets(book_id) WHERE is_primary = 1;

-- One row per in-flight physical metadata write-back replacement. Dirty work
-- is still derived from assets.writeback_rev < books.metadata_rev; this table
-- only makes the overwrite temp/rename window inspectable and repairable.
-- Both paths are relative to the storage root; hashes and byte size describe
-- the staged replacement, not the previous file at storage_path.
CREATE TABLE metadata_writeback_attempts (
    asset_id INTEGER PRIMARY KEY REFERENCES assets(id) ON DELETE CASCADE,
    metadata_rev INTEGER NOT NULL,
    storage_path TEXT NOT NULL,
    temp_path TEXT NOT NULL,
    sha256 BLOB NOT NULL CHECK (typeof(sha256) = 'blob' AND length(sha256) = 32),
    size INTEGER NOT NULL,
    koreader_hash TEXT,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Authors are matched by exact display name. sort_name also controls the
-- storage bucket/folder, so changing it relayouts primary-author files.
CREATE TABLE authors (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    sort_name TEXT NOT NULL
);

-- author_order defines the display order and primary author. The first author
-- is also the author used by canonical path construction. Books without known
-- authors have no entries here.
CREATE TABLE book_authors (
    book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    author_id INTEGER NOT NULL REFERENCES authors(id) ON DELETE CASCADE,
    role TEXT,
    author_order INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (book_id, author_id)
);

CREATE INDEX idx_book_authors_author_id ON book_authors(author_id);

-- Cleanup duplicate dismissals are detector bookkeeping, not book metadata. A
-- group is hidden only while its current live member set is covered by one of
-- these rows for the same detector reason/key; metadata edits and newly imported
-- copies naturally surface it again.
CREATE TABLE duplicate_dismissals (
    reason       TEXT NOT NULL,
    detector_key TEXT NOT NULL,
    book_ids     TEXT NOT NULL, -- JSON array of sorted, distinct book IDs.
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    created_by   INTEGER REFERENCES users(id) ON DELETE SET NULL,
    UNIQUE (reason, detector_key, book_ids)
);

-- Accounts share catalog data; shelves, reading state, and preferences belong
-- to individual users. Usernames are normalized to lowercase before storage;
-- passwords are stored as bcrypt hashes.
CREATE TABLE users (
    -- Do not reuse a deleted account's identity for stale URLs or requests.
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'reader' CHECK (role IN ('admin', 'member', 'reader')),
    -- For readers, 'shelves' restricts access to eligible user_scope_shelves;
    -- an empty assignment grants nothing. Members/admins see the full catalog.
    content_scope TEXT NOT NULL DEFAULT 'all' CHECK (content_scope IN ('all', 'shelves')),
    created_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at    INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE UNIQUE INDEX idx_users_username ON users(username);

-- Browser login sessions. The cookie contains a random token; SQLite retains
-- only its SHA-256 hash. Each session has an explicit expiry.
CREATE TABLE sessions (
    token_hash   BLOB NOT NULL PRIMARY KEY CHECK (typeof(token_hash) = 'blob' AND length(token_hash) = 32),
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);

CREATE INDEX idx_sessions_user_id ON sessions(user_id);

-- Device credentials remain retrievable for setup and have no automatic expiry.
-- Auth middleware restricts their accepted routes.
CREATE TABLE app_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token        TEXT NOT NULL UNIQUE,
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
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('manual', 'query')),
    query      TEXT,
    query_match TEXT,
    owner_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
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
    shelf_id INTEGER NOT NULL REFERENCES shelves(id) ON DELETE CASCADE,
    book_id  INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    position INTEGER NOT NULL DEFAULT 0,
    added_at INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (shelf_id, book_id)
);

CREATE INDEX idx_shelf_books_book_id ON shelf_books(book_id);

-- Access grants for shelf-scoped readers. Eligibility is checked at query time;
-- a reader's own personal shelves cannot widen their access.
CREATE TABLE user_scope_shelves (
    user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    shelf_id INTEGER NOT NULL REFERENCES shelves(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, shelf_id)
);

CREATE INDEX idx_user_scope_shelves_shelf_id ON user_scope_shelves(shelf_id);

-- Account and reader layout preferences shared across the user's books.
CREATE TABLE user_settings (
    user_id               INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    theme                 TEXT NOT NULL DEFAULT 'system' CHECK (theme IN ('system', 'light', 'dark', 'sepia')),
    show_continue_reading INTEGER NOT NULL DEFAULT 1 CHECK (show_continue_reading IN (0, 1)),
    -- IANA zone name. NULL means not yet selected/detected; 'UTC' is a choice.
    time_zone             TEXT,
    reader_flow           TEXT NOT NULL DEFAULT 'paginated' CHECK (reader_flow IN ('paginated', 'scrolled')),
    reader_style          TEXT NOT NULL DEFAULT 'paper' CHECK (reader_style IN ('original', 'paper', 'custom')),
    -- Relative font-size step; 0 is the default size.
    reader_font_size       INTEGER NOT NULL DEFAULT 0 CHECK (reader_font_size >= -4 AND reader_font_size <= 6),
    -- Used by the custom reader style: CSS pixels and a line-height multiplier.
    reader_column_width   INTEGER NOT NULL DEFAULT 760 CHECK (reader_column_width >= 560 AND reader_column_width <= 920),
    reader_line_height    REAL NOT NULL DEFAULT 1.72 CHECK (reader_line_height >= 1.2 AND reader_line_height <= 2.2),
    updated_at            INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Per-user reader state. Position is stored per concrete asset because EPUB/PDF
-- variants of the same book have independent locators. `locator` is a JSON
-- object opaque to SQLite (CFI/page/resource position/etc. belongs to the reader
-- engine); progress is a coarse normalized 0..1 value for browse/UI summaries.
CREATE TABLE user_asset_state (
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    asset_id     INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    progress     REAL NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 1),
    locator      TEXT NOT NULL DEFAULT '{}',
    last_read_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (user_id, asset_id)
);

CREATE INDEX idx_user_asset_state_last_read ON user_asset_state(user_id, last_read_at DESC);

-- Reading-time accounting, separate from login sessions and reader position.
-- All timestamps in these two activity tables are Unix milliseconds.
-- A web session can contain short pauses: only the current visible segment
-- keeps checkpoint state; earlier segments are folded into counted_ms.
CREATE TABLE reading_sessions (
    id                 INTEGER PRIMARY KEY,
    user_id            INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    asset_id           INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    source             TEXT NOT NULL,
    source_id          BLOB NOT NULL, -- Stable source identity; web uses 16 random bytes.
    -- Captured once; preference changes do not affect past calendar days.
    time_zone          TEXT NOT NULL,
    started_at         INTEGER NOT NULL,
    segment            INTEGER NOT NULL DEFAULT 0,
    segment_started_at INTEGER NOT NULL,
    -- Time accounted through; resuming advances past the uncounted hidden gap.
    observed_at        INTEGER NOT NULL,
    last_activity_at   INTEGER NOT NULL,
    counted_ms         INTEGER NOT NULL DEFAULT 0, -- Total across segments and days.
    closed_at          INTEGER, -- Upper bound for late checkpoints after closure/takeover.
    UNIQUE (user_id, source, source_id),
    CHECK (observed_at >= started_at),
    CHECK (last_activity_at >= started_at),
    CHECK (closed_at IS NULL OR closed_at >= observed_at)
);

CREATE UNIQUE INDEX idx_reading_sessions_active_web ON reading_sessions(user_id)
    WHERE source = 'web' AND closed_at IS NULL;

CREATE INDEX idx_reading_sessions_observed ON reading_sessions(user_id, source, observed_at);

-- One aggregate per session/calendar day in the session's captured time zone.
-- Bounds may span short pauses; active_ms excludes those gaps.
CREATE TABLE reading_session_days (
    session_id INTEGER NOT NULL REFERENCES reading_sessions(id) ON DELETE CASCADE,
    day        TEXT NOT NULL, -- YYYY-MM-DD.
    started_at INTEGER NOT NULL,
    ended_at   INTEGER NOT NULL,
    active_ms  INTEGER NOT NULL CHECK (active_ms > 0),
    PRIMARY KEY (session_id, day),
    CHECK (ended_at > started_at)
) WITHOUT ROWID;

-- Reading status belongs to a book; reader position belongs to an asset.
-- Current state serves status: searches, while events retain completion dates
-- and repeat reads. Undo marks events as reverted and restores the previous state.
-- No row means unread, so importing books creates no per-user status rows.
CREATE TABLE user_book_reading_state (
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    book_id      INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    status       TEXT NOT NULL CHECK (status IN ('unread', 'reading', 'finished', 'dropped')),
    last_event_id INTEGER,
    updated_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (user_id, book_id)
);

CREATE INDEX idx_user_book_reading_state_status ON user_book_reading_state(user_id, status, updated_at DESC);

CREATE TABLE user_book_reading_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    book_id      INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    -- Explicitly link one status history rather than deriving it from global
    -- insertion order. Duplicate merge can retain independent histories for
    -- the same resulting book without making Undo cross between them.
    previous_event_id INTEGER REFERENCES user_book_reading_events(id) ON DELETE SET NULL,
    from_status  TEXT NOT NULL CHECK (from_status IN ('unread', 'reading', 'finished', 'dropped')),
    to_status    TEXT NOT NULL CHECK (to_status IN ('unread', 'reading', 'finished', 'dropped')),
    source       TEXT NOT NULL CHECK (source IN ('manual', 'web_reader', 'kosync')),
    occurred_at  INTEGER NOT NULL DEFAULT (unixepoch()),
    reverted_at  INTEGER,
    CHECK (from_status <> to_status)
);

CREATE INDEX idx_user_book_reading_events_history ON user_book_reading_events(user_id, book_id, id DESC);
CREATE INDEX idx_user_book_reading_events_finished ON user_book_reading_events(user_id, to_status, occurred_at DESC)
    WHERE reverted_at IS NULL;
CREATE INDEX idx_user_book_reading_events_previous ON user_book_reading_events(previous_event_id);

-- Per-user web-reader annotations. Values are anchored to the concrete asset
-- with Foliate CFI because alternate formats have different document locators.
-- EPUB/FB2 files are never rewritten for web highlights; the DB is authoritative.
CREATE TABLE user_annotations (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    asset_id       INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL DEFAULT 'highlight' CHECK (kind IN ('highlight')),
    cfi            TEXT NOT NULL,
    quote          TEXT NOT NULL DEFAULT '',
    context_before TEXT NOT NULL DEFAULT '',
    context_after  TEXT NOT NULL DEFAULT '',
    note           TEXT NOT NULL DEFAULT '',
    color          TEXT NOT NULL DEFAULT 'yellow' CHECK (color IN ('yellow', 'green', 'blue', 'pink', 'purple')),
    created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (user_id, asset_id, kind, cfi)
);

CREATE INDEX idx_user_annotations_asset ON user_annotations(user_id, asset_id, created_at);

-- One native Kobo connection per account, projecting one shelf. Its setup URL
-- remains retrievable; replacing the connection revokes the previous URL.
CREATE TABLE kobo_connections (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    shelf_id     INTEGER NOT NULL REFERENCES shelves(id) ON DELETE CASCADE,
    token        TEXT NOT NULL UNIQUE,
    revision     INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    last_used_at INTEGER
);

CREATE INDEX idx_kobo_connections_shelf_id ON kobo_connections(shelf_id);

-- Durable latest-value change feed for one Kobo connection. asset_id/book_id
-- intentionally are not foreign keys: a removed/purged book must leave a
-- tombstone until an offline device asks for revisions it missed. A present row
-- is always revalidated against live assets before metadata or bytes are served.
CREATE TABLE kobo_items (
    connection_id INTEGER NOT NULL REFERENCES kobo_connections(id) ON DELETE CASCADE,
    asset_id       INTEGER NOT NULL,
    book_id        INTEGER NOT NULL,
    fingerprint    BLOB NOT NULL CHECK (typeof(fingerprint) = 'blob' AND length(fingerprint) = 16),
    present        INTEGER NOT NULL CHECK (present IN (0, 1)),
    revision       INTEGER NOT NULL CHECK (revision > 0),
    first_revision INTEGER NOT NULL CHECK (first_revision > 0),
    updated_at     INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (connection_id, asset_id),
    UNIQUE (connection_id, revision)
);

-- KOReader locators use document hashes, independently of web-reader locators.
-- An unambiguous live-catalog match may advance book-level reading status.
-- Write-back updates assets.koreader_hash but leaves these records under their
-- original hashes: devices may still hold the old file bytes.
CREATE TABLE koreader_progress (
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    document_hash TEXT NOT NULL,
    progress      TEXT NOT NULL, -- Opaque KOReader locator.
    percentage    REAL NOT NULL DEFAULT 0,
    device        TEXT NOT NULL DEFAULT '',
    device_id     TEXT NOT NULL DEFAULT '',
    updated_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (user_id, document_hash)
);

CREATE INDEX idx_koreader_progress_updated ON koreader_progress(user_id, updated_at DESC);

-- Send-to-device state. SMTP configuration lives in app_settings; these
-- tables are per-user because device addresses and delivery history are
-- personal state even though library content is shared.

CREATE TABLE delivery_devices (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    email      TEXT NOT NULL,
    preset     TEXT NOT NULL CHECK (preset IN ('kindle', 'pocketbook', 'generic')),
    is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (user_id, name)
);

CREATE INDEX idx_delivery_devices_user ON delivery_devices(user_id, created_at DESC);

CREATE UNIQUE INDEX idx_delivery_devices_one_default
    ON delivery_devices(user_id) WHERE is_default = 1;

-- Queue and delivery history. Device details and title are snapshots taken
-- when queued; later edits do not change the planned recipient or history.
CREATE TABLE delivery_jobs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id    INTEGER REFERENCES delivery_devices(id) ON DELETE SET NULL,
    device_name  TEXT NOT NULL,
    device_email TEXT NOT NULL,
    preset       TEXT NOT NULL,
    book_id      INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    asset_id     INTEGER REFERENCES assets(id) ON DELETE SET NULL,
    title        TEXT NOT NULL,
    target       TEXT, -- Conversion target format; NULL sends the original file.
    filename     TEXT NOT NULL DEFAULT '',
    size_bytes   INTEGER,
    status       TEXT NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued', 'converting', 'sending', 'sent', 'failed')),
    error        TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    sent_at      INTEGER
);

CREATE INDEX idx_delivery_jobs_user_created ON delivery_jobs(user_id, created_at DESC);
CREATE INDEX idx_delivery_jobs_status_created ON delivery_jobs(status, created_at);

-- Library-wide settings. Each owning subsystem defines its keys, value encoding,
-- and defaults for missing rows.
CREATE TABLE app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- One cross-process writer lease for storage-mutating maintenance. It is a
-- heartbeat row rather than a filesystem lock so it works on network
-- filesystems where flock semantics are unreliable.
-- owner identifies a process/lease instance, not a user account.
CREATE TABLE writer_leases (
    name       TEXT PRIMARY KEY,
    owner      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

-- Contentless FTS5 projection updated with searchable metadata. Each rowid
-- matches books.id; the relational tables remain authoritative.
-- tag_keys contains encoded whole-tag tokens produced by TagSearchKeys.
CREATE VIRTUAL TABLE search USING fts5(
    title,
    authors,
    series,
    tags,
    description,
    identifiers,
    filename,
    tag_keys,
    content='',
    contentless_delete=1
);

-- Prefer title, authors, then series. Exact tag keys filter membership without
-- contributing relevance; ordinary word searches exclude that internal column.
INSERT INTO search(search, rank) VALUES ('rank', 'bm25(10.0, 8.0, 4.0, 1.0, 1.0, 1.0, 1.0, 0.0)');
