package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/store"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Upgrading a v1 database must keep its rows and really drop the single-column unique constraint,
// whatever that constraint is called: Postgres names it records_container_id_key only when it
// generated the name itself.
func TestPostgres_MigrationFromV1(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres integration tests")
	}
	v1, err := os.ReadFile("migrations_pg/000001_init.up.sql")
	if err != nil {
		t.Fatal(err)
	}

	for name, ddl := range map[string]string{
		"generated constraint name": string(v1),
		"custom constraint name": strings.Replace(string(v1),
			"container_id    TEXT NOT NULL UNIQUE,",
			"container_id    TEXT NOT NULL,\n    CONSTRAINT my_own_name UNIQUE (container_id),", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if name == "custom constraint name" && !strings.Contains(ddl, "my_own_name") {
				t.Fatal("test setup: v1 DDL changed, custom constraint was not substituted")
			}
			ctx := context.Background()
			schema := fmt.Sprintf("upgrade_%d", time.Now().UnixNano())
			admin, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close()
			if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE") })

			sep := "?"
			if strings.Contains(dsn, "?") {
				sep = "&"
			}
			scoped := dsn + sep + "search_path=" + schema

			old, err := sql.Open("pgx", scoped)
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			for _, stmt := range []string{
				ddl,
				`CREATE TABLE schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL)`,
				`INSERT INTO schema_migrations VALUES (1, false)`,
				`INSERT INTO records (id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at)
				 VALUES ('old-row', 'c1', 'old.home.arpa', '192.168.1.5', 'A', 120, 'u-old', now(), now())`,
			} {
				if _, err := old.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("seed v1 database: %v\n%s", err, stmt)
				}
			}

			s, err := store.NewPostgres(scoped, "")
			if err != nil {
				t.Fatalf("upgrade: %v", err)
			}
			defer s.Close()

			rows, err := s.ListByContainerID(ctx, "c1")
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows after upgrade = %+v, %v; want the v1 row", rows, err)
			}
			if r := rows[0]; r.ID != "old-row" || r.Hostname != "old.home.arpa" || r.IP != "192.168.1.5" ||
				r.TTL != 120 || r.UnifiRecordID != "u-old" || r.Target != "" || !r.DrainUntil.IsZero() {
				t.Fatalf("v1 row changed by the upgrade: %+v", r)
			}

			// The old constraint is gone: one container may now own several hostnames.
			now := time.Now().UTC()
			if err := s.Create(ctx, store.Record{ID: "new-row", ContainerID: "c1", Hostname: "second.home.arpa",
				IP: "192.168.1.5", RecordType: "A", TTL: 300, UnifiRecordID: "u-new", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatalf("second hostname for the same container: %v", err)
			}

			// And no unique constraint on container_id alone survives, under any name.
			var remaining int
			err = old.QueryRowContext(ctx, `
				SELECT count(*) FROM pg_constraint con
				WHERE con.conrelid = 'records'::regclass AND con.contype = 'u'
				  AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
				       WHERE a.attrelid = con.conrelid AND a.attnum = ANY (con.conkey)) = ARRAY['container_id']`).Scan(&remaining)
			if err != nil || remaining != 0 {
				t.Fatalf("single-column unique constraints left: %d, %v", remaining, err)
			}

			// The replacement key still forbids a duplicate (container, hostname).
			if err := s.Create(ctx, store.Record{ID: "dup", ContainerID: "c1", Hostname: "second.home.arpa",
				RecordType: "A", UnifiRecordID: "u-dup", CreatedAt: now, UpdatedAt: now}); err == nil {
				t.Fatal("duplicate (container, hostname) accepted")
			}
		})
	}
}
