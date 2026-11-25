package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/unifi"
)

// TestMainPackageCompiles is a compile-time smoke test. If the main package
// builds, this test passes. It verifies that all imports resolve and the
// package has no syntax errors — no Docker socket required.
func TestMainPackageCompiles(t *testing.T) {
	t.Log("main package compiled successfully")
}

func TestSanitizeDSN(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"postgres://u:secret@h/db?sslmode=require", "postgres://u:***@h/db?sslmode=require"},
		{"postgres://h/db?password=secret", "postgres://h/db?password=***"},
		{"postgres://h/db?sslmode=require&sslpassword=secret", "postgres://h/db?sslmode=require&sslpassword=***"},
		{"postgres://u:secret@h/db?password=secret", "postgres://u:***@h/db?password=***"},
		{"postgres://h/db?sslmode=require", "postgres://h/db?sslmode=require"},
		{"file:bollard.db", "file:bollard.db"},
		{"postgres://h/db", "postgres://h/db"},
		{"postgresql://u%40x:secret@h/%2A%2A%2A?%70assword=secret&sslpassword=secret&other=%2A%2A%2A&password=secret", "postgresql://u%40x:***@h/%2A%2A%2A?%70assword=***&sslpassword=***&other=%2A%2A%2A&password=***"},
		{"postgres://h/db?z=%20&a=1", "postgres://h/db?z=%20&a=1"},
		{"postgres://h/db?password =secret&application_name=worker;blue", "postgres://h/db?password =***&application_name=worker;blue"},
		{"postgres://h/db? sslpassword=secret", "postgres://h/db? sslpassword=***"},
		{"postgres://u@h/db?application_name=worker;blue", "postgres://u@h/db?application_name=worker;blue"},
	}
	for _, c := range cases {
		got := sanitizeDSN(c.in)
		if got != c.want {
			t.Errorf("sanitizeDSN(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, "secret") {
			t.Errorf("sanitizeDSN(%q) leaks secret: %q", c.in, got)
		}
	}
}

func TestCheckHealth(t *testing.T) {
	for _, tc := range []struct {
		name, network, listen, probe string
	}{
		{"ipv4", "tcp4", "127.0.0.1:0", "127.0.0.1"},
		{"ipv4 wildcard", "tcp4", "0.0.0.0:0", "0.0.0.0"},
		{"empty host", "tcp4", "127.0.0.1:0", ""},
		{"ipv6", "tcp6", "[::1]:0", "::1"},
		{"ipv6 wildcard", "tcp6", "[::]:0", "::"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen(tc.network, tc.listen)
			if err != nil && tc.network == "tcp6" {
				t.Skipf("IPv6 loopback unavailable: %v", err)
			}
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			_ = server.Listener.Close()
			server.Listener = listener
			server.Start()
			defer server.Close()
			_, port, _ := net.SplitHostPort(listener.Addr().String())
			if err := checkHealth(net.JoinHostPort(tc.probe, port)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckHealthRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			http.Redirect(w, r, "/other", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := checkHealth(server.Listener.Addr().String()); err == nil {
		t.Fatal("redirect to HTTP 200 passed healthcheck")
	}
	if err := checkHealth("not-an-address"); err == nil {
		t.Fatal("invalid address passed healthcheck")
	}
}

func TestConnectUniFiRetriesTransientFailure(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{"count":1,"totalCount":1,"data":[{"id":"site-id","internalReference":"default"}]}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := connectUniFi(ctx, &unifi.Config{Host: server.URL, RetryAttempts: 1}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.(interface{ CloseIdleConnections() }).CloseIdleConnections)
	if got := attempts.Load(); got != 2 {
		t.Fatalf("probe attempts = %d, want 2", got)
	}
}

func TestConnectUniFiDoesNotRetryForbidden(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := connectUniFi(ctx, &unifi.Config{Host: server.URL, RetryAttempts: 1}, logger)
	if err == nil || attempts.Load() != 1 {
		t.Fatalf("forbidden result: %v, with %d probe attempts; want permanent failure", err, attempts.Load())
	}
}

func TestConnectUniFiStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	cfg := &unifi.Config{
		Host: server.URL, RetryAttempts: 1,
		ObserveRequest: func(_ string, _ int, _ time.Duration) { cancel() },
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := connectUniFi(ctx, cfg, logger); !errors.Is(err, context.Canceled) {
		t.Fatalf("startup returned %v, want context cancellation", err)
	}
}

func TestRetryableUniFiStartupConnectionErrors(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.ECONNREFUSED, syscall.ECONNRESET} {
		if !retryableUniFiStartup(&net.OpError{Op: "dial", Net: "tcp", Err: errno}) {
			t.Errorf("startup did not retry %v", errno)
		}
	}
}

func TestShutdownSignalPrecedesDockerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !dockerStreamStopped(ctx, context.Canceled) {
		t.Fatal("daemon treated a signal-driven Docker stream cancellation as a failure")
	}
	if dockerStreamStopped(context.Background(), context.Canceled) {
		t.Fatal("unexpected Docker stream cancellation was hidden from the daemon")
	}
}
