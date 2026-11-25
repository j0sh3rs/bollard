package reconciler_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/j0sh3rs/bollard/internal/docker"
	"github.com/j0sh3rs/bollard/internal/reconciler"
	"github.com/j0sh3rs/bollard/internal/store"
	"github.com/j0sh3rs/bollard/internal/unifi"
)

func TestHandleStart_RefusesOccupiedName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		typeName string
		disabled bool
	}{
		{name: "enabled A", typeName: "A"},
		{name: "disabled A", typeName: "A", disabled: true},
		{name: "non-A", typeName: "CNAME_RECORD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := newFakeStore(), newFakeProvider()
			external := unifi.DNSRecord{
				ID: "manual", Hostname: "WEB.Home.Arpa.", IP: "192.168.1.1", RecordType: tc.typeName, Disabled: tc.disabled,
			}
			p.records[external.ID] = external
			r := reconciler.New(s, p, nil, "192.168.1.1", nil, slog.Default())
			err := r.HandleEvent(context.Background(), docker.Event{
				Type: "start", ContainerID: "web",
				Labels: map[string]string{"dns.bollard/hostname": "web.home.arpa"},
			})
			if err == nil {
				t.Fatal("expected conflict for occupied hostname")
			}
			if p.nextID != 0 || len(s.records) != 0 || len(p.records) != 1 || p.records[external.ID] != external {
				t.Fatalf("changed ownership or DNS: store=%+v, DNS=%+v", s.records, p.records)
			}
		})
	}
}

func TestHandleStart_PreservesExistingContainerOwnership(t *testing.T) {
	ctx := context.Background()
	s, p := newFakeStore(), newFakeProvider()
	r := reconciler.New(s, p, nil, "192.168.1.1", nil, slog.Default())
	owner := store.Record{ID: "existing", ContainerID: "owner", Hostname: "WEB.Home.Arpa.", UnifiRecordID: "old"}
	s.records[owner.ContainerID] = owner
	e := docker.Event{Type: "start", ContainerID: "other", Labels: map[string]string{"dns.bollard/hostname": "web.home.arpa"}}
	if err := r.HandleEvent(ctx, e); err == nil {
		t.Fatal("expected canonical store ownership conflict")
	}
	e.ContainerID = owner.ContainerID
	for _, host := range []string{"web.home.arpa", "changed.home.arpa"} {
		e.Labels["dns.bollard/hostname"] = host
		if err := r.HandleEvent(ctx, e); err != nil {
			t.Fatalf("repeat start for %q: %v", host, err)
		}
	}
	if len(s.records) != 1 || s.records[owner.ContainerID] != owner || p.nextID != 0 {
		t.Fatalf("replaced existing ownership: %+v, DNS create calls=%d", s.records, p.nextID)
	}
}

func TestReconcile_RecoversOnlyVacantNamesFromOneSnapshot(t *testing.T) {
	ctx := context.Background()
	s, p := newFakeStore(), newFakeProvider()
	external := unifi.DNSRecord{ID: "manual", Hostname: "EXTERNAL.home.arpa.", RecordType: "CNAME_RECORD", Disabled: true}
	p.records[external.ID] = external
	running := map[string]map[string]string{
		"unowned": {"dns.bollard/hostname": "external.home.arpa"},
		"first":   {"dns.bollard/hostname": "Shared.home.arpa."},
		"second":  {"dns.bollard/hostname": "shared.home.arpa"},
		"other":   {"dns.bollard/hostname": "other.home.arpa"},
	}
	r := reconciler.New(s, p, &fakeLister{containers: running}, "192.168.1.1", nil, slog.Default())
	if err := r.HandleEvent(ctx, docker.Event{Type: "stop", ContainerID: "unowned", Labels: running["unowned"]}); err != nil {
		t.Fatalf("stop unowned container: %v", err)
	}
	if p.records[external.ID] != external {
		t.Fatal("stop removed a DNS record with no store row")
	}
	p.listCalls = 0
	if err := r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if p.listCalls != 1 || p.nextID != 2 || len(s.records) != 2 || len(p.records) != 3 {
		t.Fatalf("lists=%d creates=%d store=%+v DNS=%+v", p.listCalls, p.nextID, s.records, p.records)
	}
	if _, owned := s.records["unowned"]; owned || p.records[external.ID] != external {
		t.Fatal("reconcile claimed or changed external DNS")
	}
	if s.records["other"].Hostname != "other.home.arpa" ||
		(s.records["first"].Hostname != "shared.home.arpa" && s.records["second"].Hostname != "shared.home.arpa") {
		t.Fatalf("reconcile did not register both vacant names: %+v", s.records)
	}
}

func TestReconcile_DeletionFreesOnlyTheDeletedPolicy(t *testing.T) {
	for _, external := range []bool{false, true} {
		name := "sole policy"
		if external {
			name = "another policy remains"
		}
		t.Run(name, func(t *testing.T) {
			s, p := newFakeStore(), newFakeProvider()
			s.records["stopped"] = store.Record{
				ID: "old", ContainerID: "stopped", Hostname: "WEB.Home.Arpa.", UnifiRecordID: "owned",
			}
			p.records["owned"] = unifi.DNSRecord{ID: "owned", Hostname: "web.home.arpa", RecordType: "A", IP: "192.168.1.1"}
			manual := unifi.DNSRecord{ID: "manual", Hostname: "web.HOME.arpa.", RecordType: "A", Disabled: true}
			if external {
				p.records[manual.ID] = manual
			}
			lister := &fakeLister{containers: map[string]map[string]string{
				"new": {"dns.bollard/hostname": "Web.Home.Arpa"},
			}}
			r := reconciler.New(s, p, lister, "192.168.1.1", nil, slog.Default())
			if err := r.Reconcile(context.Background()); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			_, stillOwned := p.records["owned"]
			_, staleRow := s.records["stopped"]
			if p.listCalls != 1 || stillOwned || staleRow {
				t.Fatalf("failed to delete owned policy via one snapshot: lists=%d, store=%+v, DNS=%+v", p.listCalls, s.records, p.records)
			}
			if external {
				if p.nextID != 0 || len(s.records) != 0 || p.records[manual.ID] != manual {
					t.Fatalf("freed an occupied name: store=%+v, DNS=%+v", s.records, p.records)
				}
			} else if p.nextID != 1 || len(s.records) != 1 || s.records["new"].Hostname != "web.home.arpa" {
				t.Fatalf("did not reuse free hostname: store=%+v, DNS=%+v", s.records, p.records)
			}
		})
	}
}

func TestReconcile_KeepsNameOccupiedWhenCreatedRecordMayRemain(t *testing.T) {
	for _, tc := range []string{"lost create response", "failed rollback"} {
		t.Run(tc, func(t *testing.T) {
			s, p := newFakeStore(), newFakeProvider()
			if tc == "lost create response" {
				p.createResponseErr = fmt.Errorf("create response lost")
			} else {
				s.createErr = fmt.Errorf("store write failed")
				p.deleteErr = fmt.Errorf("rollback failed")
			}
			lister := &fakeLister{containers: map[string]map[string]string{
				"first":  {"dns.bollard/hostname": "web.home.arpa"},
				"second": {"dns.bollard/hostname": "web.home.arpa"},
			}}
			r := reconciler.New(s, p, lister, "192.168.1.1", nil, slog.Default())
			if err := r.Reconcile(context.Background()); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if p.listCalls != 1 || p.nextID != 1 || len(p.records) != 1 || len(s.records) != 0 {
				t.Fatalf("lost occupancy: lists=%d, creates=%d, store=%+v, DNS=%+v",
					p.listCalls, p.nextID, s.records, p.records)
			}
		})
	}
}

func TestOwnership_FailsClosedWhenDNSListFails(t *testing.T) {
	ctx := context.Background()
	s, p := newFakeStore(), newFakeProvider()
	p.listErr = fmt.Errorf("DNS unavailable")
	owned := unifi.DNSRecord{ID: "owned", Hostname: "stopped.home.arpa", RecordType: "A"}
	p.records[owned.ID] = owned
	row := store.Record{ID: "old", ContainerID: "stopped", Hostname: owned.Hostname, UnifiRecordID: owned.ID}
	s.records[row.ContainerID] = row
	running := map[string]map[string]string{"new": {"dns.bollard/hostname": "new.home.arpa"}}
	r := reconciler.New(s, p, &fakeLister{containers: running}, "192.168.1.1", nil, slog.Default())
	if err := r.HandleEvent(ctx, docker.Event{Type: "start", ContainerID: "new", Labels: running["new"]}); err == nil {
		t.Error("start must fail when it cannot check occupancy")
	}
	if err := r.Reconcile(ctx); err == nil {
		t.Error("reconcile must fail when it cannot list DNS records")
	}
	if err := r.Adopt(ctx, running); err == nil {
		t.Error("adopt must fail when it cannot list DNS records")
	}
	if p.nextID != 0 || len(s.records) != 1 || s.records[row.ContainerID] != row || len(p.records) != 1 || p.records[owned.ID] != owned {
		t.Fatalf("changed ownership or DNS: store=%+v, DNS=%+v", s.records, p.records)
	}
}

func TestAdopt_RejectsIneligibleOccupiedNames(t *testing.T) {
	match := unifi.DNSRecord{ID: "match", Hostname: "web.home.arpa", IP: "192.168.1.1", RecordType: "A"}
	disabled, cname, unknown, wrongIP, duplicate, missingID := match, match, match, match, match, match
	disabled.ID, disabled.Disabled = "disabled", true
	cname.ID, cname.RecordType = "cname", "CNAME_RECORD"
	unknown.ID, unknown.RecordType = "unknown", ""
	wrongIP.ID, wrongIP.IP = "wrong-ip", "192.168.1.2"
	duplicate.ID, duplicate.Hostname = "duplicate", "WEB.home.arpa."
	missingID.ID = ""
	for _, tc := range []struct {
		name    string
		records []unifi.DNSRecord
	}{
		{"disabled", []unifi.DNSRecord{disabled}},
		{"non-A", []unifi.DNSRecord{cname}},
		{"unknown type", []unifi.DNSRecord{unknown}},
		{"missing ID", []unifi.DNSRecord{missingID}},
		{"different address", []unifi.DNSRecord{wrongIP}},
		{"duplicate exact match", []unifi.DNSRecord{match, duplicate}},
		{"multiple addresses", []unifi.DNSRecord{match, wrongIP}},
		{"disabled sibling", []unifi.DNSRecord{match, disabled}},
		{"non-A sibling", []unifi.DNSRecord{match, cname}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := newFakeStore(), newFakeProvider()
			for _, rec := range tc.records {
				p.records[rec.ID] = rec
			}
			r := reconciler.New(s, p, nil, "192.168.1.1", nil, slog.Default())
			running := map[string]map[string]string{"new": {"dns.bollard/hostname": "web.home.arpa"}}
			if err := r.Adopt(context.Background(), running); err != nil {
				t.Fatalf("Adopt: %v", err)
			}
			if p.nextID != 0 || len(s.records) != 0 || len(p.records) != len(tc.records) {
				t.Fatalf("changed ownership or DNS: store=%+v, DNS=%+v", s.records, p.records)
			}
			for _, rec := range tc.records {
				if p.records[rec.ID] != rec {
					t.Errorf("changed external record %q", rec.ID)
				}
			}
		})
	}
}

func TestAdopt_PreservesOwnershipByCanonicalNameOrRecordID(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hostname string
		unifiID  string
	}{
		{"canonical name", "WEB.Home.Arpa.", "old-id"},
		{"record ID", "renamed.home.arpa", "existing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := newFakeStore(), newFakeProvider()
			old := store.Record{ID: "old", ContainerID: "owner", Hostname: tc.hostname, UnifiRecordID: tc.unifiID}
			s.records[old.ContainerID] = old
			p.records["existing"] = unifi.DNSRecord{ID: "existing", Hostname: "web.home.arpa", IP: "192.168.1.1", RecordType: "A"}
			r := reconciler.New(s, p, nil, "192.168.1.1", nil, slog.Default())
			running := map[string]map[string]string{
				"owner":     {"dns.bollard/hostname": "changed.home.arpa"},
				"contender": {"dns.bollard/hostname": "web.home.arpa"},
				"vacant":    {"dns.bollard/hostname": "free.home.arpa"},
			}
			if err := r.Adopt(context.Background(), running); err != nil {
				t.Fatalf("Adopt: %v", err)
			}
			if p.listCalls != 1 || p.nextID != 1 || len(s.records) != 2 || s.records[old.ContainerID] != old ||
				s.records["vacant"].Hostname != "free.home.arpa" {
				t.Fatalf("changed existing ownership or failed to create vacant name: lists=%d, store=%+v, DNS=%+v",
					p.listCalls, s.records, p.records)
			}
		})
	}
}
