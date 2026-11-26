package reconciler_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/docker"
	"github.com/j0sh3rs/bollard/internal/metrics"
	"github.com/j0sh3rs/bollard/internal/reconciler"
	"github.com/j0sh3rs/bollard/internal/store"
	"github.com/j0sh3rs/bollard/internal/unifi"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type fakeStore struct {
	records   map[string]store.Record
	createErr error
}

func newFakeStore() *fakeStore { return &fakeStore{records: map[string]store.Record{}} }

func (f *fakeStore) Create(_ context.Context, r store.Record) error {
	if f.createErr != nil {
		return f.createErr
	}
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
	records           map[string]unifi.DNSRecord
	nextID            int
	listCalls         int
	listErr           error
	deleteErr         error
	createResponseErr error
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{records: map[string]unifi.DNSRecord{}}
}
func (f *fakeProvider) ListRecords(_ context.Context) ([]unifi.DNSRecord, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
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
	if f.createResponseErr != nil {
		return "", f.createResponseErr
	}
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

func TestHandleEvent_Start_ReservedHostnameRefused(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{}}, "192.168.1.1", nil, slog.Default()).
		WithReservedHostnames("Unifi.home.arpa.")

	err := r.HandleEvent(context.Background(), docker.Event{
		Type:        "start",
		ContainerID: "container-evil",
		Labels: map[string]string{
			"dns.bollard/hostname":    "unifi.home.arpa",
			"dns.bollard/ip-override": "10.6.6.6",
		},
	})
	if err == nil {
		t.Fatal("expected error for reserved hostname")
	}
	if len(p.records) != 0 {
		t.Fatalf("reserved hostname must not reach UniFi, got %d records", len(p.records))
	}
	if rec, _ := s.GetByContainerID(context.Background(), "container-evil"); rec != nil {
		t.Fatal("reserved hostname must not create a store row")
	}
}

// cancelOnCreateStore cancels the caller's context while the ownership write fails, like SIGTERM
// arriving between the UniFi POST and the store insert.
type cancelOnCreateStore struct {
	*fakeStore
	cancel context.CancelFunc
}

func (c *cancelOnCreateStore) Create(context.Context, store.Record) error {
	c.cancel()
	return context.Canceled
}

// ctxAwareProvider fails deletes issued on a cancelled context, as the real transport does.
type ctxAwareProvider struct{ *fakeProvider }

func (c *ctxAwareProvider) DeleteRecord(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.fakeProvider.DeleteRecord(ctx, id)
}

func TestCreateRecord_RollbackSurvivesShutdownCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &ctxAwareProvider{newFakeProvider()}
	r := reconciler.New(&cancelOnCreateStore{newFakeStore(), cancel}, p, nil, "192.168.1.1", nil, slog.Default())

	err := r.HandleEvent(ctx, docker.Event{
		Type: "start", ContainerID: "c1",
		Labels: map[string]string{"dns.bollard/hostname": "rollback.home.arpa"},
	})
	if err == nil {
		t.Fatal("expected the failed ownership write to be reported")
	}
	if len(p.records) != 0 {
		t.Fatalf("UniFi record left behind with no ownership row: %v", p.records)
	}
}

func TestReconcileAndAdopt_ReservedHostnameRefused(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname":    "Unifi.Home.Arpa.",
		"dns.bollard/ip-override": "10.6.6.6",
	}
	running := map[string]map[string]string{"container-evil": labels}

	t.Run("pass3 vacant name", func(t *testing.T) {
		s, p := newFakeStore(), newFakeProvider()
		r := reconciler.New(s, p, &fakeLister{containers: running}, "192.168.1.1", nil, slog.Default()).
			WithReservedHostnames("unifi.home.arpa")
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if len(p.records) != 0 {
			t.Fatalf("reserved hostname must not be created, got %d records", len(p.records))
		}
	})

	t.Run("adopt vacant name", func(t *testing.T) {
		s, p := newFakeStore(), newFakeProvider()
		r := reconciler.New(s, p, nil, "192.168.1.1", nil, slog.Default()).WithReservedHostnames("unifi.home.arpa")
		if err := r.Adopt(context.Background(), running); err != nil {
			t.Fatalf("Adopt: %v", err)
		}
		if len(p.records) != 0 {
			t.Fatalf("reserved hostname must not be created, got %d records", len(p.records))
		}
	})

	t.Run("adopt existing operator record", func(t *testing.T) {
		s, p := newFakeStore(), newFakeProvider()
		p.records["op-1"] = unifi.DNSRecord{ID: "op-1", Hostname: "unifi.home.arpa", IP: "10.6.6.6", RecordType: "A"}
		r := reconciler.New(s, p, nil, "192.168.1.1", nil, slog.Default()).WithReservedHostnames("unifi.home.arpa")
		if err := r.Adopt(context.Background(), running); err != nil {
			t.Fatalf("Adopt: %v", err)
		}
		if rec, _ := s.GetByContainerID(context.Background(), "container-evil"); rec != nil {
			t.Fatal("operator's controller record must not be adopted")
		}
	})
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
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{"container-other": {}}}, "192.168.1.1", nil, slog.Default())

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

func TestReconcile_EmptyListsDeferDestructivePassesOneTick(t *testing.T) {
	seed := func(s *fakeStore, p *fakeProvider) {
		_ = s.Create(context.Background(), store.Record{
			ID: "rec-e", ContainerID: "container-e",
			Hostname: "e.home.arpa", IP: "192.168.1.1", RecordType: "A",
			TTL: 300, UnifiRecordID: "unifi-e",
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		})
		p.records["unifi-e"] = unifi.DNSRecord{ID: "unifi-e", Hostname: "e.home.arpa", IP: "192.168.1.1"}
	}

	t.Run("empty container list does not delete on first tick", func(t *testing.T) {
		s, p := newFakeStore(), newFakeProvider()
		seed(s, p)
		r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{}}, "192.168.1.1", nil, slog.Default())
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, ok := p.records["unifi-e"]; !ok {
			t.Fatal("record deleted on the first empty-list tick")
		}
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, ok := p.records["unifi-e"]; ok {
			t.Fatal("record not deleted when the empty list repeated")
		}
	})

	t.Run("empty UniFi list does not drop rows on first tick", func(t *testing.T) {
		s, p := newFakeStore(), newFakeProvider()
		seed(s, p)
		delete(p.records, "unifi-e")
		running := map[string]map[string]string{"container-e": {}}
		r := reconciler.New(s, p, &fakeLister{containers: running}, "192.168.1.1", nil, slog.Default())
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if rec, _ := s.GetByContainerID(context.Background(), "container-e"); rec == nil {
			t.Fatal("ownership row dropped on the first empty-list tick")
		}
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if rec, _ := s.GetByContainerID(context.Background(), "container-e"); rec != nil {
			t.Fatal("orphaned row not cleaned when the empty list repeated")
		}
	})
}

// A non-empty answer between two empty ones proves the empty answers were not consecutive, so the
// pass must be deferred again rather than acting on the second empty list.
func TestReconcile_NonEmptyListResetsEmptyCounter(t *testing.T) {
	ctx := context.Background()
	s, p := newFakeStore(), newFakeProvider()
	_ = s.Create(ctx, store.Record{
		ID: "rec-e", ContainerID: "container-e", Hostname: "e.home.arpa", IP: "192.168.1.1",
		RecordType: "A", TTL: 300, UnifiRecordID: "unifi-e", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	p.records["unifi-e"] = unifi.DNSRecord{ID: "unifi-e", Hostname: "e.home.arpa", IP: "192.168.1.1"}
	lister := &fakeLister{containers: map[string]map[string]string{}}
	r := reconciler.New(s, p, lister, "192.168.1.1", nil, slog.Default())

	steps := []struct {
		running     map[string]map[string]string
		wantPresent bool
	}{
		{map[string]map[string]string{}, true},                  // empty: deferred
		{map[string]map[string]string{"container-e": {}}, true}, // non-empty: counter resets
		{map[string]map[string]string{}, true},                  // empty again: deferred, not acted on
		{map[string]map[string]string{}, false},                 // second consecutive empty: acted on
	}
	for i, st := range steps {
		lister.containers = st.running
		if err := r.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if _, ok := p.records["unifi-e"]; ok != st.wantPresent {
			t.Fatalf("step %d: record present=%v, want %v", i, ok, st.wantPresent)
		}
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

func TestAdopt_DuplicateHostnameSkipsSecond(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	p.records["unifi-web"] = unifi.DNSRecord{
		ID: "unifi-web", Hostname: "WEB.Home.Arpa.", IP: "192.168.1.1", RecordType: "A",
	}
	conflicts := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_hostname_conflicts_total"})
	m := &metrics.Metrics{
		HostnameConflictsTotal: conflicts,
		RecordsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_records_total"},
			[]string{"action", "success"}),
	}
	r := reconciler.New(s, p, nil, "192.168.1.1", m, slog.Default())

	running := map[string]map[string]string{
		"container-a": {"dns.bollard/hostname": "web.home.arpa"},
		"container-b": {"dns.bollard/hostname": "Web.home.arpa."},
	}
	if err := r.Adopt(context.Background(), running); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(s.records) != 1 {
		t.Fatalf("expected exactly 1 store row, got %d", len(s.records))
	}
	for _, rec := range s.records {
		if rec.UnifiRecordID != "unifi-web" {
			t.Errorf("expected adopted row to point at unifi-web, got %s", rec.UnifiRecordID)
		}
	}
	var sample dto.Metric
	if err := conflicts.Write(&sample); err != nil {
		t.Fatalf("write conflict metric: %v", err)
	}
	if got := sample.GetCounter().GetValue(); got != 1 {
		t.Errorf("hostname conflicts = %v, want 1", got)
	}
}

func TestAdopt_DuplicateHostnameDifferentIP(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	p.records["unifi-web"] = unifi.DNSRecord{
		ID: "unifi-web", Hostname: "web.home.arpa", IP: "192.168.1.1", RecordType: "A",
	}
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{}}, "192.168.1.1", nil, slog.Default())

	running := map[string]map[string]string{
		"container-a": {"dns.bollard/hostname": "web.home.arpa"},
		// An occupied name with a different IP is not a creation candidate.
		"container-b": {"dns.bollard/hostname": "web.home.arpa", "dns.bollard/ip-override": "10.0.0.5"},
	}
	if err := r.Adopt(context.Background(), running); err != nil {
		t.Fatalf("Adopt must be non-fatal on conflict: %v", err)
	}
	// Only the container whose address matches may own the existing record.
	if len(s.records) != 1 {
		t.Fatalf("expected exactly 1 store row for hostname web, got %d", len(s.records))
	}
	for containerID, rec := range s.records {
		if containerID != "container-a" || rec.UnifiRecordID != "unifi-web" || rec.Hostname != "web.home.arpa" {
			t.Fatalf("unexpected owner %s: %+v", containerID, rec)
		}
	}
}

func TestAdopt_NoDuplicateSingleContainer(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	p.records["unifi-only"] = unifi.DNSRecord{
		ID: "unifi-only", Hostname: "Only.HOME.arpa.", IP: "192.168.1.1", RecordType: "A",
	}
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{}}, "192.168.1.1", nil, slog.Default())

	running := map[string]map[string]string{
		"container-1": {"dns.bollard/hostname": "only.home.arpa"},
	}
	if err := r.Adopt(context.Background(), running); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(s.records) != 1 {
		t.Fatalf("expected 1 store row, got %d", len(s.records))
	}
	if rec, _ := s.GetByContainerID(context.Background(), "container-1"); rec == nil ||
		rec.UnifiRecordID != "unifi-only" || rec.Hostname != "only.home.arpa" {
		t.Fatal("expected container-1 to adopt unifi-only")
	}
}
