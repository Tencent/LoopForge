package repo

import (
	"fmt"
	"net/url"
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

// postgresDSN normalizes a Postgres URL for the driver. Caller-supplied
// parameters are never overwritten, with two exceptions that would break the
// connection:
//
//   - pgxpool-only pool parameters (pool_max_conns, pool_min_conns, …) are
//     dropped. Plain pgx does not recognize them and forwards every unknown
//     setting to the server as a startup parameter, which Postgres rejects
//     with "unrecognized configuration parameter". The pool size is enforced
//     by database/sql instead (see Open).
//   - sslmode defaults to "disable" only for loopback hosts, where the local
//     pooler speaks plain TCP. A remote database keeps pgx's own default.
func postgresDSN(dsn string) string {
	trimmed := strings.TrimSpace(dsn)
	if !strings.Contains(trimmed, "sslmode=") && isLoopbackHost(trimmed) {
		trimmed += querySeparator(trimmed) + "sslmode=disable"
	}
	return stripPoolParams(trimmed)
}

// querySeparator returns the character that appends a parameter to a URL.
func querySeparator(dsn string) string {
	if strings.Contains(dsn, "?") {
		return "&"
	}
	return "?"
}

// isLoopbackHost reports whether the DSN points at the local machine.
func isLoopbackHost(dsn string) bool {
	parsed, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return false
	}
	switch parsed.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// stripPoolParams removes the pgxpool-only pool parameters from a DSN.
func stripPoolParams(dsn string) string {
	parsed, err := url.Parse(dsn)
	if err != nil {
		// An unparsable URL cannot connect anyway, but a caller-supplied
		// pool_* key must still not survive into the startup parameters.
		return stripPoolParamsTextually(dsn)
	}
	if parsed.RawQuery == "" {
		return dsn
	}
	values := parsed.Query()
	removed := false
	for key := range values {
		if strings.HasPrefix(strings.ToLower(key), "pool_") {
			values.Del(key)
			removed = true
		}
	}
	if !removed {
		return dsn
	}
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

// stripPoolParamsTextually drops pool_* parameters from a DSN whose URL
// cannot be parsed, by editing the raw query string.
func stripPoolParamsTextually(dsn string) string {
	base, query, found := strings.Cut(dsn, "?")
	if !found || query == "" {
		return dsn
	}
	parts := strings.Split(query, "&")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		key, _, _ := strings.Cut(part, "=")
		if strings.HasPrefix(strings.ToLower(key), "pool_") {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == len(parts) {
		return dsn
	}
	return base + "?" + strings.Join(kept, "&")
}

// openDB opens the gorm handle for the DSN's dialect.
func openDB(dsn string) (*gorm.DB, error) {
	config := &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)}
	if dsnDialect(dsn) == dialectPostgres {
		pg := postgres.Open(postgresDSN(dsn)).(*postgres.Dialector)
		db, err := gorm.Open(postgresDialector{Dialector: pg}, config)
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
