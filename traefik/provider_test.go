package traefik

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/unifi"
)

func testConfig(endpoint string) *Config {
	c := CreateConfig()
	c.TraefikAPI = endpoint
	c.UnifiHost = endpoint
	c.UnifiAPIKey = "test-secret"
	c.Address = "192.0.2.10"
	return c
}

// The provider must discover real API rules, preserve manual records, and be idempotent across restarts.
func TestReconcileCreatesOnlyMissingRecords(t *testing.T) {
	var records = []map[string]interface{}{
		{"id": "manual", "type": "A_RECORD", "enabled": true, "domain": "manual.home.arpa", "ipv4Address": "192.0.2.99"},
		{"id": "same", "type": "A_RECORD", "enabled": true, "domain": "same.home.arpa", "ipv4Address": "192.0.2.10"},
	}
	var created []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/proxy/") && r.Header.Get("X-API-KEY") != "test-secret" {
			t.Error("missing UniFi API key")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/http/routers":
			if r.Header.Get("X-API-KEY") != "" {
				t.Error("UniFi key leaked to Traefik API")
			}
			fmt.Fprint(w, `[{"name":"app@docker","provider":"docker","status":"enabled","rule":"Host(`+"`app.home.arpa`"+`) || Host(`+"`same.home.arpa`"+`) || Host(`+"`manual.home.arpa`"+`)"},{"name":"duplicate@file","status":"enabled","rule":"Host(`+"`app.home.arpa`"+`)"},{"name":"off@file","status":"disabled","rule":"Host(`+"`off.home.arpa`"+`)"},{"name":"api@internal","provider":"internal","status":"enabled","rule":"Host(`+"`internal.home.arpa`"+`)"}]`)
		case "/proxy/network/integration/v1/sites":
			fmt.Fprint(w, `{"data":[{"id":"site-id","name":"default"}],"count":1,"totalCount":1}`)
		case "/proxy/network/integration/v1/sites/site-id/dns/policies":
			if r.Method == http.MethodGet {
				json.NewEncoder(w).Encode(map[string]interface{}{"data": records, "count": len(records), "totalCount": len(records)})
				return
			}
			if r.Method != http.MethodPost {
				t.Errorf("unexpected destructive method %s", r.Method)
				w.WriteHeader(405)
				return
			}
			var policy map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&policy); err != nil {
				t.Error(err)
			}
			if policy["domain"] != "app.home.arpa" || policy["ipv4Address"] != "192.0.2.10" || policy["type"] != "A_RECORD" || policy["enabled"] != true || policy["ttlSeconds"] != float64(300) {
				t.Errorf("unexpected policy: %v", policy)
			}
			created = append(created, policy["domain"].(string))
			policy["id"] = "new-id"
			records = append(records, policy)
			json.NewEncoder(w).Encode(policy)
		default:
			t.Errorf("unexpected API request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	for range 2 {
		p, err := New(context.Background(), testConfig(srv.URL), "test")
		if err != nil {
			t.Fatal(err)
		}
		if err := p.reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(created) != 1 || created[0] != "app.home.arpa" {
		t.Fatalf("created %v, want only app.home.arpa once", created)
	}
}

// Failed discovery or record listing must never cause writes based on a partial/unknown snapshot.
func TestReconcileAPIFailuresNeverWrite(t *testing.T) {
	for _, failure := range []string{"traefik-status", "traefik-json", "unifi-list"} {
		t.Run(failure, func(t *testing.T) {
			var writes int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes++
				}
				switch r.URL.Path {
				case "/api/http/routers":
					switch failure {
					case "traefik-status":
						w.WriteHeader(401)
					case "traefik-json":
						fmt.Fprint(w, "invalid")
					default:
						fmt.Fprint(w, `[{"name":"app@file","status":"enabled","rule":"Host(`+"`app.home.arpa`"+`)"}]`)
					}
				case "/proxy/network/integration/v1/sites":
					fmt.Fprint(w, `{"data":[{"id":"site-id","name":"default"}],"count":1,"totalCount":1}`)
				default:
					w.WriteHeader(403)
				}
			}))
			defer srv.Close()
			p, err := New(context.Background(), testConfig(srv.URL), "test")
			if err != nil {
				t.Fatal(err)
			}
			if err := p.reconcile(context.Background()); err == nil {
				t.Fatal("expected API failure")
			}
			if writes != 0 {
				t.Fatalf("made %d writes after API failure", writes)
			}
		})
	}
}

// A hung API must not prevent shutdown or leave a polling worker running.
func TestProviderStopCancelsDiscovery(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer srv.Close()
	p, err := New(context.Background(), testConfig(srv.URL), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Init(); err != nil {
		t.Fatal(err)
	}
	if err := p.Provide(make(chan json.Marshaler, 1)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not poll immediately")
	}
	done := make(chan error, 1)
	go func() { done <- p.Stop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not cancel API request")
	}
	<-cancelled
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

type spyDNS struct {
	unifi.DNSProvider
	closed atomic.Bool
}

func (s *spyDNS) CloseIdleConnections() { s.closed.Store(true) }

// Stop must release idle connections held by the cached UniFi provider.
func TestProviderStopClosesUnifiIdleConnections(t *testing.T) {
	spy := &spyDNS{}
	p := &Provider{client: &http.Client{}, dns: spy}
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !spy.closed.Load() {
		t.Fatal("Stop did not close idle connections on the unifi provider")
	}
}

// Bad configuration must fail locally, before any connection or DNS write.
func TestNewValidatesConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"address missing", func(c *Config) { c.Address = "" }},
		{"address ipv6", func(c *Config) { c.Address = "2001:db8::1" }},
		{"interval zero", func(c *Config) { c.PollInterval = "0s" }},
		{"interval invalid", func(c *Config) { c.PollInterval = "oops" }},
		{"key missing", func(c *Config) { c.UnifiAPIKey = "" }},
		{"both keys", func(c *Config) { c.UnifiAPIKeyFile = "/absent" }},
		{"host missing", func(c *Config) { c.UnifiHost = "" }},
		{"host credentials", func(c *Config) { c.UnifiHost = "https://user:secret@example.com" }},
		{"host query", func(c *Config) { c.UnifiHost = "https://example.com?key=secret" }},
		{"bad api scheme", func(c *Config) { c.TraefikAPI = "file:///etc/passwd" }},
		{"invalid site", func(c *Config) { c.UnifiSite = "../other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConfig("http://127.0.0.1:1")
			tc.change(c)
			if _, err := New(context.Background(), c, "test"); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestNewReadsKeyFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(file, []byte("test-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		if r.URL.Path == "/api/http/routers" {
			fmt.Fprint(w, "[{\"name\":\"app@file\",\"status\":\"enabled\",\"rule\":\"Host(`app.home.arpa`)\"}]")
			return
		}
		if r.Header.Get("X-API-KEY") != "test-secret" {
			t.Error("secret file key not used")
		}
		switch r.URL.Path {
		case "/proxy/network/integration/v1/sites":
			fmt.Fprint(w, `{"data":[{"id":"site-id","name":"default"}],"count":1,"totalCount":1}`)
		default:
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `{"data":[{"id":"existing","type":"A_RECORD","enabled":true,"domain":"app.home.arpa","ipv4Address":"192.0.2.10"}],"count":1,"totalCount":1}`)
				return
			}
			t.Error("unexpected write for existing record")
			w.WriteHeader(409)
		}
	}))
	defer srv.Close()
	c := testConfig(srv.URL)
	c.UnifiAPIKey = ""
	c.UnifiAPIKeyFile = file
	p, err := New(context.Background(), c, "test")
	if err != nil {
		t.Fatal(err)
	}
	if probes.Load() != 0 {
		t.Fatal("constructor performed network I/O")
	}
	if err := p.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}
