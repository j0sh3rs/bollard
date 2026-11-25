package traefik

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixture is a stateful Traefik + modern UniFi API. Both list endpoints are paginated one item
// (or two) at a time so the real clients must follow pagination. The UniFi site is the
// real-world payload: name "Default", internalReference "default".
type fixture struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	routers  []string         // rule per router, one router each
	records  []map[string]any // remote UniFi DNS policies
	requests []string         // "METHOD path" for UniFi calls
	posts    []map[string]any // every POST body received
	failPost map[string]bool  // domain -> respond 500 to POST
	failDel  map[string]bool  // record id -> respond 500 to DELETE
	hold     chan struct{}    // if set, next POST commits then waits for this channel before responding
	blockDel map[string]bool  // record id -> DELETE hangs until the client cancels
	held     chan struct{}    // closed when a POST is being held
	nextID   int
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, failPost: map[string]bool{}, records: []map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/proxy/") {
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if r.Header.Get("X-API-KEY") != "test-secret" {
			f.t.Error("missing UniFi API key")
			w.WriteHeader(401)
			return
		}
	}
	page := func(total, size int) (int, int) {
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if off > total {
			off = total
		}
		end := off + size
		if end > total {
			end = total
		}
		return off, end
	}
	switch {
	case r.URL.Path == "/api/http/routers":
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if p < 1 {
			p = 1
		}
		var out []map[string]string
		if p <= len(f.routers) {
			out = append(out, map[string]string{
				"name": fmt.Sprintf("r%d@docker", p), "provider": "docker", "status": "enabled", "rule": f.routers[p-1],
			})
		}
		if p < len(f.routers) {
			w.Header().Set("X-Next-Page", strconv.Itoa(p+1))
		}
		if out == nil {
			out = []map[string]string{}
		}
		json.NewEncoder(w).Encode(out)
	case r.URL.Path == "/proxy/network/integration/v1/sites":
		sites := []map[string]any{
			{"id": "other-id", "name": "Other", "internalReference": "other"},
			{"id": "site-id", "name": "Default", "internalReference": "default"},
		}
		off, end := page(len(sites), 1)
		json.NewEncoder(w).Encode(map[string]any{"data": sites[off:end], "count": end - off, "totalCount": len(sites)})
	case r.URL.Path == "/proxy/network/integration/v1/sites/site-id/dns/policies":
		switch r.Method {
		case http.MethodGet:
			off, end := page(len(f.records), 2)
			json.NewEncoder(w).Encode(map[string]any{"data": f.records[off:end], "count": end - off, "totalCount": len(f.records)})
		case http.MethodPost:
			var policy map[string]any
			if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
				f.t.Error(err)
			}
			f.posts = append(f.posts, maps.Clone(policy))
			if f.failPost[policy["domain"].(string)] {
				w.WriteHeader(500)
				fmt.Fprint(w, `{"message":"boom"}`)
				return
			}
			f.nextID++
			policy["id"] = fmt.Sprintf("id-%d", f.nextID)
			f.records = append(f.records, policy)
			if hold := f.hold; hold != nil { // committed; delay the response
				f.hold = nil
				f.mu.Unlock()
				close(f.held)
				<-hold
				f.mu.Lock()
			}
			json.NewEncoder(w).Encode(policy)
		default:
			w.WriteHeader(405)
		}
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/proxy/network/integration/v1/sites/site-id/dns/policies/"):
		id := strings.TrimPrefix(r.URL.Path, "/proxy/network/integration/v1/sites/site-id/dns/policies/")
		if f.blockDel[id] { // hang until the client gives up
			f.mu.Unlock()
			<-r.Context().Done()
			f.mu.Lock()
			return
		}
		if f.failDel[id] {
			w.WriteHeader(500)
			return
		}
		for i, rec := range f.records {
			if rec["id"] == id {
				f.records = append(f.records[:i], f.records[i+1:]...)
				break
			}
		}
		w.WriteHeader(200)
	default:
		f.t.Errorf("unexpected API request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}

func (f *fixture) setRouters(hosts ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routers = nil
	for _, h := range hosts {
		f.routers = append(f.routers, "Host(`"+h+"`)")
	}
}

func (f *fixture) deleteRecord(domain string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, rec := range f.records {
		if rec["domain"] == domain {
			f.records = append(f.records[:i], f.records[i+1:]...)
			return
		}
	}
	f.t.Fatalf("no remote record %s", domain)
}

// state returns sorted "domain=ip" strings of the remote records.
func (f *fixture) state() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, rec := range f.records {
		out = append(out, fmt.Sprintf("%v=%v", rec["domain"], rec["ipv4Address"]))
	}
	sort.Strings(out)
	return out
}

func (f *fixture) postedDomains() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, p := range f.posts {
		out = append(out, p["domain"].(string))
	}
	return out
}

func (f *fixture) resetPosts() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = nil
}

func (f *fixture) assertNoDestructive() {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if m := strings.SplitN(r, " ", 2)[0]; m != http.MethodGet && m != http.MethodPost {
			f.t.Fatalf("destructive UniFi call %q", r)
		}
	}
}

func (f *fixture) provider(address string) *Provider {
	f.t.Helper()
	c := testConfig(f.srv.URL)
	c.Address = address
	p, err := New(context.Background(), c, "test")
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fixture) reconcile(p *Provider) {
	f.t.Helper()
	if err := p.reconcile(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

func assertEq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// New routers on a running provider create only their own record; repeated polls add nothing;
// a record deleted remotely is recreated on the next pass.
func TestReconcileSequentialPollsOnOneProvider(t *testing.T) {
	f := newFixture(t)
	p := f.provider("192.0.2.10")

	f.setRouters("a.home.arpa")
	f.reconcile(p)
	assertEq(t, "posted", f.postedDomains(), []string{"a.home.arpa"})
	assertEq(t, "state", f.state(), []string{"a.home.arpa=192.0.2.10"})

	f.resetPosts()
	f.reconcile(p)
	f.reconcile(p)
	assertEq(t, "posted on unchanged polls", f.postedDomains(), []string{})

	f.setRouters("a.home.arpa", "b.home.arpa", "c.home.arpa")
	f.reconcile(p)
	assertEq(t, "posted after new routers", f.postedDomains(), []string{"b.home.arpa", "c.home.arpa"})
	f.resetPosts()
	f.reconcile(p)
	assertEq(t, "posted on repeat", f.postedDomains(), []string{})
	assertEq(t, "state", f.state(), []string{"a.home.arpa=192.0.2.10", "b.home.arpa=192.0.2.10", "c.home.arpa=192.0.2.10"})

	f.deleteRecord("b.home.arpa")
	f.reconcile(p)
	assertEq(t, "posted after remote delete", f.postedDomains(), []string{"b.home.arpa"})
	assertEq(t, "state", f.state(), []string{"a.home.arpa=192.0.2.10", "b.home.arpa=192.0.2.10", "c.home.arpa=192.0.2.10"})

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, post := range f.posts {
		want := map[string]any{"type": "A_RECORD", "enabled": true, "domain": "b.home.arpa", "ipv4Address": "192.0.2.10", "ttlSeconds": float64(300)}
		if !reflect.DeepEqual(post, want) {
			t.Fatalf("payload %v, want %v", post, want)
		}
	}
}

// Removing one or all routers never deletes or modifies records, and issues no destructive calls.
func TestReconcileRouterRemovalPreservesRecords(t *testing.T) {
	f := newFixture(t)
	p := f.provider("192.0.2.10")
	all := []string{"a.home.arpa=192.0.2.10", "b.home.arpa=192.0.2.10", "c.home.arpa=192.0.2.10"}

	f.setRouters("a.home.arpa", "b.home.arpa", "c.home.arpa")
	f.reconcile(p)
	assertEq(t, "state", f.state(), all)

	f.resetPosts()
	f.setRouters("a.home.arpa", "c.home.arpa")
	f.reconcile(p)
	assertEq(t, "state after removing one", f.state(), all)

	f.setRouters()
	f.reconcile(p)
	f.reconcile(p)
	assertEq(t, "state after removing all", f.state(), all)
	assertEq(t, "posted", f.postedDomains(), []string{})
	f.assertNoDestructive()

	// Router returns: records still exist, so nothing is duplicated.
	f.setRouters("b.home.arpa")
	f.reconcile(p)
	assertEq(t, "posted after router returns", f.postedDomains(), []string{})
	assertEq(t, "state", f.state(), all)
	f.assertNoDestructive()
}

// One failed POST must not block other hostnames; the missing record is retried next pass.
func TestReconcileFailedPostIsIsolatedAndRetried(t *testing.T) {
	f := newFixture(t)
	p := f.provider("192.0.2.10")
	f.setRouters("bad.home.arpa", "good.home.arpa")
	f.failPost["bad.home.arpa"] = true

	f.reconcile(p) // create errors are logged, not returned
	assertEq(t, "posted", f.postedDomains(), []string{"bad.home.arpa", "good.home.arpa"})
	assertEq(t, "state", f.state(), []string{"good.home.arpa=192.0.2.10"})

	f.mu.Lock()
	delete(f.failPost, "bad.home.arpa")
	f.mu.Unlock()
	f.resetPosts()
	f.reconcile(p)
	assertEq(t, "retry posted", f.postedDomains(), []string{"bad.home.arpa"})
	assertEq(t, "state", f.state(), []string{"bad.home.arpa=192.0.2.10", "good.home.arpa=192.0.2.10"})
	f.assertNoDestructive()
}

// Changing the configured target IP never rewrites existing records; only new hostnames use it.
func TestReconcileTargetIPChangeDoesNotOverwrite(t *testing.T) {
	f := newFixture(t)
	f.setRouters("a.home.arpa")
	f.reconcile(f.provider("192.0.2.10"))

	p2 := f.provider("192.0.2.20")
	f.resetPosts()
	f.reconcile(p2)
	assertEq(t, "posted", f.postedDomains(), []string{})
	assertEq(t, "state", f.state(), []string{"a.home.arpa=192.0.2.10"})

	f.setRouters("a.home.arpa", "b.home.arpa")
	f.reconcile(p2)
	assertEq(t, "posted", f.postedDomains(), []string{"b.home.arpa"})
	assertEq(t, "state", f.state(), []string{"a.home.arpa=192.0.2.10", "b.home.arpa=192.0.2.20"})
	f.assertNoDestructive()
}

// Stop deletes exactly the records this instance created. Pre-existing and other-writer records stay.
func TestStopDeletesOnlyRecordsCreatedByThisInstance(t *testing.T) {
	f := newFixture(t)
	f.setRouters("old.home.arpa")
	f.reconcile(f.provider("192.0.2.99")) // earlier run / other writer

	p := f.provider("192.0.2.10")
	f.setRouters("old.home.arpa", "a.home.arpa", "b.home.arpa")
	f.reconcile(p)
	assertEq(t, "state before stop", f.state(), []string{"a.home.arpa=192.0.2.10", "b.home.arpa=192.0.2.10", "old.home.arpa=192.0.2.99"})

	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	assertEq(t, "state after stop", f.state(), []string{"old.home.arpa=192.0.2.99"})
	f.mu.Lock()
	deletes := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, http.MethodDelete+" ") {
			deletes++
		}
	}
	f.mu.Unlock()
	if deletes != 2 {
		t.Fatalf("DELETE calls = %d, want 2", deletes)
	}
}

// A failed delete is logged and does not stop cleanup of the remaining records.
func TestStopContinuesAfterFailedDelete(t *testing.T) {
	f := newFixture(t)
	p := f.provider("192.0.2.10")
	f.setRouters("a.home.arpa", "b.home.arpa", "c.home.arpa")
	f.reconcile(p)
	f.mu.Lock()
	f.failDel = map[string]bool{"id-1": true}
	f.mu.Unlock()

	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	assertEq(t, "state after stop", f.state(), []string{"a.home.arpa=192.0.2.10"})
}

// Stop cancels an in-flight create whose POST UniFi already committed. The response (and ID) must
// still be collected so the record is deleted, not orphaned.
func TestStopDrainsInFlightCreateBeforeCleanup(t *testing.T) {
	f := newFixture(t)
	f.setRouters("a.home.arpa", "b.home.arpa")
	f.mu.Lock()
	f.hold, f.held = make(chan struct{}), make(chan struct{})
	hold, held := f.hold, f.held
	f.mu.Unlock()
	p := f.provider("192.0.2.10")
	if err := p.Provide(make(chan json.Marshaler, 1)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-held:
	case <-time.After(3 * time.Second):
		t.Fatal("POST never reached UniFi")
	}
	// Signal after Stop calls the real cancel, so the held POST is released only once the worker is cancelled.
	cancelled := make(chan struct{})
	p.mu.Lock()
	orig := p.cancel
	p.cancel = func() { orig(); close(cancelled) }
	p.mu.Unlock()
	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop() }()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop never cancelled the worker")
	}
	close(hold)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("Stop exceeded Traefik's 10s close budget")
	}
	assertEq(t, "state after stop", f.state(), []string{})
	// If Stop had not cancelled the worker first, it would have moved on and posted b.home.arpa.
	assertEq(t, "posted", f.postedDomains(), []string{"a.home.arpa"})
}

// One hung DELETE must not consume the whole shutdown budget; later records still reach UniFi.
func TestStopHungDeleteDoesNotStarveOthers(t *testing.T) {
	f := newFixture(t)
	p := f.provider("192.0.2.10")
	f.setRouters("a.home.arpa", "b.home.arpa", "c.home.arpa")
	f.reconcile(p)
	f.mu.Lock()
	f.blockDel = map[string]bool{"id-1": true}
	f.mu.Unlock()

	start := time.Now()
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > cleanupTimeout {
		t.Fatalf("Stop took %v, want within %v", d, cleanupTimeout)
	}
	assertEq(t, "state after stop", f.state(), []string{"a.home.arpa=192.0.2.10"})
}
