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
	s, err := store.NewPostgres(dsn)
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
	got, err := s.GetByContainerID(ctx, "pg-container-abc123")
	if err != nil {
		t.Fatalf("GetByContainerID: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil record")
	}
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
	if deleted == nil || deleted.UnifiRecordID != "unifi-del" {
		t.Errorf("expected deleted record unifi ID unifi-del, got %v", deleted)
	}
	got, err := s.GetByContainerID(ctx, "pg-container-del123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
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

func TestPostgres_DuplicateContainerID(t *testing.T) {
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
		t.Fatal("expected error on duplicate container_id")
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
	got, err := s.GetByContainerID(ctx, "pg-container-direct-del")
	if err != nil {
		t.Fatalf("GetByContainerID after delete: %v", err)
	}
	if got != nil {
		t.Error("expected nil after Delete")
	}
}

func TestPostgres_DeleteByContainerID_NotFound(t *testing.T) {
	s := newPostgresStore(t)
	ctx := context.Background()
	deleted, err := s.DeleteByContainerID(ctx, "pg-no-such-container")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != nil {
		t.Errorf("expected nil record for missing container, got %v", deleted)
	}
}

func TestPostgres_GetByContainerID_NotFound(t *testing.T) {
	s := newPostgresStore(t)
	ctx := context.Background()
	got, err := s.GetByContainerID(ctx, "pg-does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing container, got %v", got)
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
