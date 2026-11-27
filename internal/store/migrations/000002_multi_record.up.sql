-- SQLite cannot drop a column constraint in place, so rebuild the table.
CREATE TABLE records_new (
    id              TEXT PRIMARY KEY,
    container_id    TEXT NOT NULL,
    hostname        TEXT NOT NULL,
    ip              TEXT NOT NULL,
    record_type     TEXT NOT NULL,
    ttl             INTEGER NOT NULL,
    unifi_record_id TEXT NOT NULL,
    target          TEXT NOT NULL DEFAULT '',
    drain_until     DATETIME,
    instance_tag    TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL,
    updated_at      DATETIME NOT NULL,
    UNIQUE (instance_tag, container_id, hostname)
);

INSERT INTO records_new (id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at)
SELECT id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at FROM records;

DROP TABLE records;
ALTER TABLE records_new RENAME TO records;

CREATE INDEX IF NOT EXISTS idx_records_container_id ON records(container_id);
CREATE INDEX IF NOT EXISTS idx_records_hostname ON records(hostname);
