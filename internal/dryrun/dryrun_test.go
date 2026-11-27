package dryrun_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/j0sh3rs/bollard/internal/dryrun"
	"github.com/j0sh3rs/bollard/internal/unifi"
)

type realProvider struct {
	records                   []unifi.DNSRecord
	creates, deletes, updates int
}

func (r *realProvider) ListRecords(context.Context) ([]unifi.DNSRecord, error) {
	return append([]unifi.DNSRecord(nil), r.records...), nil
}
func (r *realProvider) CreateRecord(context.Context, unifi.DNSRecord) (string, error) {
	r.creates++
	return "real", nil
}
func (r *realProvider) DeleteRecord(context.Context, string) error { r.deletes++; return nil }
func (r *realProvider) UpdateRecord(context.Context, string, unifi.DNSRecord) error {
	r.updates++
	return nil
}

// Mutations must never reach the controller, and later listings must reflect the intended state so a
// reconcile tick does not repeat the same would-create line forever.
func TestMutationsStayLocalAndShowInListings(t *testing.T) {
	ctx := context.Background()
	real := &realProvider{records: []unifi.DNSRecord{
		{ID: "keep", Hostname: "keep.home.arpa", RecordType: "A", IP: "10.0.0.1"},
		{ID: "gone", Hostname: "gone.home.arpa", RecordType: "A", IP: "10.0.0.2"},
		{ID: "edit", Hostname: "edit.home.arpa", RecordType: "A", IP: "10.0.0.3", TTL: 300},
	}}
	p := dryrun.New(real, slog.New(slog.NewTextHandler(io.Discard, nil)))

	id, err := p.CreateRecord(ctx, unifi.DNSRecord{Hostname: "new.home.arpa", RecordType: "A", IP: "10.0.0.9"})
	if err != nil || id == "" || id == "real" {
		t.Fatalf("create returned %q, %v", id, err)
	}
	if err := p.DeleteRecord(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if err := p.UpdateRecord(ctx, "edit", unifi.DNSRecord{Hostname: "edit.home.arpa", RecordType: "A", IP: "10.0.0.3", TTL: 5}); err != nil {
		t.Fatal(err)
	}
	if real.creates+real.deletes+real.updates != 0 {
		t.Fatalf("controller was called: %+v", real)
	}

	got := map[string]unifi.DNSRecord{}
	records, err := p.ListRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		got[r.Hostname] = r
	}
	if _, ok := got["gone.home.arpa"]; ok {
		t.Error("deleted record still listed")
	}
	if _, ok := got["new.home.arpa"]; !ok {
		t.Error("created record not listed")
	}
	if got["edit.home.arpa"].TTL != 5 {
		t.Errorf("update not reflected: %+v", got["edit.home.arpa"])
	}
	if _, ok := got["keep.home.arpa"]; !ok {
		t.Error("untouched record missing")
	}

	if err := p.DeleteRecord(ctx, id); err != nil {
		t.Fatal(err)
	}
	records, _ = p.ListRecords(ctx)
	for _, r := range records {
		if r.Hostname == "new.home.arpa" {
			t.Error("deleted fake record still listed")
		}
	}
	if err := p.DeleteRecord(ctx, ""); err == nil {
		t.Error("empty ID accepted")
	}
}
