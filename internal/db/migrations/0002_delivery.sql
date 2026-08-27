-- Send-to-device v1 state. SMTP configuration lives in app_settings; these
-- tables are per-user because device addresses and delivery history are
-- personal state even though library content is shared.

CREATE TABLE delivery_devices (
    id         TEXT PRIMARY KEY,              -- dd_...
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
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

CREATE TABLE delivery_jobs (
    id           TEXT PRIMARY KEY,            -- dj_...
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id    TEXT REFERENCES delivery_devices(id) ON DELETE SET NULL,
    device_name  TEXT NOT NULL,
    device_email TEXT NOT NULL,
    preset       TEXT NOT NULL,
    work_id      TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    asset_id     TEXT REFERENCES assets(id) ON DELETE SET NULL,
    title        TEXT NOT NULL,
    target       TEXT,
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
