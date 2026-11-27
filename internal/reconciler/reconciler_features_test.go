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

func (f *fakeProvider) UpdateRecord(_ context.Context, id string, r unifi.DNSRecord) error {
	if _, ok := f.records[id]; !ok {
		return fmt.Errorf("no record %s", id)
	}
	r.ID = id
	f.records[id] = r
	f.updates = append(f.updates, r)
	return nil
}

type fakeNetworks struct {
	nets map[string]map[string]docker.NetworkAddr // container -> network name -> address
	err  error
}

func (f *fakeNetworks) ContainerNetworks(_ context.Context, id string) (map[string]docker.NetworkAddr, error) {
	return f.nets[id], f.err
}

func start(id string, labels map[string]string) docker.Event {
	return docker.Event{Type: "start", ContainerID: id, Name: id, Labels: labels}
}

func stop(id string) docker.Event { return docker.Event{Type: "stop", ContainerID: id, Name: id} }

func newRec(s *fakeStore, p *fakeProvider, running map[string]map[string]string) *reconciler.Reconciler {
	return reconciler.New(s, p, &fakeLister{containers: running}, "192.168.1.1", nil, slog.Default())
}

func TestMultipleHostnamesCreateAndDeleteTogether(t *testing.T) {
	ctx := context.Background()
	s, p := newFakeStore(), newFakeProvider()
	r := newRec(s, p, map[string]map[string]string{})
	labels := map[string]string{"dns.bollard/hostname": "a.home.arpa, b.home.arpa"}
	if err := r.HandleEvent(ctx, start("c1", labels)); err != nil {
		t.Fatal(err)
	}
	if len(p.records) != 2 || len(s.records) != 2 {
		t.Fatalf("records=%d rows=%d, want 2 and 2", len(p.records), len(s.records))
	}
	if err := r.HandleEvent(ctx, stop("c1")); err != nil {
		t.Fatal(err)
	}
	if len(p.records) != 0 || len(s.records) != 0 {
		t.Fatalf("after stop records=%d rows=%d, want none", len(p.records), len(s.records))
	}
}

// One of several hostnames being taken must not block the others, and the failure must surface.
func TestMultipleHostnamesPartialConflict(t *testing.T) {
	ctx := context.Background()
	s, p := newFakeStore(), newFakeProvider()
	p.records["x"] = unifi.DNSRecord{ID: "x", Hostname: "b.home.arpa", RecordType: "A", IP: "10.0.0.1"}
	r := newRec(s, p, map[string]map[string]string{})
	err := r.HandleEvent(ctx, start("c1", map[string]string{"dns.bollard/hostname": "a.home.arpa,b.home.arpa"}))
	if err == nil {
		t.Fatal("the occupied name must be reported")
	}
	rows, _ := s.ListByContainerID(ctx, "c1")
	if len(rows) != 1 || rows[0].Hostname != "a.home.arpa" {
		t.Fatalf("rows = %+v, want only a.home.arpa", rows)
	}
}

// A container whose second hostname failed earlier is repaired by the next reconcile tick.
func TestReconcileRecoversMissingHostnameOfPartialContainer(t *testing.T) {
	ctx := context.Background()
	s, p := newFakeStore(), newFakeProvider()
	labels := map[string]string{"dns.bollard/hostname": "a.home.arpa,b.home.arpa"}
	r := newRec(s, p, map[string]map[string]string{"c1": labels})
	p.records["u1"] = unifi.DNSRecord{ID: "u1", Hostname: "a.home.arpa", RecordType: "A", IP: "192.168.1.1"}
	_ = s.Create(ctx, store.Record{ID: "r1", ContainerID: "c1", Hostname: "a.home.arpa", RecordType: "A", IP: "192.168.1.1", UnifiRecordID: "u1"})
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListByContainerID(ctx, "c1")
	if len(rows) != 2 || len(p.records) != 2 {
		t.Fatalf("rows=%d records=%d, want 2 and 2", len(rows), len(p.records))
	}
}

func TestCNAMERecord(t *testing.T) {
	ctx := context.Background()
	s, p := newFakeStore(), newFakeProvider()
	r := newRec(s, p, map[string]map[string]string{})
	err := r.HandleEvent(ctx, start("c1", map[string]string{
		"dns.bollard/hostname": "www.home.arpa", "dns.bollard/record-type": "CNAME", "dns.bollard/target": "App.Home.Arpa.",
	}))
	if err != nil {
		t.Fatal(err)
	}
	rec := p.records["unifi-1"]
	if rec.RecordType != "CNAME" || rec.Target != "app.home.arpa" || rec.IP != "" {
		t.Fatalf("created %+v", rec)
	}
	row, _ := s.GetByContainerID(ctx, "c1")
	if row == nil || row.RecordType != "CNAME" || row.Target != "app.home.arpa" || row.IP != "" {
		t.Fatalf("row %+v", row)
	}
	if err := r.HandleEvent(ctx, stop("c1")); err != nil || len(p.records) != 0 {
		t.Fatalf("stop: %v, records left %d", err, len(p.records))
	}
}

func TestAAAARecord(t *testing.T) {
	ctx := context.Background()
	s, p := newFakeStore(), newFakeProvider()
	r := newRec(s, p, map[string]map[string]string{})
	err := r.HandleEvent(ctx, start("c1", map[string]string{
		"dns.bollard/hostname": "v6.home.arpa", "dns.bollard/record-type": "AAAA", "dns.bollard/ip-override": "fd00::5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if rec := p.records["unifi-1"]; rec.RecordType != "AAAA" || rec.IP != "fd00::5" {
		t.Fatalf("created %+v", rec)
	}
}

func TestNetworkSelection(t *testing.T) {
	bridge := docker.NetworkAddr{Driver: "bridge", IPv4: "172.17.0.2"}
	mac := func(ip string) docker.NetworkAddr {
		return docker.NetworkAddr{Driver: "macvlan", IPv4: ip, IPv6: "fd00::" + ip[len(ip)-2:]}
	}
	for name, tc := range map[string]struct {
		labels  map[string]string
		nets    map[string]docker.NetworkAddr
		netsErr error
		want    string
		wantErr bool
	}{
		"host IP when nothing is routable": {nil, map[string]docker.NetworkAddr{"bridge": bridge}, nil, "192.168.1.1", false},
		"only macvlan network wins":        {nil, map[string]docker.NetworkAddr{"bridge": bridge, "lan": mac("192.168.5.20")}, nil, "192.168.5.20", false},
		"several macvlan is ambiguous":     {nil, map[string]docker.NetworkAddr{"a": mac("192.168.5.20"), "b": mac("192.168.6.20")}, nil, "", true},
		"network label picks bridge":       {map[string]string{"dns.bollard/network": "bridge"}, map[string]docker.NetworkAddr{"bridge": bridge, "lan": mac("192.168.5.20")}, nil, "172.17.0.2", false},
		"network label resolves ambiguity": {map[string]string{"dns.bollard/network": "b"}, map[string]docker.NetworkAddr{"a": mac("192.168.5.20"), "b": mac("192.168.6.20")}, nil, "192.168.6.20", false},
		"unknown network label":            {map[string]string{"dns.bollard/network": "nope"}, map[string]docker.NetworkAddr{"bridge": bridge}, nil, "", true},
		"override beats network":           {map[string]string{"dns.bollard/ip-override": "10.9.9.9"}, map[string]docker.NetworkAddr{"lan": mac("192.168.5.20")}, nil, "10.9.9.9", false},
		"inspect failure fails closed":     {nil, nil, fmt.Errorf("docker down"), "", true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, p := newFakeStore(), newFakeProvider()
			r := newRec(s, p, map[string]map[string]string{}).
				WithNetworks(&fakeNetworks{nets: map[string]map[string]docker.NetworkAddr{"c1": tc.nets}, err: tc.netsErr})
			labels := map[string]string{"dns.bollard/hostname": "a.home.arpa"}
			for k, v := range tc.labels {
				labels[k] = v
			}
			err := r.HandleEvent(ctx, start("c1", labels))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && p.records["unifi-1"].IP != tc.want {
				t.Fatalf("published %q, want %q", p.records["unifi-1"].IP, tc.want)
			}
			if err != nil && len(p.records) != 0 {
				t.Fatal("a record was created despite the error")
			}
		})
	}
}

func TestTraefikInference(t *testing.T) {
	ctx := context.Background()
	labels := map[string]string{"traefik.http.routers.web.rule": "Host(`app.home.arpa`) || Host(`www.home.arpa`)"}
	off, offP := newFakeStore(), newFakeProvider()
	if err := newRec(off, offP, nil).HandleEvent(ctx, start("c1", labels)); err != nil || len(offP.records) != 0 {
		t.Fatalf("inference off: err=%v records=%d, want none", err, len(offP.records))
	}
	on, onP := newFakeStore(), newFakeProvider()
	r := newRec(on, onP, nil).WithTraefikInference()
	if err := r.HandleEvent(ctx, start("c1", labels)); err != nil || len(onP.records) != 2 {
		t.Fatalf("inference on: err=%v records=%d, want 2", err, len(onP.records))
	}
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func drainFixture(t *testing.T) (*fakeStore, *fakeProvider, *reconciler.Reconciler, *clock, map[string]map[string]string) {
	t.Helper()
	ctx := context.Background()
	s, p := newFakeStore(), newFakeProvider()
	clk := &clock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	running := map[string]map[string]string{"other": {}}
	r := newRec(s, p, running).WithStopDrain(30 * time.Second).WithClock(clk.Now)
	labels := map[string]string{"dns.bollard/hostname": "web.home.arpa"}
	if err := r.HandleEvent(ctx, start("old", labels)); err != nil {
		t.Fatal(err)
	}
	return s, p, r, clk, running
}

func TestStopDrainKeepsRecordWithShortTTLThenDeletes(t *testing.T) {
	ctx := context.Background()
	s, p, r, clk, _ := drainFixture(t)
	if err := r.HandleEvent(ctx, stop("old")); err != nil {
		t.Fatal(err)
	}
	if len(p.records) != 1 || len(p.updates) != 1 || p.updates[0].TTL != 10 {
		t.Fatalf("records=%d updates=%+v, want the record kept with TTL 10", len(p.records), p.updates)
	}
	row, _ := s.GetByContainerID(ctx, "old")
	if row == nil || !row.DrainUntil.Equal(clk.now.Add(30*time.Second)) || row.TTL != 300 {
		t.Fatalf("row %+v, want draining until +30s with its original TTL", row)
	}

	clk.now = clk.now.Add(20 * time.Second)
	if err := r.Reconcile(ctx); err != nil || len(p.records) != 1 {
		t.Fatalf("deleted before the drain ended: err=%v records=%d", err, len(p.records))
	}
	clk.now = clk.now.Add(11 * time.Second)
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(p.records) != 0 || len(s.records) != 0 {
		t.Fatalf("after the drain records=%d rows=%d, want none", len(p.records), len(s.records))
	}
}

// The point of draining: a restarted container takes the same record over with no gap.
func TestStartTakesOverDrainingRecord(t *testing.T) {
	ctx := context.Background()
	s, p, r, _, _ := drainFixture(t)
	if err := r.HandleEvent(ctx, stop("old")); err != nil {
		t.Fatal(err)
	}
	r2 := r.WithNetworks(&fakeNetworks{nets: map[string]map[string]docker.NetworkAddr{"new": {"lan": {Driver: "macvlan", IPv4: "192.168.5.20"}}}})
	if err := r2.HandleEvent(ctx, start("new", map[string]string{"dns.bollard/hostname": "web.home.arpa"})); err != nil {
		t.Fatal(err)
	}
	if p.nextID != 1 {
		t.Fatalf("created %d records, want the draining one reused", p.nextID)
	}
	last := p.updates[len(p.updates)-1]
	if last.TTL != 300 || last.IP != "192.168.5.20" {
		t.Fatalf("takeover update %+v, want original TTL and the new address", last)
	}
	row, _ := s.GetByContainerID(ctx, "new")
	if row == nil || !row.DrainUntil.IsZero() || row.IP != "192.168.5.20" {
		t.Fatalf("row %+v, want owned by the new container and no longer draining", row)
	}
	if old, _ := s.GetByContainerID(ctx, "old"); old != nil {
		t.Fatalf("old container still has a row: %+v", old)
	}
}

// If the start event was missed, the reconcile tick must still hand the record over.
func TestReconcileTakesOverDrainingRecordForRunningContainer(t *testing.T) {
	ctx := context.Background()
	s, p, r, _, running := drainFixture(t)
	if err := r.HandleEvent(ctx, stop("old")); err != nil {
		t.Fatal(err)
	}
	running["new"] = map[string]string{"dns.bollard/hostname": "web.home.arpa"}
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ := s.GetByContainerID(ctx, "new")
	if row == nil || !row.DrainUntil.IsZero() || p.nextID != 1 {
		t.Fatalf("row=%+v creates=%d, want the draining record handed over", row, p.nextID)
	}
}

func TestStopDrainIgnoredWithoutUpdater(t *testing.T) {
	ctx := context.Background()
	s := newFakeStore()
	p := &noUpdateProvider{fakeProvider: newFakeProvider()}
	r := reconciler.New(s, p, &fakeLister{containers: map[string]map[string]string{}}, "192.168.1.1", nil, slog.Default()).
		WithStopDrain(30 * time.Second)
	if err := r.HandleEvent(ctx, start("c1", map[string]string{"dns.bollard/hostname": "a.home.arpa"})); err != nil {
		t.Fatal(err)
	}
	if err := r.HandleEvent(ctx, stop("c1")); err != nil {
		t.Fatal(err)
	}
	if len(p.records) != 0 {
		t.Fatalf("record kept although the provider cannot update it: %d", len(p.records))
	}
}

// noUpdateProvider hides the embedded provider's UpdateRecord, like a DNSProvider without RecordUpdater.
type noUpdateProvider struct{ *fakeProvider }

func (noUpdateProvider) UpdateRecord() {}

func TestAdoptMatchesCNAMEAndAAAAByValue(t *testing.T) {
	for name, tc := range map[string]struct {
		existing unifi.DNSRecord
		labels   map[string]string
		adopted  bool
	}{
		"cname same target": {
			unifi.DNSRecord{ID: "u1", Hostname: "www.home.arpa", RecordType: "CNAME", Target: "app.home.arpa"},
			map[string]string{"dns.bollard/hostname": "www.home.arpa", "dns.bollard/record-type": "CNAME", "dns.bollard/target": "app.home.arpa"}, true,
		},
		"cname other target": {
			unifi.DNSRecord{ID: "u1", Hostname: "www.home.arpa", RecordType: "CNAME", Target: "other.home.arpa"},
			map[string]string{"dns.bollard/hostname": "www.home.arpa", "dns.bollard/record-type": "CNAME", "dns.bollard/target": "app.home.arpa"}, false,
		},
		"aaaa same address": {
			unifi.DNSRecord{ID: "u1", Hostname: "v6.home.arpa", RecordType: "AAAA", IP: "FD00:0:0:0:0:0:0:5"},
			map[string]string{"dns.bollard/hostname": "v6.home.arpa", "dns.bollard/record-type": "AAAA", "dns.bollard/ip-override": "fd00::5"}, true,
		},
		"a record does not satisfy aaaa": {
			unifi.DNSRecord{ID: "u1", Hostname: "v6.home.arpa", RecordType: "A", IP: "192.168.1.1"},
			map[string]string{"dns.bollard/hostname": "v6.home.arpa", "dns.bollard/record-type": "AAAA", "dns.bollard/ip-override": "fd00::5"}, false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, p := newFakeStore(), newFakeProvider()
			p.records["u1"] = tc.existing
			r := newRec(s, p, nil)
			if err := r.Adopt(ctx, map[string]map[string]string{"c1": tc.labels}); err != nil {
				t.Fatal(err)
			}
			row, _ := s.GetByContainerID(ctx, "c1")
			if (row != nil) != tc.adopted {
				t.Fatalf("adopted = %v, want %v (row %+v)", row != nil, tc.adopted, row)
			}
			if p.nextID != 0 {
				t.Fatal("adopt created a record for an occupied name")
			}
		})
	}
}
