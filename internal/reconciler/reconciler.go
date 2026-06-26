package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/j0sh3rs/bollard/internal/docker"
	"github.com/j0sh3rs/bollard/internal/metrics"
	"github.com/j0sh3rs/bollard/internal/resolver"
	"github.com/j0sh3rs/bollard/internal/store"
	"github.com/j0sh3rs/bollard/internal/unifi"
)

// ContainerLister returns a snapshot of running containers with their labels.
type ContainerLister interface {
	ListRunning(ctx context.Context) (map[string]map[string]string, error)
}

// Reconciler orchestrates DNS record lifecycle.
type Reconciler struct {
	store       store.Store
	provider    unifi.DNSProvider
	lister      ContainerLister
	hostIP      string
	resolveOnce sync.Once
	resolveErr  error
	metrics     *metrics.Metrics
	log         *slog.Logger
}

// New creates a Reconciler. hostIP may be empty (inferred on first use).
// m may be nil; all metric calls are no-ops when nil.
func New(s store.Store, p unifi.DNSProvider, lister ContainerLister, hostIP string, m *metrics.Metrics, log *slog.Logger) *Reconciler {
	return &Reconciler{store: s, provider: p, lister: lister, hostIP: hostIP, metrics: m, log: log}
}

func (r *Reconciler) resolvedIP(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if r.hostIP != "" {
		return r.hostIP, nil
	}
	r.resolveOnce.Do(func() {
		r.hostIP, r.resolveErr = resolver.HostIP("")
	})
	if r.resolveErr != nil {
		return "", r.resolveErr
	}
	return r.hostIP, nil
}

// HandleEvent processes a single Docker container event.
func (r *Reconciler) HandleEvent(ctx context.Context, e docker.Event) error {
	switch e.Type {
	case "start":
		return r.handleStart(ctx, e)
	case "stop":
		return r.handleStop(ctx, e)
	default:
		return nil
	}
}

func (r *Reconciler) handleStart(ctx context.Context, e docker.Event) error {
	spec, err := docker.ParseLabels(e.Labels)
	if err != nil {
		r.log.Warn("invalid labels on container", "container", e.ContainerID, "err", err)
		return nil
	}
	if spec == nil || !spec.Enabled {
		return nil
	}

	all, err := r.store.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: list store: %w", err)
	}
	for _, rec := range all {
		if rec.Hostname == spec.Hostname {
			if r.metrics != nil {
				r.metrics.HostnameConflictsTotal.Inc()
			}
			return fmt.Errorf("reconciler: hostname %q already owned by container %s", spec.Hostname, rec.ContainerID)
		}
	}

	ip, err := r.resolvedIP(spec.IPOverride)
	if err != nil {
		if r.metrics != nil {
			r.metrics.RecordsTotal.WithLabelValues("created", "false").Inc()
		}
		return fmt.Errorf("reconciler: resolve IP: %w", err)
	}

	unifiID, err := r.provider.CreateRecord(ctx, unifi.DNSRecord{
		Hostname: spec.Hostname, IP: ip, RecordType: spec.RecordType, TTL: spec.TTL,
	})
	if err != nil {
		if r.metrics != nil {
			r.metrics.RecordsTotal.WithLabelValues("created", "false").Inc()
		}
		return fmt.Errorf("reconciler: create unifi record: %w", err)
	}

	now := time.Now().UTC()
	if err := r.store.Create(ctx, store.Record{
		ID:            uuid.New().String(),
		ContainerID:   e.ContainerID,
		Hostname:      spec.Hostname,
		IP:            ip,
		RecordType:    spec.RecordType,
		TTL:           spec.TTL,
		UnifiRecordID: unifiID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		_ = r.provider.DeleteRecord(ctx, unifiID)
		if r.metrics != nil {
			r.metrics.RecordsTotal.WithLabelValues("created", "false").Inc()
		}
		return fmt.Errorf("reconciler: write store: %w", err)
	}

	if r.metrics != nil {
		r.metrics.RecordsTotal.WithLabelValues("created", "true").Inc()
	}
	r.log.Info("created DNS record", "hostname", spec.Hostname, "ip", ip, "container", e.ContainerID)
	return nil
}

func (r *Reconciler) handleStop(ctx context.Context, e docker.Event) error {
	rec, err := r.store.DeleteByContainerID(ctx, e.ContainerID)
	if err != nil {
		if r.metrics != nil {
			r.metrics.RecordsTotal.WithLabelValues("deleted", "false").Inc()
		}
		return fmt.Errorf("reconciler: delete from store: %w", err)
	}
	if rec == nil {
		return nil
	}
	if err := r.provider.DeleteRecord(ctx, rec.UnifiRecordID); err != nil {
		if r.metrics != nil {
			r.metrics.RecordsTotal.WithLabelValues("deleted", "false").Inc()
		}
		r.log.Error("failed to delete unifi record", "unifi_id", rec.UnifiRecordID, "err", err)
		return fmt.Errorf("reconciler: delete unifi record: %w", err)
	}
	if r.metrics != nil {
		r.metrics.RecordsTotal.WithLabelValues("deleted", "true").Inc()
	}
	r.log.Info("deleted DNS record", "hostname", rec.Hostname, "container", e.ContainerID)
	return nil
}

// Reconcile performs one full reconcile tick. It performs two passes:
//  1. Orphan cleanup: store rows whose UniFi record has been deleted externally
//     are removed from the store.
//  2. Missed-event recovery: running containers that carry bollard labels but
//     have no store row (e.g. events dropped during a restart) are registered.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	storeRecords, err := r.store.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: list store: %w", err)
	}
	unifiRecords, err := r.provider.ListRecords(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: list unifi records: %w", err)
	}

	// Pass 1: remove store rows whose UniFi record no longer exists.
	unifiIndex := map[string]struct{}{}
	for _, ur := range unifiRecords {
		unifiIndex[ur.ID] = struct{}{}
	}
	for _, sr := range storeRecords {
		if _, exists := unifiIndex[sr.UnifiRecordID]; !exists {
			r.log.Warn("orphaned store record, cleaning up",
				"hostname", sr.Hostname, "unifi_id", sr.UnifiRecordID)
			_ = r.store.Delete(ctx, sr.ID)
			if r.metrics != nil {
				r.metrics.OrphansCleanedTotal.Inc()
			}
		}
	}

	// Pass 2: missed-event recovery — running containers with labels but no store row.
	if r.lister == nil {
		return nil
	}
	running, err := r.lister.ListRunning(ctx)
	if err != nil {
		r.log.Warn("reconcile: list running containers failed", "err", err)
		return nil // best-effort; don't fail the full tick
	}
	storeIndex := map[string]struct{}{}
	for _, sr := range storeRecords {
		storeIndex[sr.ContainerID] = struct{}{}
	}
	for containerID, labels := range running {
		if _, owned := storeIndex[containerID]; owned {
			continue
		}
		spec, err := docker.ParseLabels(labels)
		if err != nil || spec == nil || !spec.Enabled {
			continue
		}
		r.log.Info("reconcile: creating record for unlabeled running container", "container", containerID)
		if err := r.handleStart(ctx, docker.Event{
			Type: "start", ContainerID: containerID, Labels: labels,
		}); err != nil {
			r.log.Error("reconcile: missed-event recovery failed", "container", containerID, "err", err)
		} else {
			if r.metrics != nil {
				r.metrics.MissedRecoveredTotal.Inc()
			}
		}
	}
	return nil
}

// Adopt reclaims ownership of existing UniFi records for running containers.
// running is a map of containerID → labels. Non-destructive: if no matching
// UniFi record exists, handleStart is called to create one.
func (r *Reconciler) Adopt(ctx context.Context, running map[string]map[string]string) error {
	unifiRecords, err := r.provider.ListRecords(ctx)
	if err != nil {
		return fmt.Errorf("adopt: list unifi records: %w", err)
	}
	type key struct{ hostname, ip string }
	unifiByKey := map[key]unifi.DNSRecord{}
	for _, ur := range unifiRecords {
		unifiByKey[key{ur.Hostname, ur.IP}] = ur
	}
	for containerID, labels := range running {
		spec, err := docker.ParseLabels(labels)
		if err != nil || spec == nil || !spec.Enabled {
			continue
		}
		ip, err := r.resolvedIP(spec.IPOverride)
		if err != nil {
			r.log.Warn("adopt: cannot resolve IP", "container", containerID, "err", err)
			continue
		}
		existing, _ := r.store.GetByContainerID(ctx, containerID)
		if existing != nil {
			continue
		}
		k := key{spec.Hostname, ip}
		unifiRec, found := unifiByKey[k]
		now := time.Now().UTC()
		if found {
			if err := r.store.Create(ctx, store.Record{
				ID: uuid.New().String(), ContainerID: containerID,
				Hostname: spec.Hostname, IP: ip, RecordType: spec.RecordType,
				TTL: spec.TTL, UnifiRecordID: unifiRec.ID,
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				r.log.Error("adopt: store create failed", "container", containerID, "err", err)
				continue
			}
			if r.metrics != nil {
				r.metrics.RecordsTotal.WithLabelValues("adopted", "true").Inc()
			}
			r.log.Info("adopted existing unifi record", "hostname", spec.Hostname, "container", containerID)
		} else {
			if err := r.handleStart(ctx, docker.Event{
				Type: "start", ContainerID: containerID, Labels: labels,
			}); err != nil {
				r.log.Error("adopt: create record failed", "container", containerID, "err", err)
			}
		}
	}
	return nil
}
