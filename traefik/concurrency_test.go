package traefik

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Two plugin instances (two Providers, as when Traefik runs two replicas or two plugin
// entries) share no state. Each one lists the zone, then creates what is missing. UniFi has no
// known atomic create-if-absent, so the only cross-instance protection is whatever the server
// does on a duplicate POST. These tests pin that boundary; they do not add any guarantee.

const (
	raceHost    = "app.home.arpa"
	raceBarrier = 5 * time.Second  // max wait for the second provider to list
	raceTimeout = 15 * time.Second // hard bound for every reconcile in this file
)

// raceKeys maps each provider's API key (which identifies the caller) to its target address.
var raceKeys = map[string]string{"key-a": "192.0.2.10", "key-b": "192.0.2.20"}

// raceAPI is a Traefik + modern UniFi API with one enabled Host router. Every DNS-policy list
// response is held until both providers have taken their snapshot, so neither can POST before
// both have seen the zone empty. No sleeps: the barrier is a channel closed by the second list.
type raceAPI struct {
	t               *testing.T
	srv             *httptest.Server
	allowDuplicates bool          // false: a POST for an existing domain gets 409
	release         chan struct{} // closed once both callers have listed

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
	a := &raceAPI{t: t, allowDuplicates: allowDuplicates, release: make(chan struct{}), callers: map[string]int{}}
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
		a.t.Errorf("barrier: %s listed but the other provider never did", caller)
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
	a.mu.Lock()
	defer a.mu.Unlock()
	select {
	case <-a.release:
	default:
		a.t.Errorf("%s POSTed before both providers listed", caller)
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

// reconcileTogether runs one reconcile per provider at the same time and waits for both.
// The context bounds every request, so a stuck barrier fails the test instead of hanging it.
func reconcileTogether(ctx context.Context, t *testing.T, providers ...*Provider) {
	t.Helper()
	errs := make([]error, len(providers))
	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = p.reconcile(ctx)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("provider %d reconcile: %v", i, err)
		}
	}
}

// assertResyncIsQuiet runs each provider once more, one after the other: no POST, no change to
// the remote records, no destructive call.
func assertResyncIsQuiet(ctx context.Context, t *testing.T, a *raceAPI, providers ...*Provider) {
	t.Helper()
	postsBefore, recordsBefore, _ := a.snapshot()
	for i, p := range providers {
		if err := p.reconcile(ctx); err != nil {
			t.Fatalf("provider %d resync: %v", i, err)
		}
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

// assertBothListedEmpty proves the barrier held: each provider's first list saw an empty zone.
func assertBothListedEmpty(t *testing.T, callers map[string]int) {
	t.Helper()
	if want := map[string]int{"key-a": 0, "key-b": 0}; !reflect.DeepEqual(callers, want) {
		t.Fatalf("first list snapshots (records seen per provider) = %v, want %v", callers, want)
	}
}

// Both providers see an empty zone, then both POST the same name. The server rejects the second
// with 409. Exactly one record survives with the winner's payload; the loser logs the failure and
// does not retry or overwrite; later reconciles add nothing and never call anything destructive.
func TestConcurrentCreatesServerRejectsDuplicate(t *testing.T) {
	ctx, cancel := raceContext()
	defer cancel()
	api := newRaceAPI(t, false)
	a, logA := api.provider(ctx, "key-a")
	b, logB := api.provider(ctx, "key-b")

	reconcileTogether(ctx, t, a, b)

	posts, records, callers := api.snapshot()
	assertBothListedEmpty(t, callers)
	if len(posts) != 2 || posts[0].caller == posts[1].caller {
		t.Fatalf("want exactly one POST per provider (no blind retry), got %v", posts)
	}
	statuses := []int{posts[0].status, posts[1].status}
	slices.Sort(statuses)
	if !slices.Equal(statuses, []int{http.StatusOK, http.StatusConflict}) {
		t.Fatalf("POST statuses = %v, want one 200 and one 409", statuses)
	}
	for _, p := range posts {
		if want := wantPolicy(raceKeys[p.caller]); !reflect.DeepEqual(p.policy, want) {
			t.Fatalf("%s POST payload %v, want %v", p.caller, p.policy, want)
		}
	}
	winner, loser := posts[0], posts[1]
	if winner.status != http.StatusOK {
		winner, loser = loser, winner
	}

	if len(records) != 1 {
		t.Fatalf("remote records = %v, want exactly one", records)
	}
	if id, _ := records[0]["id"].(string); id == "" {
		t.Fatalf("remote record has no id: %v", records[0])
	}
	delete(records[0], "id")
	if want := wantPolicy(raceKeys[winner.caller]); !reflect.DeepEqual(records[0], want) {
		t.Fatalf("remote record %v, want winner's %v", records[0], want)
	}

	logs := map[string]*bytes.Buffer{"key-a": logA, "key-b": logB}
	if got, want := logs[winner.caller].String(), "created A record "+raceHost+" -> "+raceKeys[winner.caller]; !strings.Contains(got, want) {
		t.Fatalf("winner log %q lacks %q", got, want)
	}
	got := logs[loser.caller].String()
	if strings.Contains(got, "created A record") || !strings.Contains(got, "create DNS record for "+raceHost) {
		t.Fatalf("loser log %q: want a logged create failure and no success", got)
	}

	assertResyncIsQuiet(ctx, t, api, a, b)
}

// BOUNDARY, not a safety claim: nothing in the plugin stops two instances creating the same
// name. When the server accepts duplicate POSTs, both instances create one, and the plugin
// never deletes either afterwards. List-then-create cannot be made atomic across instances.
func TestBoundaryNoCrossInstanceUniquenessWhenServerAcceptsDuplicates(t *testing.T) {
	ctx, cancel := raceContext()
	defer cancel()
	api := newRaceAPI(t, true)
	a, _ := api.provider(ctx, "key-a")
	b, _ := api.provider(ctx, "key-b")

	reconcileTogether(ctx, t, a, b)

	posts, records, callers := api.snapshot()
	assertBothListedEmpty(t, callers)
	if len(posts) != 2 || posts[0].status != http.StatusOK || posts[1].status != http.StatusOK || posts[0].caller == posts[1].caller {
		t.Fatalf("want one accepted POST per provider, got %v", posts)
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
	assertResyncIsQuiet(ctx, t, api, a, b)
}
