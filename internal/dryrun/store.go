package dryrun

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/j0sh3rs/bollard/internal/store"
)

// Store reads the real ownership rows and keeps every write in memory, so a dry run sees what is
// already owned but never changes the database.
type Store struct {
	base    store.Store
	mu      sync.Mutex
	created map[string]store.Record // row ID -> row
	updated map[string]store.Record
	deleted map[string]struct{}
}

// NewStore wraps base. Close does not close base; the caller owns it.
func NewStore(base store.Store) *Store {
	return &Store{
		base:    base,
		created: map[string]store.Record{},
		updated: map[string]store.Record{},
		deleted: map[string]struct{}{},
	}
}

var _ store.Store = (*Store)(nil)

func (s *Store) merged(ctx context.Context) ([]store.Record, error) {
	rows, err := s.base.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.Record, 0, len(rows)+len(s.created))
	for _, r := range rows {
		if _, gone := s.deleted[r.ID]; gone {
			continue
		}
		if u, ok := s.updated[r.ID]; ok {
			r = u
		}
		out = append(out, r)
	}
	for _, r := range s.created {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hostname != out[j].Hostname {
			return out[i].Hostname < out[j].Hostname
		}
		return out[i].ContainerID < out[j].ContainerID
	})
	return out, nil
}

// Create records the row in memory, refusing a second row for the same container and hostname.
func (s *Store) Create(ctx context.Context, r store.Record) error {
	rows, err := s.merged(ctx)
	if err != nil {
		return err
	}
	for _, e := range rows {
		if e.ContainerID == r.ContainerID && e.Hostname == r.Hostname {
			return fmt.Errorf("dryrun store: duplicate row for container %s hostname %s", r.ContainerID, r.Hostname)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created[r.ID] = r
	return nil
}

// Update replaces a row in memory.
func (s *Store) Update(ctx context.Context, r store.Record) error {
	rows, err := s.merged(ctx)
	if err != nil {
		return err
	}
	for _, e := range rows {
		if e.ID == r.ID {
			s.mu.Lock()
			defer s.mu.Unlock()
			if _, fake := s.created[r.ID]; fake {
				s.created[r.ID] = r
			} else {
				s.updated[r.ID] = r
			}
			return nil
		}
	}
	return fmt.Errorf("dryrun store: no row %s", r.ID)
}

// Delete hides a row.
func (s *Store) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, fake := s.created[id]; fake {
		delete(s.created, id)
		return nil
	}
	s.deleted[id] = struct{}{}
	delete(s.updated, id)
	return nil
}

// ListByContainerID returns the merged rows of one container.
func (s *Store) ListByContainerID(ctx context.Context, containerID string) ([]store.Record, error) {
	rows, err := s.merged(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.Record
	for _, r := range rows {
		if r.ContainerID == containerID {
			out = append(out, r)
		}
	}
	return out, nil
}

// DeleteByContainerID hides every row of one container and returns them.
func (s *Store) DeleteByContainerID(ctx context.Context, containerID string) ([]store.Record, error) {
	rows, err := s.ListByContainerID(ctx, containerID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if err := s.Delete(ctx, r.ID); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// ListAll returns the merged rows.
func (s *Store) ListAll(ctx context.Context) ([]store.Record, error) { return s.merged(ctx) }

// Close is a no-op: the real store belongs to the caller.
func (s *Store) Close() error { return nil }
