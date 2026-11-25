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

type fakeLister struct {
	containers map[string]map[string]string
	listErr    error
}

func (f *fakeLister) ListRunning(_ context.Context) (map[string]map[string]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.containers, nil
}

type fakeProvider struct {
	records   map[string]unifi.DNSRecord
	nextID    int
	deleteErr error
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
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.records, id)
	return nil
}

func TestHandleEvent_Start(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{}}, "192.168.1.1", nil, slog.Default())

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
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{}}, "192.168.1.1", nil, slog.Default())

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
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{}}, "192.168.1.1", nil, slog.Default())

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

func TestHandleStop_KeepsStoreRowWhenProviderFails(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	p.deleteErr = fmt.Errorf("unifi unavailable")
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{}}, "192.168.1.1", nil, slog.Default())

	_ = s.Create(context.Background(), store.Record{
		ID: "rec-1", ContainerID: "container-789",
		Hostname: "stopped.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-stopped",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	p.records["unifi-stopped"] = unifi.DNSRecord{
		ID: "unifi-stopped", Hostname: "stopped.home.arpa", IP: "192.168.1.1",
	}

	err := r.HandleEvent(context.Background(), docker.Event{
		Type: "stop", ContainerID: "container-789",
	})
	if err == nil {
		t.Fatal("expected error when provider delete fails")
	}
	rec, _ := s.GetByContainerID(context.Background(), "container-789")
	if rec == nil {
		t.Error("expected store row kept when provider delete fails")
	}
	if _, ok := p.records["unifi-stopped"]; !ok {
		t.Error("expected unifi record to remain when delete fails")
	}
}

func TestHandleStop_DeletesRowAndRecord(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{}}, "192.168.1.1", nil, slog.Default())

	_ = s.Create(context.Background(), store.Record{
		ID: "rec-2", ContainerID: "container-ok",
		Hostname: "ok.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-ok",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	p.records["unifi-ok"] = unifi.DNSRecord{
		ID: "unifi-ok", Hostname: "ok.home.arpa", IP: "192.168.1.1",
	}

	if err := r.HandleEvent(context.Background(), docker.Event{
		Type: "stop", ContainerID: "container-ok",
	}); err != nil {
		t.Fatalf("HandleEvent stop: %v", err)
	}
	if rec, _ := s.GetByContainerID(context.Background(), "container-ok"); rec != nil {
		t.Error("expected store row removed after stop")
	}
	if len(p.records) != 0 {
		t.Errorf("expected unifi record deleted after stop, got %d", len(p.records))
	}
}

func TestReconcile_RetriesDeleteForStoppedContainer(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{}}, "192.168.1.1", nil, slog.Default())

	_ = s.Create(context.Background(), store.Record{
		ID: "rec-3", ContainerID: "container-dead",
		Hostname: "dead.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-dead",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	p.records["unifi-dead"] = unifi.DNSRecord{
		ID: "unifi-dead", Hostname: "dead.home.arpa", IP: "192.168.1.1",
	}

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if rec, _ := s.GetByContainerID(context.Background(), "container-dead"); rec != nil {
		t.Error("expected store row deleted by retry pass")
	}
	if _, ok := p.records["unifi-dead"]; ok {
		t.Error("expected unifi record deleted by retry pass")
	}
}

func TestReconcile_RetryFailureKeepsRow(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	p.deleteErr = fmt.Errorf("unifi unavailable")
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{}}, "192.168.1.1", nil, slog.Default())

	_ = s.Create(context.Background(), store.Record{
		ID: "rec-4", ContainerID: "container-dead2",
		Hostname: "dead2.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-dead2",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	p.records["unifi-dead2"] = unifi.DNSRecord{
		ID: "unifi-dead2", Hostname: "dead2.home.arpa", IP: "192.168.1.1",
	}

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile must not fail when a retry delete fails: %v", err)
	}
	if rec, _ := s.GetByContainerID(context.Background(), "container-dead2"); rec == nil {
		t.Error("expected store row kept for retry on next tick")
	}
	if _, ok := p.records["unifi-dead2"]; !ok {
		t.Error("expected unifi record to remain when retry delete fails")
	}
}

func TestReconcile_RunningContainerNotTouched(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{"container-live": {}}}, "192.168.1.1", nil, slog.Default())

	_ = s.Create(context.Background(), store.Record{
		ID: "rec-5", ContainerID: "container-live",
		Hostname: "live.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-live",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	p.records["unifi-live"] = unifi.DNSRecord{
		ID: "unifi-live", Hostname: "live.home.arpa", IP: "192.168.1.1",
	}

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if rec, _ := s.GetByContainerID(context.Background(), "container-live"); rec == nil {
		t.Error("expected store row for running container to remain")
	}
	if _, ok := p.records["unifi-live"]; !ok {
		t.Error("expected unifi record for running container to remain")
	}
}

func TestReconcile_ListRunningErrorSkipsDeletes(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, &fakeLister{listErr: fmt.Errorf("docker down")}, "192.168.1.1", nil, slog.Default())

	_ = s.Create(context.Background(), store.Record{
		ID: "rec-6", ContainerID: "container-dead3",
		Hostname: "dead3.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-dead3",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	p.records["unifi-dead3"] = unifi.DNSRecord{
		ID: "unifi-dead3", Hostname: "dead3.home.arpa", IP: "192.168.1.1",
	}

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile must not fail when ListRunning fails: %v", err)
	}
	if rec, _ := s.GetByContainerID(context.Background(), "container-dead3"); rec == nil {
		t.Error("expected no deletes when ListRunning fails")
	}
	if _, ok := p.records["unifi-dead3"]; !ok {
		t.Error("expected no record deletes when ListRunning fails")
	}
}

func TestReconcile_SkipsDeleteWhenRecordSharedWithRunningRow(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{"container-a": {}}}, "192.168.1.1", nil, slog.Default())

	_ = s.Create(context.Background(), store.Record{
		ID: "rec-a", ContainerID: "container-a",
		Hostname: "dup.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-shared",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	_ = s.Create(context.Background(), store.Record{
		ID: "rec-b", ContainerID: "container-b",
		Hostname: "dup.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-shared",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	p.records["unifi-shared"] = unifi.DNSRecord{
		ID: "unifi-shared", Hostname: "dup.home.arpa", IP: "192.168.1.1",
	}

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if rec, _ := s.GetByContainerID(context.Background(), "container-b"); rec == nil {
		t.Error("expected stopped row kept while record is shared with a running row")
	}
	if _, ok := p.records["unifi-shared"]; !ok {
		t.Error("expected shared unifi record to remain")
	}
}

func TestHandleStop_SkipsDeleteWhenRecordSharedWithRunning(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{"container-a": {}}}, "192.168.1.1", nil, slog.Default())

	_ = s.Create(context.Background(), store.Record{
		ID: "rec-a", ContainerID: "container-a",
		Hostname: "dup.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-shared",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	_ = s.Create(context.Background(), store.Record{
		ID: "rec-b", ContainerID: "container-b",
		Hostname: "dup.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-shared",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	p.records["unifi-shared"] = unifi.DNSRecord{
		ID: "unifi-shared", Hostname: "dup.home.arpa", IP: "192.168.1.1",
	}

	err := r.HandleEvent(context.Background(), docker.Event{
		Type: "stop", ContainerID: "container-b",
	})
	if err != nil {
		t.Fatalf("expected nil error when record is shared with a running container: %v", err)
	}
	if rec, _ := s.GetByContainerID(context.Background(), "container-b"); rec == nil {
		t.Error("expected stopped row kept when record is shared")
	}
	if _, ok := p.records["unifi-shared"]; !ok {
		t.Error("expected shared unifi record untouched")
	}
}
