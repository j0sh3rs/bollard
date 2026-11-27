package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/store"
)

func newPostgresStore(t *testing.T) store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres integration tests")
	}
	s, err := store.NewPostgres(dsn, "")
	if err != nil {
		t.Fatalf("failed to open postgres test store: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		records, err := s.ListAll(ctx)
		if err == nil {
			for _, r := range records {
				_ = s.Delete(ctx, r.ID)
			}
		}
		s.Close()
	})
	return s
}

func TestPostgres_CreateAndGet(t *testing.T) {
	s := newPostgresStore(t)
	ctx := context.Background()
	r := store.Record{
		ID:            "pg-test-uuid-1",
		ContainerID:   "pg-container-abc123",
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
	recs, err := s.ListByContainerID(ctx, "pg-container-abc123")
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

func TestPostgres_DeleteByContainerID(t *testing.T) {
	s := newPostgresStore(t)
	ctx := context.Background()
	r := store.Record{
		ID: "pg-test-uuid-2", ContainerID: "pg-container-del123",
		Hostname: "del.home.arpa", IP: "192.168.1.11", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-del",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	deleted, err := s.DeleteByContainerID(ctx, "pg-container-del123")
	if err != nil {
		t.Fatalf("DeleteByContainerID: %v", err)
	}
	if len(deleted) != 1 || deleted[0].UnifiRecordID != "unifi-del" {
		t.Errorf("expected deleted record unifi ID unifi-del, got %v", deleted)
	}
	got, err := s.ListByContainerID(ctx, "pg-container-del123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Error("expected none after delete")
	}
}

func TestPostgres_ListAll(t *testing.T) {
	s := newPostgresStore(t)
	ctx := context.Background()
	for i, cid := range []string{"pg-c1", "pg-c2", "pg-c3"} {
		if err := s.Create(ctx, store.Record{
			ID: fmt.Sprintf("pg-uuid-%d", i), ContainerID: cid,
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

func TestPostgres_DuplicateContainerHostname(t *testing.T) {
	s := newPostgresStore(t)
	ctx := context.Background()
	r := store.Record{
		ID: "pg-uuid-dup", ContainerID: "pg-dup-container",
		Hostname: "dup.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-dup",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	r.ID = "pg-uuid-dup2"
	if err := s.Create(ctx, r); err == nil {
		t.Fatal("expected error on duplicate (container_id, hostname)")
	}
}

func TestPostgres_Delete(t *testing.T) {
	s := newPostgresStore(t)
	ctx := context.Background()
	r := store.Record{
		ID: "pg-uuid-direct-del", ContainerID: "pg-container-direct-del",
		Hostname: "direct.home.arpa", IP: "192.168.1.20", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-direct",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Delete(ctx, "pg-uuid-direct-del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := s.ListByContainerID(ctx, "pg-container-direct-del")
	if err != nil {
		t.Fatalf("ListByContainerID after delete: %v", err)
	}
	if len(got) != 0 {
		t.Error("expected none after Delete")
	}
}

func TestPostgres_DeleteByContainerID_NotFound(t *testing.T) {
	s := newPostgresStore(t)
	ctx := context.Background()
	deleted, err := s.DeleteByContainerID(ctx, "pg-no-such-container")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("expected no records for missing container, got %v", deleted)
	}
}

func TestPostgres_ListByContainerID_NotFound(t *testing.T) {
	s := newPostgresStore(t)
	ctx := context.Background()
	got, err := s.ListByContainerID(ctx, "pg-does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected none for missing container, got %v", got)
	}
}

func TestPostgres_ListAll_Empty(t *testing.T) {
	s := newPostgresStore(t)
	ctx := context.Background()
	all, err := s.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll on empty store: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 records, got %d", len(all))
	}
}

func TestPostgres_MultiRowUpdateAndTag(t *testing.T) {
	s := newPostgresStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i, h := range []string{"b.home.arpa", "a.home.arpa"} {
		if err := s.Create(ctx, store.Record{
			ID: fmt.Sprintf("pg-multi-%d", i), ContainerID: "pg-multi", Hostname: h,
			IP: "192.168.1.1", RecordType: "A", TTL: 300, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	recs, err := s.ListByContainerID(ctx, "pg-multi")
	if err != nil || len(recs) != 2 || recs[0].Hostname != "a.home.arpa" {
		t.Fatalf("ListByContainerID = %v, %v", recs, err)
	}
	upd := recs[0]
	upd.Target, upd.DrainUntil = "t.home.arpa", now.Add(time.Hour)
	if err := s.Update(ctx, upd); err != nil {
		t.Fatalf("Update: %v", err)
	}
	recs, _ = s.ListByContainerID(ctx, "pg-multi")
	if recs[0].Target != "t.home.arpa" || !recs[0].DrainUntil.Equal(upd.DrainUntil) {
		t.Errorf("update not persisted: %+v", recs[0])
	}
	if err := s.Update(ctx, store.Record{ID: "missing"}); err == nil {
		t.Error("expected error updating missing row")
	}

	other, err := store.NewPostgres(os.Getenv("TEST_POSTGRES_DSN"), "other")
	if err != nil {
		t.Fatalf("NewPostgres other: %v", err)
	}
	defer other.Close()
	if all, _ := other.ListAll(ctx); len(all) != 0 {
		t.Errorf("other tag sees %d rows", len(all))
	}
	if del, _ := other.DeleteByContainerID(ctx, "pg-multi"); len(del) != 0 {
		t.Errorf("other tag deleted %d rows", len(del))
	}
	if _, err := store.NewPostgres(os.Getenv("TEST_POSTGRES_DSN"), "bad tag"); err == nil {
		t.Error("expected error for invalid tag")
	}
}
