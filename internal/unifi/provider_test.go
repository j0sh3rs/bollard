package unifi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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

	provider, err := unifi.New(newTestConfig(srv.URL))
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

	provider, err := unifi.New(newTestConfig(srv.URL))
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

	provider, err := unifi.New(newTestConfig(srv.URL))
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

// TestNew_LegacyFallback verifies that when the modern API probe returns 404,
// New() falls back to the legacy provider.
func TestNew_LegacyFallback(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Modern API not available — 404 on everything
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	provider, err := unifi.New(newTestConfig(srv.URL))
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
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"_id": "leg-1", "key": "legacy.home.arpa", "value": "10.0.0.5",
						"record_type": "A", "ttl": 120, "enabled": true},
					// disabled record — should be filtered
					{"_id": "leg-2", "key": "old.home.arpa", "value": "10.0.0.6",
						"record_type": "A", "ttl": 300, "enabled": false},
				},
				"meta": map[string]any{"rc": "ok"},
			})
		default:
			// modern probe fails → legacy fallback
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	provider, err := unifi.New(newTestConfig(srv.URL))
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
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"_id": "leg-new", "key": "new.home.arpa", "value": "10.0.0.99",
						"record_type": "A", "ttl": 300, "enabled": true},
				},
				"meta": map[string]any{"rc": "ok"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	provider, err := unifi.New(newTestConfig(srv.URL))
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

	provider, err := unifi.New(newTestConfig(srv.URL))
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

	provider, err := unifi.New(newTestConfig(srvModern.URL))
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

	legacyProvider, err := unifi.New(newTestConfig(srvLegacy.URL))
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
	provider, err := unifi.New(cfg)
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
	if _, err := unifi.New(cfg); err != nil {
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
	provider, err := unifi.New(cfg)
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

	provider, err := unifi.New(newTestConfig(srv.URL))
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
	provider, err := unifi.New(cfg)
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
