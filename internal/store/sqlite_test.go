package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/store"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLite("file::memory:?cache=shared")
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
	got, err := s.GetByContainerID(ctx, "container-abc123")
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
	if deleted == nil || deleted.UnifiRecordID != "unifi-del" {
		t.Errorf("expected deleted record unifi ID unifi-del, got %v", deleted)
	}
	got, err := s.GetByContainerID(ctx, "container-del123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
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

func TestSQLite_DuplicateContainerID(t *testing.T) {
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
		t.Fatal("expected error on duplicate container_id")
	}
}

func TestSQLite_DeleteByContainerID_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// Deleting a non-existent container should return nil, nil.
	deleted, err := s.DeleteByContainerID(ctx, "no-such-container")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != nil {
		t.Errorf("expected nil record for missing container, got %v", deleted)
	}
}

func TestSQLite_GetByContainerID_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	got, err := s.GetByContainerID(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing container, got %v", got)
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
