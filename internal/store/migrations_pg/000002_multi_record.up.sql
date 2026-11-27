ALTER TABLE records DROP CONSTRAINT IF EXISTS records_container_id_key;
ALTER TABLE records ADD COLUMN target TEXT NOT NULL DEFAULT '';
ALTER TABLE records ADD COLUMN drain_until TIMESTAMPTZ;
ALTER TABLE records ADD COLUMN instance_tag TEXT NOT NULL DEFAULT '';
ALTER TABLE records ADD CONSTRAINT records_tag_container_hostname_key UNIQUE (instance_tag, container_id, hostname);
