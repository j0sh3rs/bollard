package unifi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/j0sh3rs/bollard/internal/unifi"
)

const (
	modernPolicies = "/proxy/network/integration/v1/sites/site-uuid/dns/policies"
	legacyStatic   = "/proxy/network/v2/api/site/default/static-dns"
)

type captured struct {
	method, path string
	body         map[string]any
}

// newTypeServer serves a modern (or legacy) controller and records the last
// non-GET request. list is returned for GETs of the record collection.
func newTypeServer(t *testing.T, modern bool, list any, status int) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case modern && r.URL.Path == "/proxy/network/integration/v1/sites":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "site-uuid", "name": "default"}}})
		case r.Method == http.MethodGet && (r.URL.Path == modernPolicies || r.URL.Path == legacyStatic):
			json.NewEncoder(w).Encode(list)
		case strings.HasPrefix(r.URL.Path, modernPolicies), strings.HasPrefix(r.URL.Path, legacyStatic):
			b, _ := io.ReadAll(r.Body)
			got.method, got.path = r.Method, r.URL.Path
			json.Unmarshal(b, &got.body)
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "new-id", "_id": "new-id"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func newUpdater(t *testing.T, srv *httptest.Server, includeAll bool) (unifi.DNSProvider, unifi.RecordUpdater) {
	t.Helper()
	cfg := newTestConfig(srv.URL)
	cfg.IncludeAllRecords = includeAll
	p, err := unifi.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	u, ok := p.(unifi.RecordUpdater)
	if !ok {
		t.Fatal("provider does not implement RecordUpdater")
	}
	return p, u
}

var typeCases = []struct {
	name string
	rec  unifi.DNSRecord
	// wire body fields expected for modern / legacy
	modern, legacy map[string]any
}{
	{
		"A", unifi.DNSRecord{Hostname: "a.home", IP: "10.0.0.1", RecordType: "A", TTL: 60},
		map[string]any{"type": "A_RECORD", "domain": "a.home", "ipv4Address": "10.0.0.1", "ttlSeconds": 60.0, "enabled": true},
		map[string]any{"record_type": "A", "key": "a.home", "value": "10.0.0.1", "ttl": 60.0, "enabled": true},
	},
	{
		"AAAA", unifi.DNSRecord{Hostname: "v6.home", IP: "fd00::1", RecordType: "AAAA", TTL: 60},
		map[string]any{"type": "AAAA_RECORD", "domain": "v6.home", "ipv6Address": "fd00::1", "ttlSeconds": 60.0, "enabled": true},
		map[string]any{"record_type": "AAAA", "key": "v6.home", "value": "fd00::1", "ttl": 60.0, "enabled": true},
	},
	{
		"CNAME", unifi.DNSRecord{Hostname: "c.home", Target: "host.home", RecordType: "CNAME", TTL: 60},
		map[string]any{"type": "CNAME_RECORD", "domain": "c.home", "targetDomain": "host.home", "ttlSeconds": 60.0, "enabled": true},
		map[string]any{"record_type": "CNAME", "key": "c.home", "value": "host.home", "ttl": 60.0, "enabled": true},
	},
}

func TestCreateAndUpdate_RecordTypes(t *testing.T) {
	for _, modern := range []bool{true, false} {
		for _, tc := range typeCases {
			name := tc.name + "/legacy"
			want, base := tc.legacy, legacyStatic
			if modern {
				name, want, base = tc.name+"/modern", tc.modern, modernPolicies
			}
			t.Run(name, func(t *testing.T) {
				srv, got := newTypeServer(t, modern, nil, http.StatusOK)
				p, u := newUpdater(t, srv, false)

				if id, err := p.CreateRecord(context.Background(), tc.rec); err != nil || id != "new-id" {
					t.Fatalf("create: id=%q err=%v", id, err)
				}
				if got.method != http.MethodPost || got.path != base {
					t.Errorf("create request = %s %s", got.method, got.path)
				}
				if !reflect.DeepEqual(got.body, want) {
					t.Errorf("create body = %v, want %v", got.body, want)
				}

				if err := u.UpdateRecord(context.Background(), "rec-1", tc.rec); err != nil {
					t.Fatalf("update: %v", err)
				}
				if got.method != http.MethodPut || got.path != base+"/rec-1" {
					t.Errorf("update request = %s %s", got.method, got.path)
				}
				wantUpd := map[string]any{}
				for k, v := range want {
					wantUpd[k] = v
				}
				key := "id"
				if !modern {
					key = "_id"
				}
				wantUpd[key] = "rec-1"
				if !reflect.DeepEqual(got.body, wantUpd) {
					t.Errorf("update body = %v, want %v", got.body, wantUpd)
				}
			})
		}
	}
}

func TestUpdateRecord_Errors(t *testing.T) {
	for _, modern := range []bool{true, false} {
		srv, _ := newTypeServer(t, modern, nil, http.StatusNotFound)
		p, u := newUpdater(t, srv, false)
		rec := unifi.DNSRecord{Hostname: "a.home", IP: "10.0.0.1", RecordType: "A", TTL: 60}
		if err := u.UpdateRecord(context.Background(), "", rec); err == nil {
			t.Errorf("modern=%v: empty id should fail", modern)
		}
		if err := u.UpdateRecord(context.Background(), "x", rec); err == nil {
			t.Errorf("modern=%v: 404 should fail", modern)
		}
		if err := u.UpdateRecord(context.Background(), "x", unifi.DNSRecord{RecordType: "MX"}); err == nil {
			t.Errorf("modern=%v: unsupported type should fail on update", modern)
		}
		if _, err := p.CreateRecord(context.Background(), unifi.DNSRecord{RecordType: "MX"}); err == nil {
			t.Errorf("modern=%v: unsupported type should fail on create", modern)
		}
	}
}

func TestUpdateRecord_AcceptedIsNotCompleted(t *testing.T) {
	for _, modern := range []bool{true, false} {
		srv, _ := newTypeServer(t, modern, nil, http.StatusAccepted)
		_, u := newUpdater(t, srv, false)
		err := u.UpdateRecord(context.Background(), "x", unifi.DNSRecord{Hostname: "a.home", IP: "10.0.0.1", RecordType: "A"})
		if err == nil {
			t.Errorf("modern=%v: 202 should be not completed", modern)
		}
	}
}

func TestListRecords_AAAAandCNAME(t *testing.T) {
	modernList := map[string]any{"count": 4, "totalCount": 4, "data": []map[string]any{
		{"id": "1", "type": "A_RECORD", "enabled": true, "domain": "a.home", "ipv4Address": "10.0.0.1", "ttlSeconds": 60},
		{"id": "2", "type": "AAAA_RECORD", "enabled": true, "domain": "v6.home", "ipv6Address": "fd00::1", "ttlSeconds": 60},
		{"id": "3", "type": "CNAME_RECORD", "enabled": true, "domain": "c.home", "targetDomain": "host.home.", "ttlSeconds": 60},
		{"id": "4", "type": "MX_RECORD", "enabled": true, "domain": "m.home"},
	}}
	legacyList := []map[string]any{
		{"_id": "1", "key": "a.home", "value": "10.0.0.1", "record_type": "A", "ttl": 60, "enabled": true},
		{"_id": "2", "key": "v6.home", "value": "fd00::1", "record_type": "AAAA", "ttl": 60, "enabled": true},
		{"_id": "3", "key": "c.home", "value": "host.home.", "record_type": "CNAME", "ttl": 60, "enabled": true},
		{"_id": "4", "key": "m.home", "value": "x", "record_type": "MX", "enabled": true},
	}
	for _, modern := range []bool{true, false} {
		var list any = legacyList
		if modern {
			list = modernList
		}
		srv, _ := newTypeServer(t, modern, list, http.StatusOK)

		// Default: enabled A records only.
		p, _ := newUpdater(t, srv, false)
		recs, err := p.ListRecords(context.Background())
		if err != nil || len(recs) != 1 || recs[0].RecordType != "A" || recs[0].IP != "10.0.0.1" {
			t.Fatalf("modern=%v default list = %+v err=%v", modern, recs, err)
		}

		p, _ = newUpdater(t, srv, true)
		recs, err = p.ListRecords(context.Background())
		if err != nil || len(recs) != 4 {
			t.Fatalf("modern=%v all list = %+v err=%v", modern, recs, err)
		}
		if r := recs[1]; r.RecordType != "AAAA" || r.IP != "fd00::1" || r.Target != "" {
			t.Errorf("modern=%v AAAA = %+v", modern, r)
		}
		if r := recs[2]; r.RecordType != "CNAME" || r.IP != "" || r.Target != "host.home" {
			t.Errorf("modern=%v CNAME = %+v", modern, r)
		}
	}
}
