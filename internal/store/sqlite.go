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
	db  *sql.DB
	tag string
}

// NewSQLite opens a SQLite database at the given DSN, runs migrations, and
// returns a Store implementation.
func NewSQLite(dsn, instanceTag string) (Store, error) {
	if err := validateTag(instanceTag); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &sqliteStore{db: db, tag: instanceTag}, nil
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

func timeStr(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (s *sqliteStore) Create(ctx context.Context, r Record) error {
	var drain any
	if !r.DrainUntil.IsZero() {
		drain = timeStr(r.DrainUntil)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO records (`+recordCols+`, instance_tag)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ContainerID, r.Hostname, r.IP, r.RecordType, r.TTL, r.UnifiRecordID, r.Target, drain,
		timeStr(r.CreatedAt), timeStr(r.UpdatedAt), s.tag,
	)
	if err != nil {
		return fmt.Errorf("store: create record: %w", err)
	}
	return nil
}

func (s *sqliteStore) Update(ctx context.Context, r Record) error {
	var drain any
	if !r.DrainUntil.IsZero() {
		drain = timeStr(r.DrainUntil)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE records SET container_id = ?, ip = ?, ttl = ?, unifi_record_id = ?, target = ?, drain_until = ?, updated_at = ?
		 WHERE id = ? AND instance_tag = ?`,
		r.ContainerID, r.IP, r.TTL, r.UnifiRecordID, r.Target, drain, timeStr(r.UpdatedAt), r.ID, s.tag,
	)
	if err != nil {
		return fmt.Errorf("store: update record: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: update record: %w", err)
	} else if n == 0 {
		return errNoRow(r.ID)
	}
	return nil
}

func (s *sqliteStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM records WHERE id = ? AND instance_tag = ?`, id, s.tag)
	return err
}

func (s *sqliteStore) ListByContainerID(ctx context.Context, containerID string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+recordCols+` FROM records WHERE container_id = ? AND instance_tag = ? ORDER BY hostname`,
		containerID, s.tag)
	if err != nil {
		return nil, fmt.Errorf("store: list by container id: %w", err)
	}
	return collect(rows, scanRecord)
}

func (s *sqliteStore) DeleteByContainerID(ctx context.Context, containerID string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`DELETE FROM records WHERE container_id = ? AND instance_tag = ? RETURNING `+recordCols,
		containerID, s.tag)
	if err != nil {
		return nil, fmt.Errorf("store: delete by container id: %w", err)
	}
	return collect(rows, scanRecord)
}

func (s *sqliteStore) ListAll(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+recordCols+` FROM records WHERE instance_tag = ? ORDER BY hostname, container_id`, s.tag)
	if err != nil {
		return nil, fmt.Errorf("store: list all: %w", err)
	}
	return collect(rows, scanRecord)
}

func (s *sqliteStore) Close() error {
	return s.db.Close()
}

func scanRecord(s scanner) (Record, error) {
	var r Record
	var drain sql.NullString
	var createdStr, updatedStr string
	err := s.Scan(
		&r.ID, &r.ContainerID, &r.Hostname, &r.IP, &r.RecordType,
		&r.TTL, &r.UnifiRecordID, &r.Target, &drain, &createdStr, &updatedStr,
	)
	if err != nil {
		return Record{}, fmt.Errorf("store: scan record: %w", err)
	}
	if drain.Valid {
		if r.DrainUntil, err = time.Parse(time.RFC3339, drain.String); err != nil {
			return Record{}, fmt.Errorf("store: parse drain_until: %w", err)
		}
	}
	if r.CreatedAt, err = time.Parse(time.RFC3339, createdStr); err != nil {
		return Record{}, fmt.Errorf("store: parse created_at: %w", err)
	}
	if r.UpdatedAt, err = time.Parse(time.RFC3339, updatedStr); err != nil {
		return Record{}, fmt.Errorf("store: parse updated_at: %w", err)
	}
	return r, nil
}
