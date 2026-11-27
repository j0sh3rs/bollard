package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"
)

// Record represents a DNS record managed by bollard.
type Record struct {
	ID            string
	ContainerID   string
	Hostname      string
	IP            string
	RecordType    string
	TTL           int
	UnifiRecordID string
	Target        string    // CNAME target; empty otherwise
	DrainUntil    time.Time // zero = active; non-zero = container stopped, record kept until this instant
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Store is the persistence interface for DNS records. A store only sees rows
// carrying its instance tag.
type Store interface {
	Create(ctx context.Context, r Record) error
	// Update replaces the mutable fields of the row with r.ID; it errors if no row exists.
	Update(ctx context.Context, r Record) error
	Delete(ctx context.Context, id string) error
	// ListByContainerID returns the container's records ordered by hostname.
	ListByContainerID(ctx context.Context, containerID string) ([]Record, error)
	// DeleteByContainerID returns the rows it deleted.
	DeleteByContainerID(ctx context.Context, containerID string) ([]Record, error)
	ListAll(ctx context.Context) ([]Record, error)
	Close() error
}

const recordCols = `id, container_id, hostname, ip, record_type, ttl, unifi_record_id, target, drain_until, created_at, updated_at`

var tagRe = regexp.MustCompile(`^[A-Za-z0-9._-]{0,64}$`)

func validateTag(tag string) error {
	if !tagRe.MatchString(tag) {
		return fmt.Errorf("store: invalid instance tag %q: want at most 64 chars of [A-Za-z0-9._-]", tag)
	}
	return nil
}

// scanner is a common interface between *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// collect drains rows through scan and closes them.
func collect(rows *sql.Rows, scan func(scanner) (Record, error)) ([]Record, error) {
	defer rows.Close()
	var records []Record
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

func errNoRow(id string) error {
	return fmt.Errorf("store: update record: no row with id %q", id)
}
