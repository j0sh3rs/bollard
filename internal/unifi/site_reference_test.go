package unifi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/j0sh3rs/bollard/internal/unifi"
)

const realSitesPayload = `{"offset":0,"limit":200,"count":1,"totalCount":1,` +
	`"data":[{"id":"88f7af54-98f8-306a-a1c7-c9349722b1f6","internalReference":"default","name":"Default"}]}`

func TestNew_SiteResolution(t *testing.T) {
	const siteID = "88f7af54-98f8-306a-a1c7-c9349722b1f6"
	tests := []struct {
		name    string
		site    string
		wantErr bool
	}{
		{"default config matches internalReference", "", false},
		{"explicit internalReference", "default", false},
		{"explicit name", "Default", false},
		{"explicit id", siteID, false},
		{"nonmatching reference does not pick first site", "other", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var dnsPath string
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/proxy/network/integration/v1/sites":
					w.Write([]byte(realSitesPayload))
				case strings.HasSuffix(r.URL.Path, "/dns/policies"):
					dnsPath = r.URL.Path
					w.Write([]byte(`{"data":[],"count":0,"totalCount":0}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			cfg := newTestConfig(srv.URL)
			cfg.Site = tc.site
			p, err := unifi.New(context.Background(), cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error for nonmatching site")
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := p.ListRecords(context.Background()); err != nil {
				t.Fatalf("ListRecords: %v", err)
			}
			if want := "/proxy/network/integration/v1/sites/" + siteID + "/dns/policies"; dnsPath != want {
				t.Errorf("DNS path = %q, want %q", dnsPath, want)
			}
		})
	}
}

// A site with empty fields must not match an empty or unrelated reference.
func TestNew_EmptyFieldsDoNotMatch(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"count":1,"totalCount":1,"data":[{"id":"x","name":"","internalReference":""}]}`))
	}))
	defer srv.Close()

	// setDefaults turns "" into "default", which must not match the blank site.
	if _, err := unifi.New(context.Background(), newTestConfig(srv.URL)); err == nil {
		t.Fatal("expected site not found")
	}
}
