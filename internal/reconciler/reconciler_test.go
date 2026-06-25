package reconciler_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/docker"
	"github.com/j0sh3rs/bollard/internal/reconciler"
	"github.com/j0sh3rs/bollard/internal/store"
	"github.com/j0sh3rs/bollard/internal/unifi"
)

type fakeStore struct{ records map[string]store.Record }

func newFakeStore() *fakeStore { return &fakeStore{records: map[string]store.Record{}} }

func (f *fakeStore) Create(_ context.Context, r store.Record) error {
	if _, exists := f.records[r.ContainerID]; exists {
		return fmt.Errorf("duplicate container_id %s", r.ContainerID)
	}
	f.records[r.ContainerID] = r
	return nil
}
func (f *fakeStore) Delete(_ context.Context, id string) error {
	for k, r := range f.records {
		if r.ID == id {
			delete(f.records, k)
		}
	}
	return nil
}
func (f *fakeStore) DeleteByContainerID(_ context.Context, cid string) (*store.Record, error) {
	r, ok := f.records[cid]
	if !ok {
		return nil, nil
	}
	delete(f.records, cid)
	return &r, nil
}
func (f *fakeStore) GetByContainerID(_ context.Context, cid string) (*store.Record, error) {
	r, ok := f.records[cid]
	if !ok {
		return nil, nil
	}
	return &r, nil
}
func (f *fakeStore) ListAll(_ context.Context) ([]store.Record, error) {
	var out []store.Record
	for _, r := range f.records {
		out = append(out, r)
	}
	return out, nil
}
func (f *fakeStore) Close() error { return nil }

type fakeProvider struct {
	records map[string]unifi.DNSRecord
	nextID  int
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{records: map[string]unifi.DNSRecord{}}
}
func (f *fakeProvider) ListRecords(_ context.Context) ([]unifi.DNSRecord, error) {
	var out []unifi.DNSRecord
	for _, r := range f.records {
		out = append(out, r)
	}
	return out, nil
}
func (f *fakeProvider) CreateRecord(_ context.Context, r unifi.DNSRecord) (string, error) {
	f.nextID++
	id := fmt.Sprintf("unifi-%d", f.nextID)
	r.ID = id
	f.records[id] = r
	return id, nil
}
func (f *fakeProvider) DeleteRecord(_ context.Context, id string) error {
	delete(f.records, id)
	return nil
}

func TestHandleEvent_Start(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, "192.168.1.1", slog.Default())

	err := r.HandleEvent(context.Background(), docker.Event{
		Type:        "start",
		ContainerID: "container-123",
		Labels:      map[string]string{"dns.bollard/hostname": "myapp.home.arpa"},
	})
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	rec, err := s.GetByContainerID(context.Background(), "container-123")
	if err != nil || rec == nil {
		t.Fatal("expected record in store after start event")
	}
	if len(p.records) != 1 {
		t.Fatalf("expected 1 unifi record, got %d", len(p.records))
	}
}

func TestHandleEvent_Stop(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, "192.168.1.1", slog.Default())

	_ = s.Create(context.Background(), store.Record{
		ID: "rec-1", ContainerID: "container-456",
		Hostname: "old.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-old",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	p.records["unifi-old"] = unifi.DNSRecord{
		ID: "unifi-old", Hostname: "old.home.arpa", IP: "192.168.1.1",
	}

	err := r.HandleEvent(context.Background(), docker.Event{
		Type: "stop", ContainerID: "container-456",
	})
	if err != nil {
		t.Fatalf("HandleEvent stop: %v", err)
	}
	rec, _ := s.GetByContainerID(context.Background(), "container-456")
	if rec != nil {
		t.Error("expected record removed from store after stop")
	}
	if len(p.records) != 0 {
		t.Error("expected unifi record deleted after stop")
	}
}

func TestHandleEvent_DuplicateHostname(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, "192.168.1.1", slog.Default())

	_ = r.HandleEvent(context.Background(), docker.Event{
		Type: "start", ContainerID: "c1",
		Labels: map[string]string{"dns.bollard/hostname": "shared.home.arpa"},
	})
	err := r.HandleEvent(context.Background(), docker.Event{
		Type: "start", ContainerID: "c2",
		Labels: map[string]string{"dns.bollard/hostname": "shared.home.arpa"},
	})
	if err == nil {
		t.Fatal("expected error on duplicate hostname")
	}
	if len(p.records) != 1 {
		t.Errorf("expected exactly 1 unifi record, got %d", len(p.records))
	}
}
