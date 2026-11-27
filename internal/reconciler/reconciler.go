package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/j0sh3rs/bollard/internal/dnsname"
	"github.com/j0sh3rs/bollard/internal/docker"
	"github.com/j0sh3rs/bollard/internal/metrics"
	"github.com/j0sh3rs/bollard/internal/resolver"
	"github.com/j0sh3rs/bollard/internal/store"
	"github.com/j0sh3rs/bollard/internal/unifi"
)

const (
	// rollbackTimeout bounds the compensating delete after a failed ownership write.
	rollbackTimeout = 5 * time.Second
	// drainTTLSeconds is the TTL a draining record is lowered to, so resolver caches expire
	// soon after the record is finally deleted.
	drainTTLSeconds = 10
)

// ContainerLister returns a snapshot of running containers with their labels.
type ContainerLister interface {
	ListRunning(ctx context.Context) (map[string]map[string]string, error)
}

// NetworkLister returns a container's addresses on its Docker networks.
type NetworkLister interface {
	ContainerNetworks(ctx context.Context, containerID string) (map[string]docker.NetworkAddr, error)
}

// Reconciler orchestrates DNS record lifecycle.
type Reconciler struct {
	store             store.Store
	provider          unifi.DNSProvider
	updater           unifi.RecordUpdater
	lister            ContainerLister
	networks          NetworkLister
	hostIP            string
	resolveOnce       sync.Once
	resolveErr        error
	hostIP6           string
	resolve6Once      sync.Once
	resolve6Err       error
	metrics           *metrics.Metrics
	reserved          map[string]struct{}
	parse             docker.ParseOptions
	drain             time.Duration
	now               func() time.Time
	emptyUnifiTicks   int // consecutive ticks with a suspicious empty UniFi list
	emptyRunningTicks int // consecutive ticks with a suspicious empty container list
	log               *slog.Logger
}

// New creates a Reconciler. hostIP may be empty (inferred on first use).
// m may be nil; all metric calls are no-ops when nil.
func New(s store.Store, p unifi.DNSProvider, lister ContainerLister, hostIP string, m *metrics.Metrics, log *slog.Logger) *Reconciler {
	r := &Reconciler{store: s, provider: p, lister: lister, hostIP: hostIP, metrics: m, log: log, now: time.Now}
	r.updater, _ = p.(unifi.RecordUpdater)
	return r
}

// WithReservedHostnames makes the reconciler refuse to create records for the given names.
// Use it for names bollard itself resolves (the UniFi controller host), so a label author
// cannot redirect the daemon's own API traffic with a vacant name.
func (r *Reconciler) WithReservedHostnames(names ...string) *Reconciler {
	r.reserved = make(map[string]struct{}, len(names))
	for _, n := range names {
		if n = dnsname.Canonical(n); n != "" {
			r.reserved[n] = struct{}{}
		}
	}
	return r
}

// WithTraefikInference derives hostnames from Traefik router rule labels for containers that
// have no dns.bollard/hostname label.
func (r *Reconciler) WithTraefikInference() *Reconciler {
	r.parse.InferTraefik = true
	return r
}

// WithNetworks lets records publish a container's own address (a macvlan or ipvlan network, or
// the network named by dns.bollard/network) instead of the host's.
func (r *Reconciler) WithNetworks(n NetworkLister) *Reconciler {
	r.networks = n
	return r
}

// WithStopDrain keeps a stopped container's record, with a short TTL, for d before deleting it,
// so a restart can take the record over without a resolution gap. It needs a provider that
// implements unifi.RecordUpdater; without one draining stays off.
func (r *Reconciler) WithStopDrain(d time.Duration) *Reconciler {
	if d > 0 && r.updater == nil {
		r.log.Warn("STOP_DRAIN ignored: the UniFi provider cannot update records")
		return r
	}
	r.drain = d
	return r
}

// WithClock replaces the time source used for drain deadlines.
func (r *Reconciler) WithClock(now func() time.Time) *Reconciler {
	r.now = now
	return r
}

// addrResolver picks the value of a container's records, inspecting its networks at most once.
type addrResolver struct {
	r           *Reconciler
	ctx         context.Context
	containerID string
	loaded      bool
	nets        map[string]docker.NetworkAddr
	netsErr     error
}

func (r *Reconciler) addrs(ctx context.Context, containerID string) *addrResolver {
	return &addrResolver{r: r, ctx: ctx, containerID: containerID}
}

func (a *addrResolver) networks() (map[string]docker.NetworkAddr, error) {
	if !a.loaded {
		a.loaded = true
		a.nets, a.netsErr = a.r.networks.ContainerNetworks(a.ctx, a.containerID)
	}
	return a.nets, a.netsErr
}

func pick(n docker.NetworkAddr, v6 bool) string {
	if v6 {
		return n.IPv6
	}
	return n.IPv4
}

// resolve returns the address an A or AAAA record publishes: the ip-override label, the named
// network's address, the address on the container's only macvlan/ipvlan network, or the host's.
func (a *addrResolver) resolve(spec docker.RecordSpec) (string, error) {
	if spec.IPOverride != "" {
		return spec.IPOverride, nil
	}
	v6 := spec.RecordType == "AAAA"
	if spec.Network != "" {
		if a.r.networks == nil {
			return "", fmt.Errorf("label dns.bollard/network needs Docker network inspection, which is unavailable")
		}
		nets, err := a.networks()
		if err != nil {
			return "", fmt.Errorf("reconciler: inspect networks of %s: %w", a.containerID, err)
		}
		n, ok := nets[spec.Network]
		if !ok {
			return "", fmt.Errorf("reconciler: container is not attached to network %q", spec.Network)
		}
		if v := pick(n, v6); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("reconciler: network %q gives the container no %s address", spec.Network, spec.RecordType)
	}
	if a.r.networks != nil {
		nets, err := a.networks()
		if err != nil {
			return "", fmt.Errorf("reconciler: inspect networks of %s: %w", a.containerID, err)
		}
		var names []string
		for name, n := range nets {
			if n.Routable() && pick(n, v6) != "" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		switch len(names) {
		case 0:
		case 1:
			return pick(nets[names[0]], v6), nil
		default:
			return "", fmt.Errorf("reconciler: container is on several routable networks %v; set dns.bollard/network or ip-override", names)
		}
	}
	return a.r.hostAddr(v6)
}

func (r *Reconciler) hostAddr(v6 bool) (string, error) {
	if v6 {
		r.resolve6Once.Do(func() {
			r.hostIP6, r.resolve6Err = resolver.HostIPv6("")
		})
		return r.hostIP6, r.resolve6Err
	}
	if r.hostIP != "" {
		return r.hostIP, nil
	}
	r.resolveOnce.Do(func() {
		r.hostIP, r.resolveErr = resolver.HostIP("")
	})
	return r.hostIP, r.resolveErr
}

// valueFor returns the record value: the CNAME target, or the resolved address.
func (a *addrResolver) valueFor(spec docker.RecordSpec) (string, error) {
	if spec.RecordType == "CNAME" {
		return "", nil
	}
	return a.resolve(spec)
}

func recordFor(spec docker.RecordSpec, value string) unifi.DNSRecord {
	rec := unifi.DNSRecord{Hostname: spec.Hostname, RecordType: spec.RecordType, TTL: spec.TTL, IP: value}
	if spec.RecordType == "CNAME" {
		rec.Target = spec.Target
	}
	return rec
}

// sameValue reports whether an existing record already says what spec would publish.
func sameValue(rec unifi.DNSRecord, spec docker.RecordSpec, value string) bool {
	if rec.RecordType != spec.RecordType {
		return false
	}
	if spec.RecordType == "CNAME" {
		return dnsname.Canonical(rec.Target) == spec.Target
	}
	a, b := net.ParseIP(rec.IP), net.ParseIP(value)
	return a != nil && b != nil && a.Equal(b)
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

type occupiedName struct {
	count  int
	record *unifi.DNSRecord
}

type occupiedNames map[string]occupiedName

func indexNames(records []unifi.DNSRecord) occupiedNames {
	names := make(occupiedNames, len(records))
	for i := range records {
		rec := &records[i]
		name := dnsname.Canonical(rec.Hostname)
		entry := names[name]
		entry.count++
		if entry.count == 1 {
			entry.record = rec
		} else {
			entry.record = nil
		}
		names[name] = entry
	}
	return names
}

func (names occupiedNames) remove(name string) {
	entry := names[name]
	if entry.count <= 1 {
		delete(names, name)
		return
	}
	entry.count--
	names[name] = entry
}

func (r *Reconciler) countHostnameConflict() {
	if r.metrics != nil {
		r.metrics.HostnameConflictsTotal.Inc()
	}
}

func draining(row store.Record) bool { return !row.DrainUntil.IsZero() }

func rowKey(containerID, hostname string) string {
	return containerID + "\x00" + dnsname.Canonical(hostname)
}

func (r *Reconciler) handleStart(ctx context.Context, e docker.Event) error {
	specs, err := docker.ParseLabels(e.Labels, r.parse)
	if err != nil {
		r.log.Warn("invalid labels on container", "container", e.Ref(), "err", err)
		return nil
	}
	if len(specs) == 0 {
		return nil
	}

	all, err := r.store.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: list store: %w", err)
	}
	hasRows := false
	owners := map[string]store.Record{}
	for _, row := range all {
		if row.ContainerID == e.ContainerID {
			hasRows = true
			continue
		}
		owners[dnsname.Canonical(row.Hostname)] = row
	}
	if hasRows {
		// Reconcile owns repairs; a repeated start must not replace or extend its rows.
		return nil
	}

	ar := r.addrs(ctx, e.ContainerID)
	var occupied occupiedNames // listed once, on first need
	var errs []error
	for _, spec := range specs {
		if owner, ok := owners[spec.Hostname]; ok {
			if draining(owner) {
				errs = append(errs, r.takeover(ctx, owner, e, spec, ar))
				continue
			}
			r.countHostnameConflict()
			errs = append(errs, fmt.Errorf("reconciler: hostname %q already owned by container %s", spec.Hostname, owner.ContainerID))
			continue
		}
		if occupied == nil {
			records, err := r.provider.ListRecords(ctx)
			if err != nil {
				return errors.Join(append(errs, fmt.Errorf("reconciler: list unifi records: %w", err))...)
			}
			occupied = indexNames(records)
		}
		if occupied[spec.Hostname].count > 0 {
			r.countHostnameConflict()
			errs = append(errs, fmt.Errorf("reconciler: hostname %q already occupied in UniFi", spec.Hostname))
			continue
		}
		errs = append(errs, r.createRecord(ctx, e, spec, ar, nil))
	}
	return errors.Join(errs...)
}

// takeover hands a draining record to the container that now wants its name: the record is
// rewritten for the new container (address and TTL) and the row stops draining.
func (r *Reconciler) takeover(ctx context.Context, owner store.Record, e docker.Event, spec docker.RecordSpec, ar *addrResolver) error {
	if r.updater == nil {
		return fmt.Errorf("reconciler: cannot take over draining record %q: provider cannot update records", spec.Hostname)
	}
	if _, reserved := r.reserved[spec.Hostname]; reserved {
		r.countHostnameConflict()
		return fmt.Errorf("reconciler: hostname %q is reserved, refusing to take it over for %s", spec.Hostname, e.Ref())
	}
	value, err := ar.valueFor(spec)
	if err != nil {
		return fmt.Errorf("reconciler: resolve address for %s (%s): %w", spec.Hostname, e.Ref(), err)
	}
	if err := r.updater.UpdateRecord(ctx, owner.UnifiRecordID, recordFor(spec, value)); err != nil {
		return fmt.Errorf("reconciler: take over record %s for %s: %w", spec.Hostname, e.Ref(), err)
	}
	owner.ContainerID = e.ContainerID
	owner.RecordType, owner.TTL, owner.IP, owner.Target = spec.RecordType, spec.TTL, value, ""
	if spec.RecordType == "CNAME" {
		owner.Target = spec.Target
	}
	owner.DrainUntil = time.Time{}
	owner.UpdatedAt = r.now().UTC()
	if err := r.store.Update(ctx, owner); err != nil {
		return fmt.Errorf("reconciler: update store row for %s: %w", spec.Hostname, err)
	}
	r.log.Info("took over draining DNS record", "hostname", spec.Hostname, "container", e.Ref())
	return nil
}

func (r *Reconciler) createRecord(ctx context.Context, e docker.Event, spec docker.RecordSpec, ar *addrResolver, names occupiedNames) error {
	if _, ok := r.reserved[spec.Hostname]; ok {
		r.countHostnameConflict()
		return fmt.Errorf("reconciler: hostname %q is reserved (UniFi controller host), refusing to create it for %s", spec.Hostname, e.Ref())
	}
	value, err := ar.valueFor(spec)
	if err != nil {
		if r.metrics != nil {
			r.metrics.RecordsTotal.WithLabelValues("created", "false").Inc()
		}
		return fmt.Errorf("reconciler: resolve address for %s (%s): %w", spec.Hostname, e.Ref(), err)
	}

	if names != nil {
		// A failed POST can still have created a record.
		names[spec.Hostname] = occupiedName{count: 1}
	}
	unifiID, err := r.provider.CreateRecord(ctx, recordFor(spec, value))
	if err != nil {
		// An ambiguous failure (timeout, lost response, no ID) may have committed the record. Claim it
		// only when exactly one record matches name, type and value; a 4xx means it did not commit.
		if !unifi.IsRejection(err) {
			unifiID = r.findCommitted(ctx, spec, value)
		}
		if unifiID == "" {
			if r.metrics != nil {
				r.metrics.RecordsTotal.WithLabelValues("created", "false").Inc()
			}
			return fmt.Errorf("reconciler: create record %s for %s: %w", spec.Hostname, e.Ref(), err)
		}
		r.log.Warn("recovered UniFi record after a failed create", "hostname", spec.Hostname, "unifi_id", unifiID, "err", err)
	}

	if err := r.saveOwnership(ctx, e.ContainerID, spec, value, unifiID); err != nil {
		// Roll back on a context that survives shutdown cancellation: the cancelled ctx is the likely
		// reason the store write failed, and it would fail the delete too and orphan the UniFi record.
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		if rbErr := r.provider.DeleteRecord(rbCtx, unifiID); rbErr == nil {
			delete(names, spec.Hostname)
		} else {
			r.log.Error("rollback delete of unowned UniFi record failed; remove it manually",
				"hostname", spec.Hostname, "unifi_id", unifiID, "err", rbErr)
		}
		cancel()
		if r.metrics != nil {
			r.metrics.RecordsTotal.WithLabelValues("created", "false").Inc()
		}
		return fmt.Errorf("reconciler: write store for %s: %w", spec.Hostname, err)
	}

	if r.metrics != nil {
		r.metrics.RecordsTotal.WithLabelValues("created", "true").Inc()
	}
	r.log.Info("created DNS record", "hostname", spec.Hostname, "type", spec.RecordType, "value", displayValue(spec, value), "container", e.Ref())
	return nil
}

func displayValue(spec docker.RecordSpec, value string) string {
	if spec.RecordType == "CNAME" {
		return spec.Target
	}
	return value
}

// findCommitted returns the ID of the only record at spec.Hostname with the same type and value, or
// "" when there is none, several, one already owned by another row, or the lookup fails. Callers have
// just checked the name was free, so a match is the record a failed create committed.
func (r *Reconciler) findCommitted(ctx context.Context, spec docker.RecordSpec, value string) string {
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	records, err := r.provider.ListRecords(lookupCtx)
	if err != nil {
		r.log.Warn("could not look for a record committed by a failed create", "hostname", spec.Hostname, "err", err)
		return ""
	}
	owned := map[string]struct{}{}
	rows, err := r.store.ListAll(ctx)
	if err != nil {
		r.log.Warn("could not check ownership of a record committed by a failed create", "hostname", spec.Hostname, "err", err)
		return ""
	}
	for _, row := range rows {
		owned[row.UnifiRecordID] = struct{}{}
	}
	id, n := "", 0
	for _, rec := range records {
		if _, taken := owned[rec.ID]; taken {
			continue // another row already owns it; it is not ours to claim
		}
		if dnsname.Canonical(rec.Hostname) == spec.Hostname && rec.ID != "" && sameValue(rec, spec, value) {
			id, n = rec.ID, n+1
		}
	}
	if n != 1 {
		return ""
	}
	return id
}

func (r *Reconciler) saveOwnership(ctx context.Context, containerID string, spec docker.RecordSpec, value, unifiID string) error {
	now := r.now().UTC()
	row := store.Record{
		ID:            uuid.New().String(),
		ContainerID:   containerID,
		Hostname:      spec.Hostname,
		IP:            value,
		RecordType:    spec.RecordType,
		TTL:           spec.TTL,
		UnifiRecordID: unifiID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if spec.RecordType == "CNAME" {
		row.Target = spec.Target
	}
	return r.store.Create(ctx, row)
}

func (r *Reconciler) handleStop(ctx context.Context, e docker.Event) error {
	rows, err := r.store.ListByContainerID(ctx, e.ContainerID)
	if err != nil {
		return fmt.Errorf("reconciler: lookup store rows: %w", err)
	}
	var errs []error
	for i := range rows {
		if err := r.stopRow(ctx, e, &rows[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Reconciler) stopRow(ctx context.Context, e docker.Event, rec *store.Record) error {
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
	if r.drain > 0 && !draining(*rec) {
		err := r.startDrain(ctx, rec)
		if err == nil {
			r.log.Info("draining DNS record", "hostname", rec.Hostname, "container", e.Ref(), "until", rec.DrainUntil)
			return nil
		}
		// A stop must still clean up; draining is only a courtesy to quick restarts.
		r.log.Warn("could not start draining, deleting now", "hostname", rec.Hostname, "container", e.Ref(), "err", err)
	}
	if err := r.provider.DeleteRecord(ctx, rec.UnifiRecordID); err != nil {
		if r.metrics != nil {
			r.metrics.RecordsTotal.WithLabelValues("deleted", "false").Inc()
		}
		r.log.Error("failed to delete unifi record", "hostname", rec.Hostname, "container", e.Ref(), "unifi_id", rec.UnifiRecordID, "err", err)
		return fmt.Errorf("reconciler: delete record %s for %s: %w", rec.Hostname, e.Ref(), err)
	}
	if err := r.store.Delete(ctx, rec.ID); err != nil {
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

// startDrain lowers the record's TTL and marks the row as draining until the drain period ends.
func (r *Reconciler) startDrain(ctx context.Context, rec *store.Record) error {
	ttl := drainTTLSeconds
	if rec.TTL > 0 && rec.TTL < ttl {
		ttl = rec.TTL
	}
	update := unifi.DNSRecord{Hostname: rec.Hostname, RecordType: rec.RecordType, IP: rec.IP, Target: rec.Target, TTL: ttl}
	if err := r.updater.UpdateRecord(ctx, rec.UnifiRecordID, update); err != nil {
		return err
	}
	rec.DrainUntil = r.now().UTC().Add(r.drain)
	rec.UpdatedAt = r.now().UTC()
	return r.store.Update(ctx, *rec)
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
		if row.UnifiRecordID == rec.UnifiRecordID && row.ID != rec.ID && row.ContainerID != rec.ContainerID {
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

// confirmEmpty guards destructive passes against a bad-but-successful list response. A suspicious
// empty list (empty while the store has rows) is ignored the first time it is seen and acted on
// when it repeats on the next tick, so a transient empty or truncated answer cannot wipe state.
func (r *Reconciler) confirmEmpty(ticks *int, suspicious bool, msg string) bool {
	if !suspicious {
		*ticks = 0
		return true
	}
	*ticks++
	if *ticks >= 2 {
		return true
	}
	r.log.Warn(msg)
	return false
}

// Reconcile performs one full reconcile tick. It performs three passes:
//  1. Orphan cleanup: store rows whose UniFi record has been deleted externally
//     are removed from the store.
//  2. Retry deletes: records whose container is no longer running are
//     deleted (a failed stop delete, or a missed die/destroy event), and
//     their store rows removed. Records still referenced by a running row
//     are never deleted, and a draining record is kept until its drain ends.
//  3. Missed-event recovery: running containers that carry bollard labels but
//     have no store row for a requested hostname (e.g. events dropped during
//     a restart) are registered, taking over a draining record when one holds the name.
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
	names := indexNames(unifiRecords)
	unifiIndex := make(map[string]*unifi.DNSRecord, len(unifiRecords))
	for i := range unifiRecords {
		unifiIndex[unifiRecords[i].ID] = &unifiRecords[i]
	}
	allowOrphanCleanup := r.confirmEmpty(&r.emptyUnifiTicks, len(unifiRecords) == 0 && len(storeRecords) > 0,
		"reconcile: UniFi list is empty but store has rows; deferring orphan cleanup one tick")
	ownedNames := make(map[string]int, len(storeRecords))
	rowKeys := make(map[string]struct{}, len(storeRecords))
	drainingByName := map[string]store.Record{}
	for _, sr := range storeRecords {
		rowKeys[rowKey(sr.ContainerID, sr.Hostname)] = struct{}{}
		if _, exists := unifiIndex[sr.UnifiRecordID]; !exists && allowOrphanCleanup {
			r.log.Warn("orphaned store record, cleaning up",
				"hostname", sr.Hostname, "unifi_id", sr.UnifiRecordID)
			if err := r.store.Delete(ctx, sr.ID); err == nil {
				if r.metrics != nil {
					r.metrics.OrphansCleanedTotal.Inc()
				}
				continue
			} else {
				r.log.Error("reconcile: delete orphaned store row failed", "id", sr.ID, "err", err)
			}
		}
		ownedNames[dnsname.Canonical(sr.Hostname)]++
		if draining(sr) {
			drainingByName[dnsname.Canonical(sr.Hostname)] = sr
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
	allowDeletes := r.confirmEmpty(&r.emptyRunningTicks, len(running) == 0 && len(storeRecords) > 0,
		"reconcile: container list is empty but store has rows; deferring deletes one tick")
	// Records still referenced by a running row are never deleted, even
	// when the stopped row is a duplicate of it.
	runningUnifiIDs := map[string]struct{}{}
	for _, sr := range storeRecords {
		if _, ok := running[sr.ContainerID]; ok {
			runningUnifiIDs[sr.UnifiRecordID] = struct{}{}
		}
	}
	for _, sr := range storeRecords {
		if !allowDeletes {
			break
		}
		if _, ok := running[sr.ContainerID]; ok {
			continue
		}
		if draining(sr) && r.now().Before(sr.DrainUntil) {
			continue // a restart may still claim it
		}
		rec, exists := unifiIndex[sr.UnifiRecordID]
		if !exists {
			continue // pass 1 already removed this row
		}
		if _, shared := runningUnifiIDs[sr.UnifiRecordID]; shared {
			r.log.Warn("reconcile: record shared with a running container, not deleting",
				"hostname", sr.Hostname, "container", sr.ContainerID, "unifi_id", sr.UnifiRecordID)
			continue
		}
		if rec != nil {
			if err := r.provider.DeleteRecord(ctx, sr.UnifiRecordID); err != nil {
				r.log.Warn("reconcile: retry delete unifi record failed",
					"hostname", sr.Hostname, "container", sr.ContainerID, "unifi_id", sr.UnifiRecordID, "err", err)
				continue
			}
			names.remove(dnsname.Canonical(rec.Hostname))
			unifiIndex[sr.UnifiRecordID] = nil // Other stopped rows may reference this ID.
		}
		if err := r.store.Delete(ctx, sr.ID); err != nil {
			r.log.Error("reconcile: delete store row after record delete failed", "id", sr.ID, "err", err)
			continue
		}
		ownedNames[dnsname.Canonical(sr.Hostname)]--
		delete(drainingByName, dnsname.Canonical(sr.Hostname))
		r.log.Info("reconcile: deleted record for stopped container", "hostname", sr.Hostname, "container", sr.ContainerID)
		if r.metrics != nil {
			r.metrics.RecordsTotal.WithLabelValues("deleted", "true").Inc()
		}
	}

	// Pass 3: missed-event recovery — running containers with labels but no store row.
	for containerID, labels := range running {
		specs, err := docker.ParseLabels(labels, r.parse)
		if err != nil || len(specs) == 0 {
			continue
		}
		ar := r.addrs(ctx, containerID)
		for _, spec := range specs {
			if _, owned := rowKeys[rowKey(containerID, spec.Hostname)]; owned {
				continue
			}
			if _, ok := r.reserved[spec.Hostname]; ok {
				r.countHostnameConflict()
				r.log.Warn("reconcile: hostname is reserved, not creating", "hostname", spec.Hostname, "container", containerID)
				continue
			}
			e := docker.Event{ContainerID: containerID}
			if owner, ok := drainingByName[spec.Hostname]; ok {
				if err := r.takeover(ctx, owner, e, spec, ar); err != nil {
					r.log.Error("reconcile: take over draining record failed", "container", containerID, "err", err)
				} else {
					delete(drainingByName, spec.Hostname)
					if r.metrics != nil {
						r.metrics.MissedRecoveredTotal.Inc()
					}
				}
				continue
			}
			if ownedNames[spec.Hostname] > 0 || names[spec.Hostname].count > 0 {
				r.countHostnameConflict()
				r.log.Warn("reconcile: hostname occupied, not creating", "hostname", spec.Hostname, "container", containerID)
				continue
			}
			if err := r.createRecord(ctx, e, spec, ar, names); err != nil {
				r.log.Error("reconcile: missed-event recovery failed", "container", containerID, "err", err)
			} else if r.metrics != nil {
				r.metrics.MissedRecoveredTotal.Inc()
			}
		}
	}
	return nil
}

// Adopt reclaims ownership of existing UniFi records for running containers.
// It creates records only when the name is vacant.
func (r *Reconciler) Adopt(ctx context.Context, running map[string]map[string]string) error {
	unifiRecords, err := r.provider.ListRecords(ctx)
	if err != nil {
		return fmt.Errorf("adopt: list unifi records: %w", err)
	}
	names := indexNames(unifiRecords)
	all, err := r.store.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("adopt: list store: %w", err)
	}
	ownedNames := make(map[string]bool, len(all))
	ownedContainers := make(map[string]bool, len(all))
	ownedIDs := make(map[string]bool, len(all))
	for _, rec := range all {
		ownedNames[dnsname.Canonical(rec.Hostname)] = true
		ownedContainers[rec.ContainerID] = true
		ownedIDs[rec.UnifiRecordID] = true
	}
	for containerID, labels := range running {
		if ownedContainers[containerID] {
			continue // reconcile repairs a container that already has rows
		}
		specs, err := docker.ParseLabels(labels, r.parse)
		if err != nil || len(specs) == 0 {
			continue
		}
		ar := r.addrs(ctx, containerID)
		for _, spec := range specs {
			r.adoptOne(ctx, containerID, spec, ar, names, ownedNames, ownedIDs)
		}
	}
	return nil
}

func (r *Reconciler) adoptOne(ctx context.Context, containerID string, spec docker.RecordSpec, ar *addrResolver,
	names occupiedNames, ownedNames, ownedIDs map[string]bool) {
	if _, ok := r.reserved[spec.Hostname]; ok {
		r.countHostnameConflict()
		r.log.Warn("adopt: hostname is reserved, skipping", "hostname", spec.Hostname, "container", containerID)
		return
	}
	if ownedNames[spec.Hostname] {
		r.countHostnameConflict()
		r.log.Warn("adopt: hostname already owned, skipping", "hostname", spec.Hostname, "container", containerID)
		return
	}
	entry := names[spec.Hostname]
	if entry.count == 0 {
		if err := r.createRecord(ctx, docker.Event{ContainerID: containerID}, spec, ar, names); err != nil {
			r.log.Error("adopt: create record failed", "container", containerID, "err", err)
		} else {
			ownedNames[spec.Hostname] = true
		}
		return
	}
	value, err := ar.valueFor(spec)
	if err != nil {
		r.log.Warn("adopt: cannot resolve address", "container", containerID, "err", err)
		return
	}
	rec := entry.record
	if rec == nil || rec.ID == "" || ownedIDs[rec.ID] || rec.Disabled || !sameValue(*rec, spec, value) {
		r.countHostnameConflict()
		r.log.Warn("adopt: no unique eligible record for occupied hostname", "hostname", spec.Hostname, "container", containerID)
		return
	}
	if err := r.saveOwnership(ctx, containerID, spec, value, rec.ID); err != nil {
		r.log.Error("adopt: store create failed", "container", containerID, "err", err)
		return
	}
	ownedNames[spec.Hostname] = true
	ownedIDs[rec.ID] = true
	if r.metrics != nil {
		r.metrics.RecordsTotal.WithLabelValues("adopted", "true").Inc()
	}
	r.log.Info("adopted existing unifi record", "hostname", spec.Hostname, "container", containerID)
}
