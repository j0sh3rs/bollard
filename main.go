package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/j0sh3rs/bollard/internal/config"
	dockerwatcher "github.com/j0sh3rs/bollard/internal/docker"
	bollardlog "github.com/j0sh3rs/bollard/internal/log"
	"github.com/j0sh3rs/bollard/internal/metrics"
	"github.com/j0sh3rs/bollard/internal/reconciler"
	"github.com/j0sh3rs/bollard/internal/store"
	"github.com/j0sh3rs/bollard/internal/unifi"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// version is set via ldflags at build time.
var version = "dev"

func main() {
	adopt := flag.Bool("adopt", false, "adopt existing UniFi records before starting normal operation")
	healthcheck := flag.Bool("healthcheck", false, "check /healthz and exit 0 (healthy) or 1 (unhealthy)")
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *healthcheck {
		if err := checkHealth(os.Getenv("METRICS_ADDR")); err != nil {
			os.Exit(1)
		}
		return
	}

	os.Exit(run(*adopt))
}

func checkHealth(addr string) error {
	if addr == "" {
		addr = ":9090"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	switch host {
	case "":
		host = "localhost"
	case "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	endpoint := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/healthz"}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := http.Client{
		Timeout:   3 * time.Second,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(endpoint.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: HTTP %d", resp.StatusCode)
	}
	return nil
}

func run(adopt bool) int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", "err", err)
		return 1
	}

	logger, err := bollardlog.New(cfg.LogFormat, cfg.LogLevel)
	if err != nil {
		slog.Error("logger init error", "err", err)
		return 1
	}
	slog.SetDefault(logger)

	m := metrics.New(version, runtime.Version())
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	metricsListener, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		logger.Error("metrics listen failed", "err", err)
		return 1
	}
	metricsServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer metricsServer.Close()
	go func() {
		logger.Info("metrics server listening", "addr", metricsListener.Addr())
		if err := metricsServer.Serve(metricsListener); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server failed", "err", err)
		}
	}()

	db, err := store.NewStore(cfg.DatabaseURL)
	if err != nil {
		logger.Error("state store unavailable", "err", err)
		return 1
	}
	defer db.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	provider, err := connectUniFi(ctx, &unifi.Config{
		Host:              cfg.UnifiHost,
		APIKey:            cfg.UnifiAPIKey,
		Site:              cfg.UnifiSite,
		SkipTLSVerify:     cfg.UnifiSkipTLSVerify,
		CACertPath:        cfg.UnifiCACert,
		IncludeAllRecords: true,
		ObserveRequest: func(method string, status int, elapsed time.Duration) {
			label := "error"
			if status != 0 {
				label = strconv.Itoa(status)
			}
			m.UnifiRequestsTotal.WithLabelValues(method, label).Inc()
			m.UnifiRequestDuration.WithLabelValues(method).Observe(elapsed.Seconds())
		},
		ObserveProvider: func(provider string) {
			m.UnifiAPIVersion.WithLabelValues(provider).Set(1)
		},
	}, logger)
	if err != nil {
		if ctx.Err() != nil {
			return 0
		}
		logger.Error("unifi client init error", "err", strings.ReplaceAll(err.Error(), cfg.UnifiAPIKey, "[redacted]"))
		return 1
	}
	if closer, ok := provider.(interface{ CloseIdleConnections() }); ok {
		defer closer.CloseIdleConnections()
	}

	watcher, err := dockerwatcher.NewWatcher()
	if err != nil {
		logger.Error("docker socket unavailable", "err", err)
		return 1
	}
	defer watcher.Close()

	listerClient, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		logger.Error("docker client for lister unavailable", "err", err)
		return 1
	}
	defer listerClient.Close()

	lister := &dockerLister{client: listerClient}
	rec := reconciler.New(db, provider, lister, "", m, logger)

	if adopt {
		logger.Info("starting adopt phase")
		running, err := lister.ListRunning(ctx)
		if err != nil {
			logger.Error("adopt: list containers failed", "err", err)
			return 1
		}
		if err := rec.Adopt(ctx, running); err != nil {
			logger.Error("adopt phase failed", "err", err)
			return 1
		}
		logger.Info("adopt phase complete, starting normal operation")
	}

	eventCh, errCh := watcher.Watch(ctx)
	ticker := time.NewTicker(cfg.ReconcileInterval)
	defer ticker.Stop()

	m.Up.Set(1)
	defer m.Up.Set(0)

	// Startup banner — build info.
	logger.Info("bollard starting",
		"version", version,
		"go", runtime.Version(),
		"os", runtime.GOOS,
		"arch", runtime.GOARCH,
	)

	// Config synopsis — no credentials.
	logger.Info("configuration",
		"unifi_host", cfg.UnifiHost,
		"unifi_site", cfg.UnifiSite,
		"unifi_tls_verify", !cfg.UnifiSkipTLSVerify,
		"database", sanitizeDSN(cfg.DatabaseURL),
		"reconcile_interval", cfg.ReconcileInterval,
		"metrics_addr", cfg.MetricsAddr,
		"log_format", cfg.LogFormat,
		"log_level", cfg.LogLevel,
	)

	// State store synopsis.
	if existing, err := db.ListAll(context.Background()); err == nil {
		logger.Info("state store ready", "owned_records", len(existing))
	}

	logger.Info("bollard started", "reconcile_interval", cfg.ReconcileInterval)

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return 0
		case err := <-errCh:
			if dockerStreamStopped(ctx, err) {
				return 0
			}
			logger.Error("docker event stream error", "err", err)
			return 1
		case e, ok := <-eventCh:
			if !ok {
				return 0
			}
			m.DockerEventsTotal.WithLabelValues(e.Type).Inc()
			if err := rec.HandleEvent(ctx, e); err != nil {
				m.DockerEventErrorsTotal.WithLabelValues("handle").Inc()
				logger.Error("handle event failed", "container", e.Ref(), "err", err)
			}
		case <-ticker.C:
			start := time.Now()
			if err := rec.Reconcile(ctx); err != nil {
				m.ReconcileIterationsTotal.WithLabelValues("failure").Inc()
				logger.Error("reconcile failed", "err", err)
			} else {
				m.ReconcileIterationsTotal.WithLabelValues("success").Inc()
				m.ReconcileDuration.Observe(time.Since(start).Seconds())
				m.ReconcileLastTimestamp.Set(float64(time.Now().Unix()))
			}
			if recs, err := db.ListAll(ctx); err == nil {
				m.RecordsActive.Set(float64(len(recs)))
			}
		}
	}
}

func connectUniFi(ctx context.Context, cfg *unifi.Config, logger *slog.Logger) (unifi.DNSProvider, error) {
	delay := 500 * time.Millisecond
	for {
		provider, err := unifi.New(ctx, cfg)
		if err == nil || !retryableUniFiStartup(err) {
			return provider, err
		}
		logger.Warn("UniFi unavailable; retrying", "delay", delay)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay = min(2*delay, 30*time.Second)
	}
}

func retryableUniFiStartup(err error) bool {
	return unifi.IsRetryableStartupError(err) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET)
}

// dockerStreamStopped reports a clean end of the Docker event stream: no error, or the
// cancellation caused by our own shutdown signal racing the select in run.
func dockerStreamStopped(ctx context.Context, err error) bool {
	return err == nil || (ctx.Err() != nil && errors.Is(err, context.Canceled))
}

type dockerLister struct {
	client *dockerclient.Client
}

func (d *dockerLister) ListRunning(ctx context.Context) (map[string]map[string]string, error) {
	containers, err := d.client.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, err
	}
	result := make(map[string]map[string]string, len(containers))
	for _, ctr := range containers {
		result[ctr.ID] = ctr.Labels
	}
	return result, nil
}

// sanitizeDSN strips credentials from a DSN for safe logging.
// Postgres URIs have the form postgres://user:pass@host/db — the password
// is replaced with "***", as are password and sslpassword query parameters
// (pgx accepts them). SQLite file paths are returned unchanged.
func sanitizeDSN(dsn string) string {
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok || (scheme != "postgres" && scheme != "postgresql") {
		return dsn
	}
	// Edit the raw text like pgx's own redaction so every unrelated byte survives.
	var out strings.Builder
	out.WriteString(scheme + "://")
	if i := strings.IndexAny(rest, "@/"); i >= 0 && rest[i] == '@' {
		user, _, hasPassword := strings.Cut(rest[:i], ":")
		out.WriteString(user)
		if hasPassword {
			out.WriteString(":***")
		}
		out.WriteByte('@')
		rest = rest[i+1:]
	}
	path, query, hasQuery := strings.Cut(rest, "?")
	out.WriteString(path)
	if !hasQuery {
		return out.String()
	}
	out.WriteByte('?')
	for i, pair := range strings.Split(query, "&") {
		if i > 0 {
			out.WriteByte('&')
		}
		key, _, hasValue := strings.Cut(pair, "=")
		if hasValue && isDSNSecretKey(key) {
			out.WriteString(key + "=***")
		} else {
			out.WriteString(pair)
		}
	}
	return out.String()
}

// isDSNSecretKey applies pgx's key decoding: literal edge spaces are ignored, then percent escapes.
func isDSNSecretKey(raw string) bool {
	key := strings.Trim(raw, " ")
	if decoded, err := url.PathUnescape(key); err == nil {
		key = decoded
	}
	return key == "password" || key == "sslpassword"
}
