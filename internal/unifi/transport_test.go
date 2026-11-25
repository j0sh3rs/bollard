package unifi_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/unifi"
)

type countingListener struct {
	net.Listener
	open *int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	atomic.AddInt32(l.open, 1)
	return &countingConn{Conn: conn, open: l.open}, nil
}

type countingConn struct {
	net.Conn
	open *int32
}

func (c *countingConn) Close() error {
	atomic.AddInt32(c.open, -1)
	return c.Conn.Close()
}

func startCountingServer(t *testing.T, handler http.HandlerFunc) (string, *int32) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	open := new(int32)
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(&countingListener{Listener: l, open: open}) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + l.Addr().String(), open
}

func waitClosed(t *testing.T, open *int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(open) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected 0 open connections, got %d", atomic.LoadInt32(open))
}

// A failed New must release the connection the site probe opened.
func TestNew_ClosesIdleConnectionsOnFailure(t *testing.T) {
	host, open := startCountingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	_, err := unifi.New(context.Background(), newTestConfig(host))
	if err == nil {
		t.Fatal("New: expected error from 403 server, got nil")
	}
	waitClosed(t, open)
}

// CloseIdleConnections must release the keep-alive connection established by calls.
func TestProvider_CloseIdleConnections(t *testing.T) {
	host, open := startCountingServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/proxy/network/integration/v1/sites":
			w.Write([]byte(`{"data":[{"id":"site-uuid","name":"default"}]}`))
		case "/proxy/network/integration/v1/sites/site-uuid/dns/policies":
			w.Write([]byte(`{"data":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	provider, err := unifi.New(context.Background(), newTestConfig(host))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := provider.ListRecords(context.Background()); err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	closer, ok := provider.(interface{ CloseIdleConnections() })
	if !ok {
		t.Fatal("provider does not expose CloseIdleConnections")
	}
	closer.CloseIdleConnections()
	waitClosed(t, open)
}

func TestNew_ObservesRequestsAndProvider(t *testing.T) {
	for _, tc := range []struct {
		name     string
		probe    []int
		provider string
	}{
		{"modern after retries", []int{503, 429, 200}, "modern"},
		{"legacy fallback", []int{404}, "legacy"},
		{"failed probe", []int{403}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempt atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				i := int(attempt.Add(1)) - 1
				if i >= len(tc.probe) {
					t.Errorf("unexpected probe attempt %d", i+1)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				status := tc.probe[i]
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = fmt.Fprint(w, `{"data":[{"id":"sid","name":"default"}]}`)
				}
			}))
			defer srv.Close()

			cfg := newTestConfig(srv.URL)
			cfg.RetryInitialDelay = time.Nanosecond
			var events, want []string
			cfg.ObserveRequest = func(method string, status int, elapsed time.Duration) {
				if elapsed <= 0 {
					t.Errorf("%s attempt duration = %v, want positive", method, elapsed)
				}
				events = append(events, fmt.Sprintf("%s %d", method, status))
			}
			cfg.ObserveProvider = func(provider string) {
				events = append(events, provider)
			}
			for _, status := range tc.probe {
				want = append(want, fmt.Sprintf("GET %d", status))
			}
			p, err := unifi.New(context.Background(), cfg)
			if tc.provider != "" {
				if err != nil {
					t.Fatal(err)
				}
				if err := p.DeleteRecord(context.Background(), "record-id"); err != nil {
					t.Fatal(err)
				}
				want = append(want, tc.provider, "DELETE 204")
			} else if err == nil {
				t.Fatal("New: want failed probe")
			}
			if !slices.Equal(events, want) {
				t.Errorf("events = %v, want %v", events, want)
			}
		})
	}
}

func TestNew_ObservesOnlyNetworkAttempts(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request reached untrusted server")
	}))
	defer srv.Close()
	cfg := newTestConfig(srv.URL)
	cfg.SkipTLSVerify = false
	var statuses []int
	cfg.ObserveRequest = func(method string, status int, elapsed time.Duration) {
		if method != http.MethodGet || elapsed <= 0 {
			t.Errorf("method = %s, elapsed = %v", method, elapsed)
		}
		statuses = append(statuses, status)
	}
	cfg.ObserveProvider = func(name string) { t.Errorf("unexpected provider %q", name) }

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := unifi.New(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("New with canceled context: %v", err)
	}
	if _, err := unifi.New(context.Background(), cfg); err == nil || unifi.IsRetryableStartupError(err) {
		t.Fatalf("New: want a non-retryable certificate error, got %v", err)
	}
	if !slices.Equal(statuses, []int{0}) {
		t.Errorf("statuses = %v, want exactly one failed network attempt", statuses)
	}
}

func TestIsRetryableStartupError_Probe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"rate limit", 429, `{"statusCode":403,"message":"rate limited"}`, true},
		{"unavailable", 503, "temporarily unavailable", true},
		{"forbidden", 403, `{"statusCode":503,"message":"invalid API key"}`, false},
		{"unauthorized", 401, "", false},
		{"unknown site", 200, `{"count":1,"totalCount":1,"data":[{"id":"sid","name":"other"}]}`, false},
		{"malformed site response", 200, "not JSON", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			cfg := newTestConfig(srv.URL)
			cfg.RetryAttempts = 1
			_, err := unifi.New(context.Background(), cfg)
			if err == nil || unifi.IsRetryableStartupError(fmt.Errorf("startup: %w", err)) != tc.want {
				t.Fatalf("New error = %v, want error with retryable = %t", err, tc.want)
			}
		})
	}

	for _, cfg := range []*unifi.Config{
		{Host: "http://%invalid"},
		{Host: "http://127.0.0.1", CACertPath: t.TempDir() + "/missing.pem"},
	} {
		_, err := unifi.New(context.Background(), cfg)
		if err == nil || unifi.IsRetryableStartupError(err) {
			t.Errorf("New: want non-retryable configuration error, got %v", err)
		}
	}
}

func TestIsRetryableStartupError_Network(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"timeout", &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}, true},
		{"temporary DNS", &net.DNSError{Err: "server misbehaving", IsTemporary: true}, true},
		{"permanent DNS", &net.DNSError{Err: "no such host", IsNotFound: true}, false},
		{"canceled context", context.Canceled, false},
		{"expired context", context.DeadlineExceeded, false},
		{"untyped lookalike", errors.New("unifi: site probe failed (status=503): connection refused"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unifi.IsRetryableStartupError(fmt.Errorf("startup: %w", tc.err)); got != tc.want {
				t.Errorf("retryable = %t, want %t", got, tc.want)
			}
		})
	}
}
