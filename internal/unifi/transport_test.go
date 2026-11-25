package unifi_test

import (
	"context"
	"net"
	"net/http"
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
