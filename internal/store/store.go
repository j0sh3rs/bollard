package store

import (
	"context"
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
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Store is the persistence interface for DNS records.
type Store interface {
	Create(ctx context.Context, r Record) error
	Delete(ctx context.Context, id string) error
	DeleteByContainerID(ctx context.Context, containerID string) (*Record, error)
	GetByContainerID(ctx context.Context, containerID string) (*Record, error)
	ListAll(ctx context.Context) ([]Record, error)
	Close() error
}
