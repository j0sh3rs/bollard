package traefik

import (
	"maps"
	"reflect"
	"testing"
)

// Any named record, whatever its type or enabled state, reserves the hostname: no POST for it.
func TestReconcileOccupiedNamesAreNeverCreated(t *testing.T) {
	f := newFixture(t)
	f.records = []map[string]any{
		{"id": "c", "type": "CNAME_RECORD", "enabled": true, "domain": "cname.example.com"},
		{"id": "da", "type": "A_RECORD", "enabled": false, "domain": "disabled-a.example.com", "ipv4Address": "192.0.2.9"},
		{"id": "dc", "type": "CNAME_RECORD", "enabled": false, "domain": "disabled-cname.example.com"},
		{"id": "m", "type": "CNAME_RECORD", "enabled": true, "domain": "Mixed.Example.com."},
	}
	before := make([]map[string]any, len(f.records))
	for i, record := range f.records {
		before[i] = maps.Clone(record)
	}
	f.setRouters("cname.example.com", "disabled-a.example.com", "disabled-cname.example.com", "mixed.example.com", "free.example.com")
	f.reconcile(f.provider("192.0.2.1"))

	assertEq(t, "posted domains", f.postedDomains(), []string{"free.example.com"})
	f.assertNoDestructive()
	if !reflect.DeepEqual(f.records[:4], before) {
		t.Fatalf("reserved records changed: got %v, want %v", f.records[:4], before)
	}
}
