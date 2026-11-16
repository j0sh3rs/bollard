package unifi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/j0sh3rs/bollard/internal/unifi"
)

func collisionServer(modern bool) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case modern && r.URL.Path == "/proxy/network/integration/v1/sites":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "sid", "name": "default"}}})
		case modern && r.URL.Path == "/proxy/network/integration/v1/sites/sid/dns/policies":
			json.NewEncoder(w).Encode(map[string]any{"count": 4, "totalCount": 4, "data": []map[string]any{
				{"id": "1", "type": "A_RECORD", "enabled": true, "domain": "a.example.com", "ipv4Address": "10.0.0.1"},
				{"id": "2", "type": "CNAME_RECORD", "enabled": true, "domain": "Cname.Example.com."},
				{"id": "3", "type": "A_RECORD", "enabled": false, "domain": "disabled-a.example.com", "ipv4Address": "10.0.0.2"},
				{"id": "4", "type": "CNAME_RECORD", "enabled": false, "domain": "disabled-cname.example.com"},
			}})
		case !modern && r.URL.Path == "/proxy/network/v2/api/site/default/static-dns":
			json.NewEncoder(w).Encode([]map[string]any{
				{"_id": "1", "key": "a.example.com", "value": "10.0.0.1", "record_type": "A", "enabled": true},
				{"_id": "2", "key": "Cname.Example.com.", "value": "t.example.com", "record_type": "CNAME", "enabled": true},
				{"_id": "3", "key": "disabled-a.example.com", "value": "10.0.0.2", "record_type": "A", "enabled": false},
				{"_id": "4", "key": "disabled-cname.example.com", "value": "t.example.com", "record_type": "CNAME", "enabled": false},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestListRecords_IncludeAllRecords(t *testing.T) {
	for _, modern := range []bool{true, false} {
		name := "legacy"
		if modern {
			name = "modern"
		}
		t.Run(name, func(t *testing.T) {
			srv := collisionServer(modern)
			defer srv.Close()

			// Default: enabled A records only.
			p, err := unifi.New(context.Background(), newTestConfig(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			recs, err := p.ListRecords(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != 1 || recs[0].Hostname != "a.example.com" {
				t.Fatalf("default listing = %+v, want only a.example.com", recs)
			}

			// Opt-in: every named record, with type retained.
			cfg := newTestConfig(srv.URL)
			cfg.IncludeAllRecords = true
			p, err = unifi.New(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			recs, err = p.ListRecords(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != 4 {
				t.Fatalf("got %d records, want 4: %+v", len(recs), recs)
			}
			if recs[0].RecordType != "A" || recs[0].IP != "10.0.0.1" {
				t.Errorf("A record not retained: %+v", recs[0])
			}
			if recs[1].RecordType == "A" || recs[1].Hostname != "Cname.Example.com." {
				t.Errorf("CNAME not retained as non-A: %+v", recs[1])
			}
		})
	}
}
