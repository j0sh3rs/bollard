package traefik

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/j0sh3rs/bollard/internal/unifi"
)

// stubDNS records what the plugin creates and deletes. With commitThenFail, CreateRecord stores
// the record and then reports a failure, like a POST whose response was lost. With
// rejectAfterOther, another replica's same-address record appears and this POST gets a 409.
type stubDNS struct {
	mu               sync.Mutex
	records          []unifi.DNSRecord
	deleted          []string
	commitThenFail   bool
	rejectAfterOther bool
	duplicate        bool   // the create commits two matching records, so the match is ambiguous
	ip               string // overrides the stored address, to simulate another writer
}

func (s *stubDNS) ListRecords(context.Context) ([]unifi.DNSRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records), nil
}

func (s *stubDNS) CreateRecord(_ context.Context, r unifi.DNSRecord) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ip != "" {
		r.IP = s.ip
	}
	r.ID = fmt.Sprintf("rec-%d", len(s.records)+1)
	if s.rejectAfterOther {
		r.ID = "other-replica"
		s.records = append(s.records, r)
		return "", &unifi.HTTPError{Status: http.StatusConflict, Err: errors.New("duplicate")}
	}
	s.records = append(s.records, r)
	if s.duplicate {
		r.ID += "-dup"
		s.records = append(s.records, r)
	}
	if s.commitThenFail {
		return "", errors.New("response lost")
	}
	return r.ID, nil
}

func (s *stubDNS) DeleteRecord(_ context.Context, id string) error {
	runtime.Gosched()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, id)
	return nil
}

func newStubProvider(t *testing.T, dns unifi.DNSProvider) *Provider {
	t.Helper()
	p, err := New(context.Background(), testConfig("https://controller.home.arpa"), "default")
	if err != nil {
		t.Fatal(err)
	}
	p.client.Transport = routerResponseTransport{}
	p.dns = dns
	return p
}

func reconcileAndStop(t *testing.T, p *Provider) {
	t.Helper()
	if err := p.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

// A create that commits in UniFi but reports an ambiguous error must still be tracked, so Stop deletes it.
func TestReconcileRecoversRecordFromFailedCreate(t *testing.T) {
	dns := &stubDNS{commitThenFail: true}
	reconcileAndStop(t, newStubProvider(t, dns))
	if !slices.Equal(dns.deleted, []string{"rec-1"}) {
		t.Fatalf("deleted %v on Stop, want the record whose create reported an error", dns.deleted)
	}
}

// A lookup after a failed create must never claim a record with a different address.
func TestReconcileDoesNotClaimRecordWithOtherAddress(t *testing.T) {
	dns := &stubDNS{commitThenFail: true, ip: "192.0.2.99"}
	reconcileAndStop(t, newStubProvider(t, dns))
	if len(dns.deleted) != 0 {
		t.Fatalf("Stop deleted %v, which this instance did not create", dns.deleted)
	}
}

// With several replicas on one address, a 409 means another replica's record won. This instance
// must not claim it, or its Stop would delete a live record it did not create.
func TestReconcileDoesNotClaimRecordAfterRejectedCreate(t *testing.T) {
	dns := &stubDNS{rejectAfterOther: true}
	reconcileAndStop(t, newStubProvider(t, dns))
	if len(dns.deleted) != 0 {
		t.Fatalf("Stop deleted %v after a rejected create", dns.deleted)
	}
}

// Concurrent Stop calls must run cleanup once: unsynchronised cleanup panics on the map or deletes twice.
func TestConcurrentStopCleansUpOnce(t *testing.T) {
	dns := &stubDNS{}
	p := newStubProvider(t, dns)
	p.created = map[string]string{}
	for i := range 50 {
		p.created[fmt.Sprintf("id-%02d", i)] = fmt.Sprintf("h%d.home.arpa", i)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Stop(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(dns.deleted) != 50 {
		t.Fatalf("%d deletes for 50 records; cleanup ran more than once", len(dns.deleted))
	}
}

// If two records match, the plugin cannot tell which one it created and must claim neither.
func TestReconcileDoesNotClaimAmbiguousMatch(t *testing.T) {
	dns := &stubDNS{commitThenFail: true, duplicate: true}
	reconcileAndStop(t, newStubProvider(t, dns))
	if len(dns.deleted) != 0 {
		t.Fatalf("Stop deleted %v although the match was ambiguous", dns.deleted)
	}
}

type hostsTransport []string

func (h hostsTransport) RoundTrip(*http.Request) (*http.Response, error) {
	var rules []string
	for _, host := range h {
		rules = append(rules, "Host(`"+host+"`)")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`[{"name":"app@docker","provider":"docker","status":"enabled","rule":"` + strings.Join(rules, " || ") + `"}]`)),
	}, nil
}

// A router rule must not make the plugin create records for its own control-plane hosts.
func TestReconcileSkipsReservedNames(t *testing.T) {
	dns := &stubDNS{}
	p := newStubProvider(t, dns)
	p.client.Transport = hostsTransport{"controller.home.arpa", "localhost", "host.docker.internal", "ok.home.arpa"}
	if err := p.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(dns.records) != 1 || dns.records[0].Hostname != "ok.home.arpa" {
		t.Fatalf("created %+v, want only ok.home.arpa", dns.records)
	}
}
