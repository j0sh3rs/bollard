package unifi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"
)

const maxResponseBody = 25 * 1024 * 1024

type httpTransport struct {
	client *http.Client
	apiKey string
	cfg    *Config
}

func newHTTPTransport(cfg *Config) (*httpTransport, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CACertPath != "" {
		pem, err := os.ReadFile(cfg.CACertPath)
		if err != nil {
			return nil, fmt.Errorf("unifi: read CA cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("unifi: no valid certs in %s", cfg.CACertPath)
		}
		tlsCfg.RootCAs = pool
	} else if cfg.SkipTLSVerify {
		tlsCfg.InsecureSkipVerify = true //nolint:gosec
	}
	return &httpTransport{
		client: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				TLSClientConfig:     tlsCfg,
				MaxConnsPerHost:     10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     30 * time.Second,
			},
		},
		apiKey: cfg.APIKey,
		cfg:    cfg,
	}, nil
}

// CloseIdleConnections releases pooled keep-alive connections to the
// UniFi controller. Safe to call more than once.
func (t *httpTransport) CloseIdleConnections() {
	t.client.CloseIdleConnections()
}

// HTTPError is a response the controller sent with a status of 300 or more.
type HTTPError struct {
	Status int
	Err    error
}

func (e *HTTPError) Error() string { return e.Err.Error() }
func (e *HTTPError) Unwrap() error { return e.Err }

// IsRejection reports whether err is a 4xx answer: the controller received the request and refused
// it, so the operation did not commit. Transport errors, timeouts and 5xx are ambiguous and return false.
func IsRejection(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status >= 400 && he.Status < 500
}

func (t *httpTransport) do(ctx context.Context, method, url string, body []byte) ([]byte, int, error) {
	var lastErr error
	var lastStatus int
	for attempt := 0; attempt < t.cfg.RetryAttempts; attempt++ {
		if attempt > 0 {
			wait := t.backoff(attempt)
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(wait):
			}
		}
		data, status, err := t.doOnce(ctx, method, url, body)
		if err == nil {
			return data, status, nil
		}
		lastErr = err
		lastStatus = status
		if !t.shouldRetry(method, status, err) {
			return nil, status, err
		}
	}
	return nil, lastStatus, lastErr
}

func (t *httpTransport) doOnce(ctx context.Context, method, url string, body []byte) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("unifi: new request: %w", err)
	}
	req.Header.Set("X-API-KEY", t.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	observe := t.cfg.ObserveRequest
	var started time.Time
	if observe != nil {
		started = time.Now()
	}
	resp, err := t.client.Do(req)
	if err != nil {
		if observe != nil {
			observe(method, 0, time.Since(started))
		}
		return nil, 0, fmt.Errorf("unifi: request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if observe != nil {
		observe(method, resp.StatusCode, time.Since(started))
	}
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("unifi: read body: %w", err)
	}
	if resp.StatusCode >= 300 {
		var apiErr apiErrorResponse
		if jsonErr := json.Unmarshal(data, &apiErr); jsonErr == nil && apiErr.Message != "" {
			return nil, resp.StatusCode, &HTTPError{Status: resp.StatusCode, Err: &apiErr}
		}
		limit := 512
		if len(data) < limit {
			limit = len(data)
		}
		return nil, resp.StatusCode, &HTTPError{Status: resp.StatusCode, Err: fmt.Errorf("unifi: HTTP %d: %s", resp.StatusCode, strconv.Quote(string(data[:limit])))}
	}
	return data, resp.StatusCode, nil
}

func (t *httpTransport) shouldRetry(method string, status int, _ error) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if method == http.MethodGet || method == http.MethodDelete {
		return status >= 500
	}
	return false
}

func (t *httpTransport) backoff(attempt int) time.Duration {
	base := t.cfg.RetryInitialDelay
	exp := time.Duration(math.Pow(2, float64(attempt-1))) * base
	var jitter time.Duration
	if half := int64(base / 2); half > 0 {
		jitter = time.Duration(rand.Int63n(half))
	}
	wait := exp + jitter
	if wait > t.cfg.RetryMaxDelay {
		return t.cfg.RetryMaxDelay
	}
	return wait
}
