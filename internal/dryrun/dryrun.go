// Package dryrun wraps a unifi.DNSProvider so mutations are logged instead of sent.
package dryrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/j0sh3rs/bollard/internal/unifi"
)

// Provider reads from the real provider and applies every create, update and delete to an
// in-memory overlay, so later reconcile ticks see the intended state and do not repeat the same
// "would create" lines. Nothing it does reaches the controller.
type Provider struct {
	real    unifi.DNSProvider
	log     *slog.Logger
	mu      sync.Mutex
	created map[string]unifi.DNSRecord // fake ID -> record
	updated map[string]unifi.DNSRecord // real ID -> intended record
	deleted map[string]struct{}        // real IDs hidden from listings
	next    int
}

// New returns a dry-run wrapper around real.
func New(real unifi.DNSProvider, log *slog.Logger) *Provider {
	return &Provider{
		real: real, log: log,
		created: map[string]unifi.DNSRecord{},
		updated: map[string]unifi.DNSRecord{},
		deleted: map[string]struct{}{},
	}
}

var (
	_ unifi.DNSProvider   = (*Provider)(nil)
	_ unifi.RecordUpdater = (*Provider)(nil)
)

// ListRecords returns the real zone adjusted by the overlay.
func (p *Provider) ListRecords(ctx context.Context) ([]unifi.DNSRecord, error) {
	records, err := p.real.ListRecords(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]unifi.DNSRecord, 0, len(records)+len(p.created))
	for _, r := range records {
		if _, gone := p.deleted[r.ID]; gone {
			continue
		}
		if u, ok := p.updated[r.ID]; ok {
			r = u
		}
		out = append(out, r)
	}
	for _, r := range p.created {
		out = append(out, r)
	}
	return out, nil
}

// CreateRecord logs the record it would create and returns a fake ID.
func (p *Provider) CreateRecord(_ context.Context, r unifi.DNSRecord) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	r.ID = fmt.Sprintf("dry-run-%d", p.next)
	p.created[r.ID] = r
	p.log.Info("dry-run: would create record", "hostname", r.Hostname, "type", r.RecordType, "value", value(r), "ttl", r.TTL)
	return r.ID, nil
}

// UpdateRecord logs the change it would make.
func (p *Provider) UpdateRecord(_ context.Context, id string, r unifi.DNSRecord) error {
	if id == "" {
		return errors.New("dryrun: record ID required for update")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r.ID = id
	if _, fake := p.created[id]; fake {
		p.created[id] = r
	} else {
		p.updated[id] = r
	}
	p.log.Info("dry-run: would update record", "hostname", r.Hostname, "type", r.RecordType, "value", value(r), "ttl", r.TTL)
	return nil
}

// DeleteRecord logs the delete and hides the record from later listings.
func (p *Provider) DeleteRecord(_ context.Context, id string) error {
	if id == "" {
		return errors.New("dryrun: record ID required for delete")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if r, fake := p.created[id]; fake {
		delete(p.created, id)
		p.log.Info("dry-run: would delete record", "hostname", r.Hostname)
		return nil
	}
	p.deleted[id] = struct{}{}
	delete(p.updated, id)
	p.log.Info("dry-run: would delete record", "unifi_id", id)
	return nil
}

func value(r unifi.DNSRecord) string {
	if r.RecordType == "CNAME" {
		return r.Target
	}
	return r.IP
}
