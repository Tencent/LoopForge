package repo

import (
	"fmt"
	"strings"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The store speaks three dialects: MySQL (upstream default), Postgres (this
// deployment) and sqlite (tests only). Every fragment that differs between
// them lives in this file so call sites stay readable.

const (
	dialectMySQL    = "mysql"
	dialectPostgres = "postgres"
	dialectSQLite   = "sqlite"
)

// dsnDialect classifies a DSN by its scheme. Postgres URLs start with
// postgres:// or postgresql://; everything else is the MySQL DSN form
// (user:password@tcp(host:port)/database).
func dsnDialect(dsn string) string {
	lowered := strings.ToLower(strings.TrimSpace(dsn))
	if strings.HasPrefix(lowered, "postgres://") || strings.HasPrefix(lowered, "postgresql://") {
		return dialectPostgres
	}
	return dialectMySQL
}

// postgresDSN normalizes a Postgres URL: the local Supavisor pooler speaks
// plain TCP without TLS, and every pooled connection owns one backend, so the
// pool stays small. Caller-supplied parameters are never overwritten.
func postgresDSN(dsn string) string {
	trimmed := strings.TrimSpace(dsn)
	separator := "?"
	if strings.Contains(trimmed, "?") {
		separator = "&"
	}
	if !strings.Contains(trimmed, "sslmode=") {
		trimmed += separator + "sslmode=disable"
		separator = "&"
	}
	if !strings.Contains(trimmed, "pool_max_conns=") {
		trimmed += separator + "pool_max_conns=10"
	}
	return trimmed
}

// openDB opens the gorm handle for the DSN's dialect.
func openDB(dsn string) (*gorm.DB, error) {
	config := &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)}
	if dsnDialect(dsn) == dialectPostgres {
		db, err := gorm.Open(postgres.Open(postgresDSN(dsn)), config)
		if err != nil {
			return nil, fmt.Errorf("postgres open: %w", err)
		}
		return db, nil
	}
	db, err := gorm.Open(mysql.Open(dsn), config)
	if err != nil {
		return nil, fmt.Errorf("mysql open: %w", err)
	}
	return db, nil
}

// isPostgres reports whether the handle talks to Postgres.
func (d *deps) isPostgres() bool {
	return d.DB != nil && d.DB.Dialector.Name() == dialectPostgres
}
