package unifi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/unifi"
)

func newTestConfig(url string) *unifi.Config {
	return &unifi.Config{
		Host:          url,
		APIKey:        "test-key",
		Site:          "default",
		SkipTLSVerify: true,
	}
}

func TestNew_ModernAPIDetection(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proxy/network/integration/v1/sites" {
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "site-uuid", "name": "default"}},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	provider, err := unifi.New(context.Background(), newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestListRecords_Modern(t *testing.T) {
	ttl := 300
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/proxy/network/integration/v1/sites":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "site-uuid", "name": "default"}},
			})
		case "/proxy/network/integration/v1/sites/site-uuid/dns/policies":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"id": "rec-1", "type": "A_RECORD",
					"domain": "myapp.home.arpa", "ipv4Address": "192.168.1.10",
					"ttlSeconds": ttl, "enabled": true,
				}},
				"count": 1, "totalCount": 1,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	provider, err := unifi.New(context.Background(), newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	records, err := provider.ListRecords(context.Background())
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Hostname != "myapp.home.arpa" {
		t.Errorf("expected hostname myapp.home.arpa, got %q", records[0].Hostname)
	}
}

func TestCreateAndDeleteRecord_Modern(t *testing.T) {
	var created map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/proxy/network/integration/v1/sites":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "site-uuid", "name": "default"}},
			})
		case r.URL.Path == "/proxy/network/integration/v1/sites/site-uuid/dns/policies" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&created)
			created["id"] = "new-rec-id"
			json.NewEncoder(w).Encode(created)
		case r.URL.Path == "/proxy/network/integration/v1/sites/site-uuid/dns/policies/new-rec-id" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	provider, err := unifi.New(context.Background(), newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id, err := provider.CreateRecord(context.Background(), unifi.DNSRecord{
		Hostname: "test.home.arpa", IP: "192.168.1.5", RecordType: "A", TTL: 300,
	})
	if err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	if id != "new-rec-id" {
		t.Errorf("expected id new-rec-id, got %q", id)
	}
	if err := provider.DeleteRecord(context.Background(), id); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
}

func TestModernCreateRequiresRecordID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `{"count":1,"totalCount":1,"data":[{"id":"site-uuid","name":"default"}]}`)
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"domain":"test.home.arpa"}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	p, err := unifi.New(context.Background(), newTestConfig(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.CreateRecord(context.Background(), unifi.DNSRecord{Hostname: "test.home.arpa"}); err == nil {
		t.Fatal("accepted a create response with no ID")
	}
	if err := p.DeleteRecord(context.Background(), ""); err == nil {
		t.Fatal("accepted a delete without a record ID")
	}
}

// TestNew_LegacyFallback verifies that when the modern API probe returns 404,
// New() falls back to the legacy provider.
func TestNew_LegacyFallback(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Modern API not available — 404 on everything
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	provider, err := unifi.New(context.Background(), newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New with legacy fallback: %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil legacy provider")
	}
}

// TestListRecords_Legacy tests the legacy static-DNS list path.
func TestListRecords_Legacy(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/proxy/network/v2/api/site/default/static-dns":
			// Real legacy API returns a raw JSON array.
			json.NewEncoder(w).Encode([]map[string]any{
				{"_id": "leg-1", "key": "legacy.home.arpa", "value": "10.0.0.5",
					"record_type": "A", "ttl": 120, "enabled": true},
				// disabled record — should be filtered
				{"_id": "leg-2", "key": "old.home.arpa", "value": "10.0.0.6",
					"record_type": "A", "ttl": 300, "enabled": false},
			})
		default:
			// modern probe fails → legacy fallback
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	provider, err := unifi.New(context.Background(), newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	records, err := provider.ListRecords(context.Background())
	if err != nil {
		t.Fatalf("ListRecords legacy: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 enabled record, got %d", len(records))
	}
	if records[0].Hostname != "legacy.home.arpa" {
		t.Errorf("expected legacy.home.arpa, got %q", records[0].Hostname)
	}
}

// TestCreateRecord_Legacy tests the legacy static-DNS create path.
func TestCreateRecord_Legacy(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/proxy/network/v2/api/site/default/static-dns" && r.Method == http.MethodPost:
			// Real legacy API returns a single object, not an envelope.
			json.NewEncoder(w).Encode(map[string]any{
				"_id": "leg-new", "key": "new.home.arpa", "value": "10.0.0.99",
				"record_type": "A", "ttl": 300, "enabled": true,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	provider, err := unifi.New(context.Background(), newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id, err := provider.CreateRecord(context.Background(), unifi.DNSRecord{
		Hostname: "new.home.arpa", IP: "10.0.0.99", RecordType: "A", TTL: 300,
	})
	if err != nil {
		t.Fatalf("CreateRecord legacy: %v", err)
	}
	if id != "leg-new" {
		t.Errorf("expected id leg-new, got %q", id)
	}
}

// TestDeleteRecord_Legacy tests the legacy static-DNS delete path.
func TestDeleteRecord_Legacy(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/proxy/network/v2/api/site/default/static-dns/leg-del" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	provider, err := unifi.New(context.Background(), newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := provider.DeleteRecord(context.Background(), "leg-del"); err != nil {
		t.Fatalf("DeleteRecord legacy: %v", err)
	}
}

// TestDeleteRecord_404Tolerant verifies that a 404 on delete is not an error
// for both modern and legacy providers.
func TestDeleteRecord_404Tolerant(t *testing.T) {
	// Modern provider: 404 on delete should not be an error
	srvModern := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/proxy/network/integration/v1/sites":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "site-uuid", "name": "default"}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srvModern.Close()

	provider, err := unifi.New(context.Background(), newTestConfig(srvModern.URL))
	if err != nil {
		t.Fatalf("New modern: %v", err)
	}
	if err := provider.DeleteRecord(context.Background(), "nonexistent"); err != nil {
		t.Fatalf("modern DeleteRecord 404 should be tolerated: %v", err)
	}

	// Legacy provider: 404 on delete should not be an error either
	srvLegacy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srvLegacy.Close()

	legacyProvider, err := unifi.New(context.Background(), newTestConfig(srvLegacy.URL))
	if err != nil {
		t.Fatalf("New legacy: %v", err)
	}
	if err := legacyProvider.DeleteRecord(context.Background(), "nonexistent"); err != nil {
		t.Fatalf("legacy DeleteRecord 404 should be tolerated: %v", err)
	}
}

// TestRetry_429AlwaysRetried verifies that 429 responses are retried for both
// GET and POST methods.
func TestRetry_429AlwaysRetried(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		switch r.URL.Path {
		case "/proxy/network/integration/v1/sites":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "site-uuid", "name": "default"}},
			})
		case "/proxy/network/integration/v1/sites/site-uuid/dns/policies":
			if r.Method == http.MethodGet {
				if n < 3 {
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{
					"data": []map[string]any{}, "count": 0, "totalCount": 0,
				})
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := newTestConfig(srv.URL)
	cfg.RetryAttempts = 3
	cfg.RetryInitialDelay = 1 // 1ns to keep test fast
	provider, err := unifi.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	records, err := provider.ListRecords(context.Background())
	if err != nil {
		t.Fatalf("ListRecords after 429 retries: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}

// TestAPIKeyHeader verifies the X-API-KEY header is sent on requests.
func TestAPIKeyHeader(t *testing.T) {
	var gotKey string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-KEY")
		if r.URL.Path == "/proxy/network/integration/v1/sites" {
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "site-uuid", "name": "default"}},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cfg := newTestConfig(srv.URL)
	cfg.APIKey = "my-secret-api-key"
	if _, err := unifi.New(context.Background(), cfg); err != nil {
		t.Fatalf("New: %v", err)
	}
	if gotKey != "my-secret-api-key" {
		t.Errorf("expected X-API-KEY=my-secret-api-key, got %q", gotKey)
	}
}

// TestSiteByID verifies that site resolution works when cfg.Site matches site ID
// instead of site name.
func TestSiteByID(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proxy/network/integration/v1/sites" {
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "abc-123", "name": "my-site"}},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cfg := newTestConfig(srv.URL)
	cfg.Site = "abc-123" // match by ID, not name
	provider, err := unifi.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New with site ID: %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
}

// TestAPIError_StructuredResponse verifies that a structured API error response
// is returned as an error (covering apiErrorResponse.Error()).
func TestAPIError_StructuredResponse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/proxy/network/integration/v1/sites":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "site-uuid", "name": "default"}},
			})
		default:
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]any{
				"statusCode": 422,
				"statusName": "UNPROCESSABLE_ENTITY",
				"message":    "invalid domain format",
			})
		}
	}))
	defer srv.Close()

	provider, err := unifi.New(context.Background(), newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = provider.CreateRecord(context.Background(), unifi.DNSRecord{
		Hostname: "bad..domain", IP: "192.168.1.1", RecordType: "A", TTL: 300,
	})
	if err == nil {
		t.Fatal("expected error from structured API error response, got nil")
	}
	if msg := err.Error(); msg == "" {
		t.Error("expected non-empty error message")
	}
}

// TestRetry_5xxGetRetried verifies that 5xx responses are retried for GET.
func TestRetry_5xxGetRetried(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		switch r.URL.Path {
		case "/proxy/network/integration/v1/sites":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "site-uuid", "name": "default"}},
			})
		case "/proxy/network/integration/v1/sites/site-uuid/dns/policies":
			if n < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{}, "count": 0, "totalCount": 0,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := newTestConfig(srv.URL)
	cfg.RetryAttempts = 3
	cfg.RetryInitialDelay = 2_000_000 // 2ms
	provider, err := unifi.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	records, err := provider.ListRecords(context.Background())
	if err != nil {
		t.Fatalf("ListRecords after 5xx retry: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}

func TestNew_ProbeCancellation(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := unifi.New(ctx, newTestConfig(srv.URL))
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("probe did not stop after cancellation")
	}
}

func TestNew_ProbeFailuresDoNotFallBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, `{}`},
		{"forbidden", 403, `{}`},
		{"server failure", 500, `{}`},
		{"malformed JSON", 200, `{"data":`},
		{"site missing", 200, `{"data":[{"id":"other","name":"other"}]}`},
		{"empty remaining page", 200, `{"data":[],"count":1,"totalCount":2}`},
		{"negative total", 200, `{"data":[],"totalCount":-1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			cfg := newTestConfig(srv.URL)
			cfg.RetryAttempts = 1
			if p, err := unifi.New(context.Background(), cfg); err == nil || p != nil {
				t.Fatalf("expected probe failure without provider, got %T, %v", p, err)
			}
		})
	}
}

func TestNew_UnsupportedProbeFallsBack(t *testing.T) {
	for _, status := range []int{404, 405, 501} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer srv.Close()
			cfg := newTestConfig(srv.URL)
			cfg.RetryInitialDelay = time.Nanosecond
			if p, err := unifi.New(context.Background(), cfg); err != nil || p == nil {
				t.Fatalf("expected legacy provider, got %T, %v", p, err)
			}
		})
	}
}

func TestNew_RejectsRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		fmt.Fprint(w, `{"data":[{"id":"site","name":"default"}]}`)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	if _, err := unifi.New(context.Background(), newTestConfig(srv.URL)); err == nil {
		t.Fatal("expected redirect rejection")
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect target received request and API key")
	}
}

func TestNew_SiteOnSecondPage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Query().Get("offset") {
		case "0":
			fmt.Fprint(w, `{"data":[{"id":"other","name":"other"}],"count":200,"totalCount":2}`)
		case "1":
			fmt.Fprint(w, `{"data":[{"id":"site","name":"default"}],"count":1,"totalCount":2}`)
		default:
			t.Errorf("unexpected offset %q", r.URL.Query().Get("offset"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	if _, err := unifi.New(context.Background(), newTestConfig(srv.URL)); err != nil {
		t.Fatalf("resolve second-page site: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected two pages, got %d", calls.Load())
	}
}

func TestListRecords_Pagination(t *testing.T) {
	for _, tc := range []struct {
		name       string
		secondPage string
		wantError  bool
	}{
		{"count does not skip records", `{"data":[{"id":"two","type":"A_RECORD","enabled":true}],"count":1,"totalCount":2}`, false},
		{"empty remaining page", `{"data":[],"count":1,"totalCount":2}`, true},
		{"negative total", `{"data":[],"totalCount":-1}`, true},
		{"negative count", `{"data":[],"count":-1,"totalCount":2}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/proxy/network/integration/v1/sites" {
					fmt.Fprint(w, `{"data":[{"id":"site","name":"default"}]}`)
					return
				}
				switch r.URL.Query().Get("offset") {
				case "0":
					fmt.Fprint(w, `{"data":[{"id":"one","type":"A_RECORD","enabled":true}],"count":200,"totalCount":2}`)
				case "1":
					fmt.Fprint(w, tc.secondPage)
				default:
					t.Errorf("unexpected offset %q", r.URL.Query().Get("offset"))
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer srv.Close()
			p, err := unifi.New(context.Background(), newTestConfig(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			records, err := p.ListRecords(context.Background())
			if tc.wantError {
				if err == nil {
					t.Fatal("expected malformed pagination error")
				}
				return
			}
			if err != nil || len(records) != 2 || records[1].ID != "two" {
				t.Fatalf("expected both records, got %+v, %v", records, err)
			}
		})
	}
}

func TestMalformedListResponsesAreRejected(t *testing.T) {
	for _, tc := range []struct {
		name, modern, legacy string
	}{
		{"missing data", `{}`, `null`},
		{"null data", `{"data":null,"count":0,"totalCount":0}`, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"modern", "legacy"} {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.URL.Path == "/proxy/network/integration/v1/sites" && mode == "modern":
						fmt.Fprint(w, `{"data":[{"id":"site","name":"default"}],"count":1,"totalCount":1}`)
					case r.URL.Path == "/proxy/network/integration/v1/sites/site/dns/policies" && mode == "modern":
						fmt.Fprint(w, tc.modern)
					case r.URL.Path == "/proxy/network/v2/api/site/default/static-dns" && mode == "legacy":
						fmt.Fprint(w, tc.legacy)
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				cfg := newTestConfig(srv.URL)
				cfg.IncludeAllRecords = true
				p, err := unifi.New(context.Background(), cfg)
				if err != nil {
					srv.Close()
					t.Fatalf("%s New: %v", mode, err)
				}
				if _, err := p.ListRecords(context.Background()); err == nil {
					t.Errorf("%s listing accepted malformed response", mode)
				}
				srv.Close()
			}
		})
	}
}

func TestModernWireTypeAIsNotAdoptableARecord(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proxy/network/integration/v1/sites" {
			fmt.Fprint(w, `{"data":[{"id":"site","name":"default"}],"count":1,"totalCount":1}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"bad","type":"A","enabled":true,"domain":"web.home.arpa","ipv4Address":"192.0.2.10"}],"count":1,"totalCount":1}`)
	}))
	defer srv.Close()
	cfg := newTestConfig(srv.URL)
	cfg.IncludeAllRecords = true
	p, err := unifi.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	records, err := p.ListRecords(context.Background())
	if err != nil || len(records) != 1 || records[0].RecordType == "A" {
		t.Fatalf("modern wire type A became adoptable: %+v, %v", records, err)
	}
}

func TestDeleteAcceptedIsNotCompleted(t *testing.T) {
	for _, mode := range []string{"modern", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/proxy/network/integration/v1/sites" && mode == "modern":
					fmt.Fprint(w, `{"data":[{"id":"site","name":"default"}],"count":1,"totalCount":1}`)
				case r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusAccepted)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			p, err := unifi.New(context.Background(), newTestConfig(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			if err := p.DeleteRecord(context.Background(), "pending"); err == nil {
				t.Fatal("accepted delete was reported as completed")
			}
		})
	}
}

func TestNew_TransportFailureDoesNotFallBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	if p, err := unifi.New(context.Background(), newTestConfig(srv.URL)); err == nil || p != nil {
		t.Fatalf("expected transport failure without provider, got %T, %v", p, err)
	}
}
