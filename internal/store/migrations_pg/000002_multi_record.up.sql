-- Drop the single-column UNIQUE(container_id) by what it is, not by name: the name is
-- records_container_id_key only when Postgres generated it.
DO $$
DECLARE
    c record;
BEGIN
    FOR c IN
        SELECT con.conname
        FROM pg_constraint con
        WHERE con.conrelid = 'records'::regclass
          AND con.contype = 'u'
          AND (SELECT array_agg(a.attname::text ORDER BY a.attname)
               FROM pg_attribute a
               WHERE a.attrelid = con.conrelid AND a.attnum = ANY (con.conkey)) = ARRAY['container_id']
    LOOP
        EXECUTE format('ALTER TABLE records DROP CONSTRAINT %I', c.conname);
    END LOOP;
END $$;

ALTER TABLE records ADD COLUMN target TEXT NOT NULL DEFAULT '';
ALTER TABLE records ADD COLUMN drain_until TIMESTAMPTZ;
ALTER TABLE records ADD COLUMN instance_tag TEXT NOT NULL DEFAULT '';
ALTER TABLE records ADD CONSTRAINT records_tag_container_hostname_key UNIQUE (instance_tag, container_id, hostname);
