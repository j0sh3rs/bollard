package store

import (
	"strings"
)

// NewStore opens a Store backed by the appropriate database engine.
// DSNs starting with "postgres://" or "postgresql://" use Postgres.
// All other DSNs use SQLite. The store only sees rows written with the same
// instanceTag.
func NewStore(dsn, instanceTag string) (Store, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return NewPostgres(dsn, instanceTag)
	}
	return NewSQLite(dsn, instanceTag)
}
