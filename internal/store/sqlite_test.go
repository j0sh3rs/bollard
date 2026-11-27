package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/store"
	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLite("file::memory:?cache=shared", "")
	if err != nil {
		t.Fatalf("failed to open test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSQLite_CreateAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := store.Record{
		ID:            "test-uuid-1",
		ContainerID:   "container-abc123",
		Hostname:      "myapp.home.arpa",
		IP:            "192.168.1.10",
		RecordType:    "A",
		TTL:           300,
		UnifiRecordID: "unifi-id-xyz",
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		UpdatedAt:     time.Now().UTC().Truncate(time.Second),
	}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	recs, err := s.ListByContainerID(ctx, "container-abc123")
	if err != nil {
		t.Fatalf("ListByContainerID: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if got.Hostname != r.Hostname {
		t.Errorf("expected hostname %q, got %q", r.Hostname, got.Hostname)
	}
	if got.UnifiRecordID != r.UnifiRecordID {
		t.Errorf("expected unifi ID %q, got %q", r.UnifiRecordID, got.UnifiRecordID)
	}
}

func TestSQLite_DeleteByContainerID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := store.Record{
		ID: "test-uuid-2", ContainerID: "container-del123",
		Hostname: "del.home.arpa", IP: "192.168.1.11", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-del",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	deleted, err := s.DeleteByContainerID(ctx, "container-del123")
	if err != nil {
		t.Fatalf("DeleteByContainerID: %v", err)
	}
	if len(deleted) != 1 || deleted[0].UnifiRecordID != "unifi-del" {
		t.Errorf("expected deleted record unifi ID unifi-del, got %v", deleted)
	}
	got, err := s.ListByContainerID(ctx, "container-del123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Error("expected none after delete")
	}
}

func TestSQLite_ListAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i, cid := range []string{"c1", "c2", "c3"} {
		if err := s.Create(ctx, store.Record{
			ID: fmt.Sprintf("uuid-%d", i), ContainerID: cid,
			Hostname: cid + ".home.arpa", IP: "192.168.1.1", RecordType: "A",
			TTL: 300, UnifiRecordID: "unifi-" + cid,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("Create %s: %v", cid, err)
		}
	}
	all, err := s.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 records, got %d", len(all))
	}
}

func TestSQLite_DuplicateContainerHostname(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := store.Record{
		ID: "uuid-dup", ContainerID: "dup-container",
		Hostname: "dup.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-dup",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	r.ID = "uuid-dup2"
	if err := s.Create(ctx, r); err == nil {
		t.Fatal("expected error on duplicate (container_id, hostname)")
	}
}

func TestSQLite_DeleteByContainerID_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// Deleting a non-existent container returns no rows and no error.
	deleted, err := s.DeleteByContainerID(ctx, "no-such-container")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("expected no records for missing container, got %v", deleted)
	}
}

func TestSQLite_ListByContainerID_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	got, err := s.ListByContainerID(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected none for missing container, got %v", got)
	}
}

func TestSQLite_ListAll_Empty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	all, err := s.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll on empty store: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 records, got %d", len(all))
	}
}

func TestSQLite_Delete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := store.Record{
		ID: "uuid-direct-del", ContainerID: "container-direct-del",
		Hostname: "direct.home.arpa", IP: "192.168.1.20", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-direct",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Delete(ctx, "uuid-direct-del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := s.ListByContainerID(ctx, "container-direct-del")
	if err != nil {
		t.Fatalf("ListByContainerID after delete: %v", err)
	}
	if len(got) != 0 {
		t.Error("expected none after Delete")
	}
}

func fileDSN(t *testing.T) string {
	t.Helper()
	return "file:" + filepath.Join(t.TempDir(), "s.db")
}

func TestSQLite_MultiRowPerContainer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i, h := range []string{"b.home.arpa", "a.home.arpa"} {
		if err := s.Create(ctx, store.Record{
			ID: fmt.Sprintf("multi-%d", i), ContainerID: "multi", Hostname: h,
			IP: "192.168.1.1", RecordType: "A", TTL: 300, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("Create %s: %v", h, err)
		}
	}
	recs, err := s.ListByContainerID(ctx, "multi")
	if err != nil {
		t.Fatalf("ListByContainerID: %v", err)
	}
	if len(recs) != 2 || recs[0].Hostname != "a.home.arpa" || recs[1].Hostname != "b.home.arpa" {
		t.Fatalf("expected 2 records ordered by hostname, got %+v", recs)
	}
	deleted, err := s.DeleteByContainerID(ctx, "multi")
	if err != nil || len(deleted) != 2 {
		t.Fatalf("DeleteByContainerID = %v, %v; want 2 rows", deleted, err)
	}
}

func TestSQLite_UpdateRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	r := store.Record{
		ID: "upd-1", ContainerID: "upd", Hostname: "upd.home.arpa", RecordType: "CNAME",
		IP: "", Target: "old.home.arpa", TTL: 300, UnifiRecordID: "u1", CreatedAt: now, UpdatedAt: now,
	}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, _ := s.ListByContainerID(ctx, "upd")
	if len(got) != 1 || !got[0].DrainUntil.IsZero() || got[0].Target != "old.home.arpa" {
		t.Fatalf("unexpected initial row %+v", got)
	}
	r.Target = "new.home.arpa"
	r.TTL = 60
	r.UnifiRecordID = "u2"
	r.DrainUntil = now.Add(time.Hour)
	r.UpdatedAt = now.Add(time.Minute)
	if err := s.Update(ctx, r); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = s.ListByContainerID(ctx, "upd")
	if len(got) != 1 || got[0].Target != "new.home.arpa" || got[0].TTL != 60 ||
		got[0].UnifiRecordID != "u2" || !got[0].DrainUntil.Equal(r.DrainUntil) || !got[0].UpdatedAt.Equal(r.UpdatedAt) {
		t.Fatalf("update not persisted: %+v", got)
	}
	r.DrainUntil = time.Time{}
	if err := s.Update(ctx, r); err != nil {
		t.Fatalf("Update clear drain: %v", err)
	}
	if got, _ = s.ListByContainerID(ctx, "upd"); !got[0].DrainUntil.IsZero() {
		t.Errorf("expected drain cleared, got %v", got[0].DrainUntil)
	}
	if err := s.Update(ctx, store.Record{ID: "missing"}); err == nil {
		t.Error("expected error updating missing row")
	}
}

func TestSQLite_TagIsolation(t *testing.T) {
	dsn := fileDSN(t)
	ctx := context.Background()
	a, err := store.NewSQLite(dsn, "a")
	if err != nil {
		t.Fatalf("open a: %v", err)
	}
	if err := a.Create(ctx, store.Record{
		ID: "tag-a", ContainerID: "c", Hostname: "x.home.arpa", RecordType: "A",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	a.Close()

	b, err := store.NewSQLite(dsn, "b")
	if err != nil {
		t.Fatalf("open b: %v", err)
	}
	defer b.Close()
	// Same (container, hostname) under another tag must not collide.
	if err := b.Create(ctx, store.Record{
		ID: "tag-b", ContainerID: "c", Hostname: "x.home.arpa", RecordType: "A",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	if all, _ := b.ListAll(ctx); len(all) != 1 || all[0].ID != "tag-b" {
		t.Fatalf("b sees %+v", all)
	}
	if err := b.Delete(ctx, "tag-a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := b.Update(ctx, store.Record{ID: "tag-a"}); err == nil {
		t.Error("b must not update a's row")
	}
	if del, _ := b.DeleteByContainerID(ctx, "c"); len(del) != 1 || del[0].ID != "tag-b" {
		t.Fatalf("b deleted %+v", del)
	}
	a2, err := store.NewSQLite(dsn, "a")
	if err != nil {
		t.Fatalf("reopen a: %v", err)
	}
	defer a2.Close()
	if all, _ := a2.ListAll(ctx); len(all) != 1 || all[0].ID != "tag-a" {
		t.Fatalf("a lost its row: %+v", all)
	}
}

func TestSQLite_InvalidTag(t *testing.T) {
	for _, tag := range []string{"has space", "bad/slash", string(make([]byte, 65)), "ünï"} {
		if _, err := store.NewSQLite(fileDSN(t), tag); err == nil {
			t.Errorf("expected error for tag %q", tag)
		}
	}
	long := make([]byte, 64)
	for i := range long {
		long[i] = 'a'
	}
	s, err := store.NewSQLite(fileDSN(t), string(long))
	if err != nil {
		t.Fatalf("64-char tag rejected: %v", err)
	}
	s.Close()
}

func TestSQLite_MigrationFromV1(t *testing.T) {
	dsn := fileDSN(t)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE records (
			id TEXT PRIMARY KEY, container_id TEXT NOT NULL UNIQUE, hostname TEXT NOT NULL,
			ip TEXT NOT NULL, record_type TEXT NOT NULL, ttl INTEGER NOT NULL,
			unifi_record_id TEXT NOT NULL, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`,
		`CREATE INDEX idx_records_container_id ON records(container_id)`,
		`CREATE INDEX idx_records_hostname ON records(hostname)`,
		`INSERT INTO records VALUES ('old-1','old-c','old.home.arpa','10.0.0.1','A',300,'u-old','2024-01-02T03:04:05Z','2024-01-02T03:04:05Z')`,
		`CREATE TABLE schema_migrations (version uint64, dirty bool)`,
		`CREATE UNIQUE INDEX version_unique ON schema_migrations (version)`,
		`INSERT INTO schema_migrations VALUES (1, 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	s, err := store.NewSQLite(dsn, "")
	if err != nil {
		t.Fatalf("open upgraded: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	got, err := s.ListByContainerID(ctx, "old-c")
	if err != nil || len(got) != 1 {
		t.Fatalf("ListByContainerID = %v, %v", got, err)
	}
	if g := got[0]; g.ID != "old-1" || g.IP != "10.0.0.1" || g.UnifiRecordID != "u-old" || g.Target != "" || !g.DrainUntil.IsZero() ||
		!g.CreatedAt.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("row not preserved: %+v", g)
	}
	// Second row for the same container is now allowed.
	if err := s.Create(ctx, store.Record{
		ID: "old-2", ContainerID: "old-c", Hostname: "other.home.arpa", RecordType: "A",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("second row: %v", err)
	}
}
