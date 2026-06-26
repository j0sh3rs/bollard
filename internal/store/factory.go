package store

import (
	"strings"
)

// NewStore opens a Store backed by the appropriate database engine.
// DSNs starting with "postgres://" or "postgresql://" use Postgres.
// All other DSNs use SQLite.
func NewStore(dsn string) (Store, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return NewPostgres(dsn)
	}
	return NewSQLite(dsn)
}
