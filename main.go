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
	"github.com/j0sh3rs/bollard/internal/dnsname"
	dockerwatcher "github.com/j0sh3rs/bollard/internal/docker"
	"github.com/j0sh3rs/bollard/internal/dryrun"
	bollardlog "github.com/j0sh3rs/bollard/internal/log"
	"github.com/j0sh3rs/bollard/internal/metrics"
	"github.com/j0sh3rs/bollard/internal/reconciler"
	"github.com/j0sh3rs/bollard/internal/store"
	"github.com/j0sh3rs/bollard/internal/unifi"
	"github.com/jackc/pgx/v5/pgconn"
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
		addr = "127.0.0.1:9090"
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

	baseDB, err := store.NewStore(cfg.DatabaseURL, cfg.InstanceTag)
	if err != nil {
		logger.Error("state store unavailable", "err", err)
		return 1
	}
	defer baseDB.Close()
	db := store.Store(baseDB)
	if cfg.DryRun {
		db = dryrun.NewStore(baseDB)
	}

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
	if cfg.DryRun {
		provider = dryrun.New(provider, logger)
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
	rec := reconciler.New(db, provider, lister, "", m, logger).
		WithReservedHostnames(reservedHostnames(cfg.UnifiHost, cfg.DatabaseURL, os.Getenv("DOCKER_HOST"))...).
		WithNetworks(lister).
		WithStopDrain(cfg.StopDrain)
	if cfg.InferTraefik {
		rec.WithTraefikInference()
	}

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
		"unifi_tls_verify", cfg.UnifiCACert != "" || !cfg.UnifiSkipTLSVerify,
		"database", sanitizeDSN(cfg.DatabaseURL),
		"instance_tag", cfg.InstanceTag,
		"reconcile_interval", cfg.ReconcileInterval,
		"stop_drain", cfg.StopDrain,
		"infer_traefik_hosts", cfg.InferTraefik,
		"dry_run", cfg.DryRun,
		"metrics_addr", cfg.MetricsAddr,
		"log_format", cfg.LogFormat,
		"log_level", cfg.LogLevel,
	)
	if cfg.DryRun {
		logger.Warn("DRY_RUN is on: records are logged, not created, changed or deleted, and ownership rows are not written")
	}
	if cfg.UnifiCACert == "" && cfg.UnifiSkipTLSVerify {
		logger.Warn("UniFi TLS certificate verification is disabled; the API key can be captured by anyone who can impersonate the controller. Set UNIFI_SKIP_TLS_VERIFY=false or UNIFI_CA_CERT")
	}

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
			return streamExit(ctx, err, logger)
		case e, ok := <-eventCh:
			if !ok {
				// Watch closes errCh before eventCh, so a stream error is already buffered. A
				// ready closed eventCh can win the select against it; read the error here.
				return streamExit(ctx, pendingStreamErr(errCh), logger)
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

// dockerStreamStopped reports a clean end of the Docker event stream: our own shutdown signal
// cancelled it, either as no error or as context.Canceled. Anything else, including a stream
// that ends with no error while we are still running, is a failure.
func dockerStreamStopped(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled))
}

// streamExit returns the process exit code after the Docker event stream ended with err
// (nil when only the channels closed), logging any failure.
func streamExit(ctx context.Context, err error, logger *slog.Logger) int {
	if dockerStreamStopped(ctx, err) {
		return 0
	}
	if err == nil {
		err = errors.New("event stream closed unexpectedly")
	}
	logger.Error("docker event stream error", "err", err)
	return 1
}

// pendingStreamErr returns an error already buffered on errCh without blocking, or nil.
func pendingStreamErr(errCh <-chan error) error {
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
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

// controllerHostname returns the host part of UNIFI_HOST (scheme optional), or "" if it is not parseable.
// IP literals are returned as-is; they are never valid DNS hostnames, so the reserved-name guard ignores them.
func controllerHostname(raw string) string {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// reservedHostnames lists names the daemon (or its containers) resolve for control-plane traffic:
// the UniFi controller, Postgres hosts in DATABASE_URL, a TCP DOCKER_HOST, and Docker's well-known
// host aliases. Label authors must not be able to create or adopt records for them.
func reservedHostnames(unifiHost, databaseURL, dockerHost string) []string {
	names := append([]string{controllerHostname(unifiHost)}, dnsname.HostAliases...)
	names = append(names, postgresHostnames(databaseURL)...)
	if u, err := url.Parse(dockerHost); err == nil && dockerHost != "" {
		names = append(names, u.Hostname())
	}
	return names
}

// postgresHostnames returns every host pgx will actually dial for a postgres:// DSN: the primary
// and all fallbacks, after pgx's own handling of multi-host lists and the "host" query override.
// Unix socket paths are skipped. Keyword/value DSNs are handled by SQLite in store.NewStore,
// so they yield nothing.
func postgresHostnames(dsn string) []string {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return nil
	}
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return nil // the store fails to open the same DSN, so the daemon never starts
	}
	hosts := []string{cfg.Host}
	for _, fb := range cfg.Fallbacks {
		hosts = append(hosts, fb.Host)
	}
	var out []string
	seen := map[string]bool{} // pgx repeats each host per TLS fallback
	for _, h := range hosts {
		if h != "" && !strings.HasPrefix(h, "/") && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

func (d *dockerLister) ContainerNetworks(ctx context.Context, containerID string) (map[string]dockerwatcher.NetworkAddr, error) {
	return dockerwatcher.ContainerNetworks(ctx, d.client, containerID)
}
