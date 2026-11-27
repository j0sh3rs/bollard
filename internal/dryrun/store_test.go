package dryrun_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/j0sh3rs/bollard/internal/dryrun"
	"github.com/j0sh3rs/bollard/internal/store"
)

// A dry run must see existing ownership but leave the database untouched.
func TestStoreOverlayNeverWritesToBase(t *testing.T) {
	ctx := context.Background()
	base, err := store.NewSQLite("file:"+filepath.Join(t.TempDir(), "s.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	if err := base.Create(ctx, store.Record{ID: "r1", ContainerID: "c1", Hostname: "a.home.arpa", RecordType: "A", UnifiRecordID: "u1"}); err != nil {
		t.Fatal(err)
	}

	s := dryrun.NewStore(base)
	if err := s.Create(ctx, store.Record{ID: "r2", ContainerID: "c2", Hostname: "b.home.arpa", RecordType: "A", UnifiRecordID: "u2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, store.Record{ID: "r3", ContainerID: "c2", Hostname: "b.home.arpa", RecordType: "A"}); err == nil {
		t.Error("duplicate (container, hostname) accepted")
	}
	if err := s.Update(ctx, store.Record{ID: "r1", ContainerID: "c1", Hostname: "a.home.arpa", RecordType: "A", UnifiRecordID: "u1", TTL: 7}); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.ListByContainerID(ctx, "c1"); len(rows) != 1 || rows[0].TTL != 7 {
		t.Errorf("update not visible: %+v", rows)
	}
	if _, err := s.DeleteByContainerID(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := s.ListAll(ctx); len(rows) != 1 || rows[0].ID != "r2" {
		t.Errorf("overlay rows = %+v, want only r2", rows)
	}

	rows, err := base.ListAll(ctx)
	if err != nil || len(rows) != 1 || rows[0].ID != "r1" || rows[0].TTL != 0 {
		t.Fatalf("base changed: %+v, %v", rows, err)
	}
}
