package traefik

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Traefik ends pagination with X-Next-Page: 1, not an empty header.
func TestDiscoverFollowsTraefikPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, "[{\"name\":\"first@file\",\"status\":\"enabled\",\"rule\":\"Host(`first.home.arpa`)\"}]")
			return
		}
		if r.URL.Query().Get("page") != "2" {
			t.Error("unexpected page request", r.URL.RawQuery)
		}
		w.Header().Set("X-Next-Page", "1")
		fmt.Fprint(w, "[{\"name\":\"second@file\",\"status\":\"enabled\",\"rule\":\"Host(`second.home.arpa`)\"}]")
	}))
	defer srv.Close()
	p, err := New(context.Background(), testConfig(srv.URL), "test")
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := p.discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || !hosts["first.home.arpa"] || !hosts["second.home.arpa"] {
		t.Fatalf("incomplete discovery: %v", hosts)
	}
}

func TestDiscoverRejectsStalledPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Next-Page", "2")
		fmt.Fprint(w, "[]")
	}))
	defer srv.Close()
	p, err := New(context.Background(), testConfig(srv.URL), "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.discover(context.Background()); err == nil {
		t.Fatal("stalled pagination accepted")
	}
}
