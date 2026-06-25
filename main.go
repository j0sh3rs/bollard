package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/j0sh3rs/bollard/internal/config"
	dockerwatcher "github.com/j0sh3rs/bollard/internal/docker"
	bollardlog "github.com/j0sh3rs/bollard/internal/log"
	"github.com/j0sh3rs/bollard/internal/reconciler"
	"github.com/j0sh3rs/bollard/internal/store"
	"github.com/j0sh3rs/bollard/internal/unifi"
)

func main() {
	adopt := flag.Bool("adopt", false, "adopt existing UniFi records before starting normal operation")
	flag.Parse()

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

	db, err := store.NewSQLite(cfg.DatabaseURL)
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

	rec := reconciler.New(db, provider, "", logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if *adopt {
		logger.Info("starting adopt phase")
		running, err := listRunningContainers(ctx)
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
			if err := rec.HandleEvent(ctx, e); err != nil {
				logger.Error("handle event failed", "container", e.ContainerID, "err", err)
			}
		case <-ticker.C:
			if err := rec.Reconcile(ctx); err != nil {
				logger.Error("reconcile failed", "err", err)
			}
		}
	}
}

func listRunningContainers(ctx context.Context) (map[string]map[string]string, error) {
	c, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	containers, err := c.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, err
	}
	result := make(map[string]map[string]string, len(containers))
	for _, ctr := range containers {
		result[ctr.ID] = ctr.Labels
	}
	return result, nil
}
