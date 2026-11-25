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
		r.log.Warn("invalid labels on container", "container", e.Ref(), "err", err)
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
		return fmt.Errorf("reconciler: resolve IP for %s (%s): %w", spec.Hostname, e.Ref(), err)
	}

	unifiID, err := r.provider.CreateRecord(ctx, unifi.DNSRecord{
		Hostname: spec.Hostname, IP: ip, RecordType: spec.RecordType, TTL: spec.TTL,
	})
	if err != nil {
		if r.metrics != nil {
			r.metrics.RecordsTotal.WithLabelValues("created", "false").Inc()
		}
		return fmt.Errorf("reconciler: create record %s for %s: %w", spec.Hostname, e.Ref(), err)
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
		return fmt.Errorf("reconciler: write store for %s: %w", spec.Hostname, err)
	}

	if r.metrics != nil {
		r.metrics.RecordsTotal.WithLabelValues("created", "true").Inc()
	}
	r.log.Info("created DNS record", "hostname", spec.Hostname, "ip", ip, "container", e.Ref())
	return nil
}

func (r *Reconciler) handleStop(ctx context.Context, e docker.Event) error {
	rec, err := r.store.GetByContainerID(ctx, e.ContainerID)
	if err != nil {
		return fmt.Errorf("reconciler: lookup store row: %w", err)
	}
	if rec == nil {
		return nil
	}

	shared, err := r.recordSharedWithRunning(ctx, rec)
	if err != nil {
		return fmt.Errorf("reconciler: shared-record check: %w", err)
	}
	if shared {
		// Another row for a running container references this record
		// (duplicate rows). Deleting it would remove the live
		// container's DNS name; the row stays until the duplicate is
		// resolved by hand.
		r.log.Warn("skipping record delete: shared with a running container",
			"hostname", rec.Hostname, "container", e.Ref(), "unifi_id", rec.UnifiRecordID)
		return nil
	}
	if err := r.provider.DeleteRecord(ctx, rec.UnifiRecordID); err != nil {
		if r.metrics != nil {
			r.metrics.RecordsTotal.WithLabelValues("deleted", "false").Inc()
		}
		r.log.Error("failed to delete unifi record", "hostname", rec.Hostname, "container", e.Ref(), "unifi_id", rec.UnifiRecordID, "err", err)
		return fmt.Errorf("reconciler: delete record %s for %s: %w", rec.Hostname, e.Ref(), err)
	}
	if _, err := r.store.DeleteByContainerID(ctx, e.ContainerID); err != nil {
		// The UniFi record is gone; the row is removed by orphan cleanup on
		// the next tick. Keep the error so the caller can retry the event.
		r.log.Error("failed to delete store row", "hostname", rec.Hostname, "container", e.Ref(), "err", err)
		return fmt.Errorf("reconciler: delete from store: %w", err)
	}
	if r.metrics != nil {
		r.metrics.RecordsTotal.WithLabelValues("deleted", "true").Inc()
	}
	r.log.Info("deleted DNS record", "hostname", rec.Hostname, "container", e.Ref())
	return nil
}

// recordSharedWithRunning reports whether another store row for a
// currently running container references the same UniFi record. Rows
// sharing one record can already exist in the store; deleting a shared
// record would remove a live container's DNS name.
func (r *Reconciler) recordSharedWithRunning(ctx context.Context, rec *store.Record) (bool, error) {
	all, err := r.store.ListAll(ctx)
	if err != nil {
		return false, fmt.Errorf("reconciler: list store: %w", err)
	}
	var siblings []string
	for _, row := range all {
		if row.UnifiRecordID == rec.UnifiRecordID && row.ContainerID != rec.ContainerID {
			siblings = append(siblings, row.ContainerID)
		}
	}
	if len(siblings) == 0 {
		return false, nil
	}
	if r.lister == nil {
		return true, nil // cannot verify running state; treat as shared (safe)
	}
	running, err := r.lister.ListRunning(ctx)
	if err != nil {
		return false, fmt.Errorf("reconciler: list running containers: %w", err)
	}
	for _, id := range siblings {
		if _, ok := running[id]; ok {
			return true, nil
		}
	}
	return false, nil
}

// Reconcile performs one full reconcile tick. It performs three passes:
//  1. Orphan cleanup: store rows whose UniFi record has been deleted externally
//     are removed from the store.
//  2. Retry deletes: records whose container is no longer running are
//     deleted (a failed stop delete, or a missed die/destroy event), and
//     their store rows removed. Records still referenced by a running row
//     are never deleted.
//  3. Missed-event recovery: running containers that carry bollard labels but
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

	// Pass 2: retry deletes for records whose container has stopped —
	// either a failed stop delete (the store row was kept) or a missed
	// die/destroy event. Only rows whose UniFi record still exists.
	if r.lister == nil {
		return nil
	}
	running, err := r.lister.ListRunning(ctx)
	if err != nil {
		r.log.Warn("reconcile: list running containers failed", "err", err)
		return nil // best-effort; don't fail the full tick
	}
	runningSet := make(map[string]struct{}, len(running))
	for containerID := range running {
		runningSet[containerID] = struct{}{}
	}
	// Records still referenced by a running row are never deleted, even
	// when the stopped row is a duplicate of it.
	runningUnifiIDs := map[string]struct{}{}
	for _, sr := range storeRecords {
		if _, ok := runningSet[sr.ContainerID]; ok {
			runningUnifiIDs[sr.UnifiRecordID] = struct{}{}
		}
	}
	for _, sr := range storeRecords {
		if _, ok := runningSet[sr.ContainerID]; ok {
			continue
		}
		if _, ok := unifiIndex[sr.UnifiRecordID]; !ok {
			continue // pass 1 already removed this row
		}
		if _, shared := runningUnifiIDs[sr.UnifiRecordID]; shared {
			r.log.Warn("reconcile: record shared with a running container, not deleting",
				"hostname", sr.Hostname, "container", sr.ContainerID, "unifi_id", sr.UnifiRecordID)
			continue
		}
		if err := r.provider.DeleteRecord(ctx, sr.UnifiRecordID); err != nil {
			r.log.Warn("reconcile: retry delete unifi record failed",
				"hostname", sr.Hostname, "container", sr.ContainerID, "unifi_id", sr.UnifiRecordID, "err", err)
			continue
		}
		if err := r.store.Delete(ctx, sr.ID); err != nil {
			r.log.Error("reconcile: delete store row after record delete failed", "id", sr.ID, "err", err)
			continue
		}
		r.log.Info("reconcile: deleted record for stopped container", "hostname", sr.Hostname, "container", sr.ContainerID)
		if r.metrics != nil {
			r.metrics.RecordsTotal.WithLabelValues("deleted", "true").Inc()
		}
	}

	// Pass 3: missed-event recovery — running containers with labels but no store row.
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

	// Hostnames owned by any store row — before this run or written during
	// it. A second adopt for the same hostname is a conflict, not an overwrite.
	ownedHostnames := map[string]struct{}{}
	all, err := r.store.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("adopt: list store: %w", err)
	}
	for _, rec := range all {
		ownedHostnames[rec.Hostname] = struct{}{}
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
			if _, owned := ownedHostnames[spec.Hostname]; owned {
				r.log.Warn("adopt: hostname already owned, skipping", "hostname", spec.Hostname, "container", containerID)
				if r.metrics != nil {
					r.metrics.HostnameConflictsTotal.Inc()
				}
				continue
			}
			if err := r.store.Create(ctx, store.Record{
				ID: uuid.New().String(), ContainerID: containerID,
				Hostname: spec.Hostname, IP: ip, RecordType: spec.RecordType,
				TTL: spec.TTL, UnifiRecordID: unifiRec.ID,
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				r.log.Error("adopt: store create failed", "container", containerID, "err", err)
				continue
			}
			ownedHostnames[spec.Hostname] = struct{}{}
			if r.metrics != nil {
				r.metrics.RecordsTotal.WithLabelValues("adopted", "true").Inc()
			}
			r.log.Info("adopted existing unifi record", "hostname", spec.Hostname, "container", containerID)
		} else {
			if err := r.handleStart(ctx, docker.Event{
				Type: "start", ContainerID: containerID, Labels: labels,
			}); err != nil {
				r.log.Error("adopt: create record failed", "container", containerID, "err", err)
			} else {
				ownedHostnames[spec.Hostname] = struct{}{}
			}
		}
	}
	return nil
}
