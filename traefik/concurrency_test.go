package traefik

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/j0sh3rs/bollard/internal/unifi"
)

const (
	raceHost    = "app.home.arpa"
	raceBarrier = 5 * time.Second
	raceTimeout = 15 * time.Second
)

// raceKeys maps each writer's API key to its target address.
var raceKeys = map[string]string{"key-a": "192.0.2.10", "key-b": "192.0.2.20"}

// raceAPI captures a plugin and a remote writer's empty snapshots, then lets the remote
// writer POST first. The remote writer bypasses the plugin's process-local guard.
type raceAPI struct {
	t               *testing.T
	srv             *httptest.Server
	allowDuplicates bool          // false: a POST for an existing domain gets 409
	release         chan struct{} // closed once both callers have listed
	remoteDone      chan struct{} // closed after the simulated remote writer POSTs

	mu       sync.Mutex
	callers  map[string]int // API key -> remote record count in its first list snapshot
	records  []map[string]any
	requests []string // "METHOD path" for every UniFi call
	posts    []racePost
}

type racePost struct {
	caller string
	policy map[string]any
	status int
}

func newRaceAPI(t *testing.T, allowDuplicates bool) *raceAPI {
	a := &raceAPI{
		t: t, allowDuplicates: allowDuplicates, release: make(chan struct{}),
		remoteDone: make(chan struct{}), callers: map[string]int{},
	}
	a.srv = httptest.NewServer(http.HandlerFunc(a.handle))
	t.Cleanup(a.srv.Close)
	return a
}

const (
	racePolicies = "/proxy/network/integration/v1/sites/site-id/dns/policies"
	raceSites    = "/proxy/network/integration/v1/sites"
)

func (a *raceAPI) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/http/routers" {
		json.NewEncoder(w).Encode([]map[string]string{{
			"name": "app@docker", "provider": "docker", "status": "enabled", "rule": "Host(`" + raceHost + "`)",
		}})
		return
	}
	caller := r.Header.Get("X-API-KEY")
	if _, ok := raceKeys[caller]; !ok {
		a.t.Errorf("UniFi request with unknown API key %q", caller)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	a.mu.Lock()
	a.requests = append(a.requests, r.Method+" "+r.URL.Path)
	a.mu.Unlock()
	switch {
	case r.URL.Path == raceSites && r.Method == http.MethodGet:
		sites := []map[string]any{
			{"id": "other-id", "name": "Other", "internalReference": "other"},
			{"id": "site-id", "name": "Default", "internalReference": "default"},
		}
		json.NewEncoder(w).Encode(map[string]any{"data": sites, "count": len(sites), "totalCount": len(sites)})
	case r.URL.Path == racePolicies && r.Method == http.MethodGet:
		a.list(w, r, caller)
	case r.URL.Path == racePolicies && r.Method == http.MethodPost:
		a.create(w, r, caller)
	default:
		a.t.Errorf("unexpected UniFi request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *raceAPI) list(w http.ResponseWriter, r *http.Request, caller string) {
	a.mu.Lock()
	snapshot := append([]map[string]any{}, a.records...)
	if _, seen := a.callers[caller]; !seen {
		a.callers[caller] = len(snapshot)
		if len(a.callers) == len(raceKeys) {
			close(a.release)
		}
	}
	a.mu.Unlock()
	select {
	case <-a.release:
	case <-r.Context().Done():
		return
	case <-time.After(raceBarrier):
		a.t.Errorf("barrier: %s listed but the other writer never did", caller)
		w.WriteHeader(http.StatusRequestTimeout) // 4xx: the client does not retry, so failure stays fast
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"data": snapshot, "count": len(snapshot), "totalCount": len(snapshot)})
}

func (a *raceAPI) create(w http.ResponseWriter, r *http.Request, caller string) {
	var policy map[string]any
	if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
		a.t.Errorf("decode POST body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if caller == "key-a" {
		select {
		case <-a.remoteDone:
		case <-r.Context().Done():
			return
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	select {
	case <-a.release:
	default:
		a.t.Errorf("%s POSTed before both writers listed", caller)
	}
	post := racePost{caller: caller, policy: maps.Clone(policy), status: http.StatusOK}
	if !a.allowDuplicates && slices.ContainsFunc(a.records, func(rec map[string]any) bool { return rec["domain"] == policy["domain"] }) {
		post.status = http.StatusConflict
		a.posts = append(a.posts, post)
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"statusCode":409,"statusName":"CONFLICT","message":"domain already exists"}`)
		return
	}
	policy["id"] = fmt.Sprintf("id-%d", len(a.records)+1)
	a.records = append(a.records, policy)
	a.posts = append(a.posts, post)
	json.NewEncoder(w).Encode(policy)
}

func (a *raceAPI) provider(ctx context.Context, key string) (*Provider, *bytes.Buffer) {
	a.t.Helper()
	c := testConfig(a.srv.URL)
	c.UnifiAPIKey = key
	c.Address = raceKeys[key]
	p, err := New(ctx, c, key)
	if err != nil {
		a.t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	p.logger = log.New(logs, "", 0)
	return p, logs
}

func (a *raceAPI) snapshot() (posts []racePost, records []map[string]any, callers map[string]int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	records = make([]map[string]any, len(a.records))
	for i, rec := range a.records {
		records[i] = maps.Clone(rec)
	}
	return slices.Clone(a.posts), records, maps.Clone(a.callers)
}

// assertCreateOnly fails on any UniFi call that is not a GET or a POST.
func (a *raceAPI) assertCreateOnly() {
	a.t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.requests {
		if m := strings.SplitN(r, " ", 2)[0]; m != http.MethodGet && m != http.MethodPost {
			a.t.Fatalf("destructive UniFi call %q", r)
		}
	}
}

func raceContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), raceTimeout)
}

func (a *raceAPI) reconcileAgainstRemote(ctx context.Context, p *Provider) {
	a.t.Helper()
	remote := make(chan error, 1)
	go func() { remote <- a.remoteCreate(ctx) }()
	err := p.reconcile(ctx)
	if remoteErr := <-remote; remoteErr != nil {
		a.t.Fatalf("remote writer: %v", remoteErr)
	}
	if err != nil {
		a.t.Fatalf("plugin reconcile: %v", err)
	}
}

func (a *raceAPI) remoteCreate(ctx context.Context) error {
	defer close(a.remoteDone)
	dns, err := unifi.New(ctx, &unifi.Config{
		Host: a.srv.URL, APIKey: "key-b", Site: "site-id", IncludeAllRecords: true,
	})
	if err != nil {
		return err
	}
	if closer, ok := dns.(interface{ CloseIdleConnections() }); ok {
		defer closer.CloseIdleConnections()
	}
	records, err := dns.ListRecords(ctx)
	if err != nil {
		return err
	}
	if len(records) != 0 {
		return fmt.Errorf("expected an empty snapshot, got %v", records)
	}
	_, err = dns.CreateRecord(ctx, unifi.DNSRecord{
		Hostname: raceHost, IP: raceKeys["key-b"], RecordType: "A", TTL: 300,
	})
	return err
}

// assertResyncIsQuiet runs the provider once more: no POST, no change to
// the remote records, no destructive call.
func assertResyncIsQuiet(ctx context.Context, t *testing.T, a *raceAPI, p *Provider) {
	t.Helper()
	postsBefore, recordsBefore, _ := a.snapshot()
	if err := p.reconcile(ctx); err != nil {
		t.Fatalf("plugin resync: %v", err)
	}
	posts, records, _ := a.snapshot()
	if len(posts) != len(postsBefore) {
		t.Fatalf("resync POSTed %d more record(s): %v", len(posts)-len(postsBefore), posts[len(postsBefore):])
	}
	if !reflect.DeepEqual(records, recordsBefore) {
		t.Fatalf("resync changed remote records: %v, was %v", records, recordsBefore)
	}
	a.assertCreateOnly()
}

func wantPolicy(ip string) map[string]any {
	return map[string]any{"type": "A_RECORD", "enabled": true, "domain": raceHost, "ipv4Address": ip, "ttlSeconds": float64(300)}
}

// assertBothListedEmpty proves both writers acted on an empty zone.
func assertBothListedEmpty(t *testing.T, callers map[string]int) {
	t.Helper()
	if want := map[string]int{"key-a": 0, "key-b": 0}; !reflect.DeepEqual(callers, want) {
		t.Fatalf("first list snapshots (records seen per writer) = %v, want %v", callers, want)
	}
}

// Both writers see an empty zone. The remote writer wins and the plugin receives a 409.
// The plugin logs the failure and does not retry or overwrite the record.
func TestConcurrentCreatesServerRejectsDuplicate(t *testing.T) {
	ctx, cancel := raceContext()
	defer cancel()
	api := newRaceAPI(t, false)
	p, logs := api.provider(ctx, "key-a")

	api.reconcileAgainstRemote(ctx, p)

	posts, records, callers := api.snapshot()
	assertBothListedEmpty(t, callers)
	if len(posts) != 2 || posts[0].caller != "key-b" || posts[0].status != http.StatusOK ||
		posts[1].caller != "key-a" || posts[1].status != http.StatusConflict {
		t.Fatalf("want one remote POST (200) and one plugin POST (409), got %v", posts)
	}
	for _, p := range posts {
		if want := wantPolicy(raceKeys[p.caller]); !reflect.DeepEqual(p.policy, want) {
			t.Fatalf("%s POST payload %v, want %v", p.caller, p.policy, want)
		}
	}

	if len(records) != 1 {
		t.Fatalf("remote records = %v, want exactly one", records)
	}
	if id, _ := records[0]["id"].(string); id == "" {
		t.Fatalf("remote record has no id: %v", records[0])
	}
	delete(records[0], "id")
	if want := wantPolicy(raceKeys["key-b"]); !reflect.DeepEqual(records[0], want) {
		t.Fatalf("remote record %v, want winner's %v", records[0], want)
	}

	got := logs.String()
	if strings.Contains(got, "created A record") || !strings.Contains(got, "create DNS record for "+raceHost) {
		t.Fatalf("plugin log %q: want a logged create failure and no success", got)
	}

	assertResyncIsQuiet(ctx, t, api, p)
}

// List-then-create is not atomic across processes. A permissive server can accept both POSTs,
// and the plugin will not delete either record afterwards.
func TestBoundaryNoCrossProcessUniquenessWhenServerAcceptsDuplicates(t *testing.T) {
	ctx, cancel := raceContext()
	defer cancel()
	api := newRaceAPI(t, true)
	p, _ := api.provider(ctx, "key-a")

	api.reconcileAgainstRemote(ctx, p)

	posts, records, callers := api.snapshot()
	assertBothListedEmpty(t, callers)
	if len(posts) != 2 || posts[0].status != http.StatusOK || posts[1].status != http.StatusOK || posts[0].caller == posts[1].caller {
		t.Fatalf("want one accepted POST per writer, got %v", posts)
	}
	if len(records) != 2 {
		t.Fatalf("remote records = %v, want two duplicates", records)
	}
	var ips []string
	for _, rec := range records {
		delete(rec, "id")
		ip, _ := rec["ipv4Address"].(string)
		if want := wantPolicy(ip); !reflect.DeepEqual(rec, want) {
			t.Fatalf("remote record %v, want %v", rec, want)
		}
		ips = append(ips, ip)
	}
	slices.Sort(ips)
	if want := []string{raceKeys["key-a"], raceKeys["key-b"]}; !slices.Equal(ips, want) {
		t.Fatalf("duplicate record IPs = %v, want %v", ips, want)
	}

	// Create-only: the duplicates stay, nothing is posted again, nothing is deleted.
	assertResyncIsQuiet(ctx, t, api, p)
}

type localConcurrencyDNS struct {
	postStarted chan struct{}
	finishPost  chan struct{}
	lists       int
	posts       int
	records     []unifi.DNSRecord
}

func (s *localConcurrencyDNS) ListRecords(context.Context) ([]unifi.DNSRecord, error) {
	s.lists++
	return slices.Clone(s.records), nil
}

func (s *localConcurrencyDNS) CreateRecord(_ context.Context, record unifi.DNSRecord) (string, error) {
	s.posts++
	if s.posts == 1 {
		close(s.postStarted)
		<-s.finishPost
	}
	s.records = append(s.records, record)
	return "id", nil
}

func (*localConcurrencyDNS) DeleteRecord(context.Context, string) error {
	panic("plugin must not delete records")
}

type routerResponseTransport struct{}

func (routerResponseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`[{"name":"app@docker","provider":"docker","status":"enabled","rule":"Host(` + "`" + raceHost + "`" + `)"}]`,
		)),
	}, nil
}

func TestConcurrentProvidersCreateOnce(t *testing.T) {
	testLocalConcurrency(t, false)
}

func TestConcurrentProviderWaiterCanCancel(t *testing.T) {
	testLocalConcurrency(t, true)
}

func testLocalConcurrency(t *testing.T, cancelWaiter bool) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		dns := &localConcurrencyDNS{postStarted: make(chan struct{}), finishPost: make(chan struct{})}
		newProvider := func(endpoint, site, address string) *Provider {
			cfg := testConfig(endpoint)
			cfg.UnifiSite, cfg.Address = site, address
			p, err := New(ctx, cfg, site)
			if err != nil {
				t.Fatal(err)
			}
			p.client.Transport = routerResponseTransport{}
			p.dns = dns
			return p
		}
		a := newProvider("https://CONTROLLER.home.arpa.", "default", "192.0.2.10")
		b := newProvider("https://controller.home.arpa:443", "site-id", "192.0.2.20")

		holder := make(chan error, 1)
		go func() { holder <- a.reconcile(ctx) }()
		select {
		case <-dns.postStarted:
		case err := <-holder:
			t.Fatalf("first provider returned before POST: %v", err)
		}

		waitCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		waiter := make(chan struct{})
		var waiterErr error
		go func() {
			waiterErr = b.reconcile(waitCtx)
			close(waiter)
		}()
		synctest.Wait()
		if dns.lists != 1 || dns.posts != 1 {
			t.Errorf("second provider reached UniFi during first POST: lists=%d, posts=%d", dns.lists, dns.posts)
		}
		if cancelWaiter {
			cancel()
			synctest.Wait()
			select {
			case <-waiter:
				if !errors.Is(waiterErr, context.Canceled) {
					t.Errorf("canceled waiter returned %v, want context.Canceled", waiterErr)
				}
			default:
				t.Error("canceled waiter did not return while first POST was in flight")
			}
		}

		close(dns.finishPost)
		if err := <-holder; err != nil {
			t.Errorf("first reconcile: %v", err)
		}
		<-waiter
		if !cancelWaiter && waiterErr != nil {
			t.Errorf("second reconcile: %v", waiterErr)
		}
		wantLists := 2
		if cancelWaiter {
			wantLists = 1
		}
		if dns.lists != wantLists || dns.posts != 1 {
			t.Errorf("lists=%d, posts=%d; want lists=%d, posts=1", dns.lists, dns.posts, wantLists)
		}
		if len(dns.records) != 1 || dns.records[0].Hostname != raceHost || dns.records[0].IP != "192.0.2.10" {
			t.Errorf("records = %v, want only the first provider's record", dns.records)
		}
	})
}
