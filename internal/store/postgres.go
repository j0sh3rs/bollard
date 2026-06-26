package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations_pg/*.sql
var migrationsPgFS embed.FS

type postgresStore struct {
	db *sql.DB
}

// NewPostgres opens a Postgres database at the given DSN, runs migrations, and
// returns a Store implementation.
func NewPostgres(dsn string) (Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	if err := runPostgresMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate postgres: %w", err)
	}
	return &postgresStore{db: db}, nil
}

func runPostgresMigrations(db *sql.DB) error {
	src, err := iofs.New(migrationsPgFS, "migrations_pg")
	if err != nil {
		return err
	}
	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		return err
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx", driver)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}

func (s *postgresStore) Create(ctx context.Context, r Record) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO records (id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		r.ID, r.ContainerID, r.Hostname, r.IP, r.RecordType, r.TTL, r.UnifiRecordID,
		r.CreatedAt.UTC(),
		r.UpdatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("store: create record: %w", err)
	}
	return nil
}

func (s *postgresStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM records WHERE id = $1`, id)
	return err
}

func (s *postgresStore) DeleteByContainerID(ctx context.Context, containerID string) (*Record, error) {
	row := s.db.QueryRowContext(ctx,
		`DELETE FROM records WHERE container_id = $1
		 RETURNING id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at`,
		containerID,
	)
	r, err := scanPgRecord(row)
	if err != nil {
		return nil, fmt.Errorf("store: delete by container id: %w", err)
	}
	return r, nil
}

func (s *postgresStore) GetByContainerID(ctx context.Context, containerID string) (*Record, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at
		 FROM records WHERE container_id = $1`, containerID)
	return scanPgRecord(row)
}

func (s *postgresStore) ListAll(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at FROM records`)
	if err != nil {
		return nil, fmt.Errorf("store: list all: %w", err)
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		r, err := scanPgRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *r)
	}
	return records, rows.Err()
}

func (s *postgresStore) Close() error {
	return s.db.Close()
}

// scanPgRecord scans a Postgres row directly into time.Time fields (TIMESTAMPTZ
// round-trips cleanly without string intermediaries).
func scanPgRecord(s scanner) (*Record, error) {
	var r Record
	var createdAt, updatedAt time.Time
	err := s.Scan(
		&r.ID, &r.ContainerID, &r.Hostname, &r.IP, &r.RecordType,
		&r.TTL, &r.UnifiRecordID, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan record: %w", err)
	}
	r.CreatedAt = createdAt.UTC()
	r.UpdatedAt = updatedAt.UTC()
	return &r, nil
}
