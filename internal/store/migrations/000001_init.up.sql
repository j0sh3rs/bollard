CREATE TABLE IF NOT EXISTS records (
    id              TEXT PRIMARY KEY,
    container_id    TEXT NOT NULL UNIQUE,
    hostname        TEXT NOT NULL,
    ip              TEXT NOT NULL,
    record_type     TEXT NOT NULL,
    ttl             INTEGER NOT NULL,
    unifi_record_id TEXT NOT NULL,
    created_at      DATETIME NOT NULL,
    updated_at      DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_records_container_id ON records(container_id);
CREATE INDEX IF NOT EXISTS idx_records_hostname ON records(hostname);
