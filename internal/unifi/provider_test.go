package unifi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
