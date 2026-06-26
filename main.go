package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
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
	flag.Parse()

	if *healthcheck {
		addr := os.Getenv("METRICS_ADDR")
		if addr == "" {
			addr = ":9090"
		}
		resp, err := http.Get("http://localhost" + addr + "/healthz") //nolint:noctx
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	logger, err := bollardlog.New(cfg.LogFormat, cfg.LogLevel)
	if err != nil {
		slog.Error("logger init error", "err", err)
		os.Exit(1)
	}
	slog.SetDefault(logger)

	m := metrics.New(version, runtime.Version())
	m.Up.Set(0)

	db, err := store.NewStore(cfg.DatabaseURL)
	if err != nil {
		logger.Error("state store unavailable", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	provider, err := unifi.New(&unifi.Config{
		Host:          cfg.UnifiHost,
		APIKey:        cfg.UnifiAPIKey,
		Site:          cfg.UnifiSite,
		SkipTLSVerify: cfg.UnifiSkipTLSVerify,
		CACertPath:    cfg.UnifiCACert,
	})
	if err != nil {
		logger.Error("unifi client init error", "err", err)
		os.Exit(1)
	}

	watcher, err := dockerwatcher.NewWatcher()
	if err != nil {
		logger.Error("docker socket unavailable", "err", err)
		os.Exit(1)
	}
	defer watcher.Close()

	listerClient, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		logger.Error("docker client for lister unavailable", "err", err)
		os.Exit(1)
	}
	defer listerClient.Close()

	lister := &dockerLister{client: listerClient}
	rec := reconciler.New(db, provider, lister, "", m, logger)

	// Start metrics HTTP server.
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		logger.Info("metrics server listening", "addr", cfg.MetricsAddr)
		if err := http.ListenAndServe(cfg.MetricsAddr, mux); err != nil {
			logger.Error("metrics server failed", "err", err)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if *adopt {
		logger.Info("starting adopt phase")
		running, err := lister.ListRunning(ctx)
		if err != nil {
			logger.Error("adopt: list containers failed", "err", err)
			os.Exit(1)
		}
		if err := rec.Adopt(ctx, running); err != nil {
			logger.Error("adopt phase failed", "err", err)
			os.Exit(1)
		}
		logger.Info("adopt phase complete, starting normal operation")
	}

	eventCh, errCh := watcher.Watch(ctx)
	ticker := time.NewTicker(cfg.ReconcileInterval)
	defer ticker.Stop()

	m.Up.Set(1)
	logger.Info("bollard started", "reconcile_interval", cfg.ReconcileInterval)

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		case err := <-errCh:
			if err != nil {
				logger.Error("docker event stream error", "err", err)
			}
			return
		case e, ok := <-eventCh:
			if !ok {
				return
			}
			m.DockerEventsTotal.WithLabelValues(e.Type).Inc()
			if err := rec.HandleEvent(ctx, e); err != nil {
				m.DockerEventErrorsTotal.WithLabelValues("handle").Inc()
				logger.Error("handle event failed", "container", e.ContainerID, "err", err)
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
