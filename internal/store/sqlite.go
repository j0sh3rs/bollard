package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type sqliteStore struct {
	db *sql.DB
}

// NewSQLite opens a SQLite database at the given DSN, runs migrations, and
// returns a Store implementation.
func NewSQLite(dsn string) (Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &sqliteStore{db: db}, nil
}

func runMigrations(db *sql.DB) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	driver, err := migratesqlite.WithInstance(db, &migratesqlite.Config{})
	if err != nil {
		return err
	}
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}

func (s *sqliteStore) Create(ctx context.Context, r Record) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO records (id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ContainerID, r.Hostname, r.IP, r.RecordType, r.TTL, r.UnifiRecordID,
		r.CreatedAt.UTC().Format(time.RFC3339),
		r.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("store: create record: %w", err)
	}
	return nil
}

func (s *sqliteStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM records WHERE id = ?`, id)
	return err
}

func (s *sqliteStore) DeleteByContainerID(ctx context.Context, containerID string) (*Record, error) {
	row := s.db.QueryRowContext(ctx,
		`DELETE FROM records WHERE container_id = ?
		 RETURNING id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at`,
		containerID,
	)
	r, err := scanRecord(row)
	if err != nil {
		return nil, fmt.Errorf("store: delete by container id: %w", err)
	}
	return r, nil
}

func (s *sqliteStore) GetByContainerID(ctx context.Context, containerID string) (*Record, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at
		 FROM records WHERE container_id = ?`, containerID)
	return scanRecord(row)
}

func (s *sqliteStore) ListAll(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at FROM records`)
	if err != nil {
		return nil, fmt.Errorf("store: list all: %w", err)
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *r)
	}
	return records, rows.Err()
}

func (s *sqliteStore) Close() error {
	return s.db.Close()
}

// scanner is a common interface between *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(s scanner) (*Record, error) {
	var r Record
	var createdStr, updatedStr string
	err := s.Scan(
		&r.ID, &r.ContainerID, &r.Hostname, &r.IP, &r.RecordType,
		&r.TTL, &r.UnifiRecordID, &createdStr, &updatedStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan record: %w", err)
	}
	r.CreatedAt, err = time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, fmt.Errorf("store: parse created_at: %w", err)
	}
	r.UpdatedAt, err = time.Parse(time.RFC3339, updatedStr)
	if err != nil {
		return nil, fmt.Errorf("store: parse updated_at: %w", err)
	}
	return &r, nil
}
