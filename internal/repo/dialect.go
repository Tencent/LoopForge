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

// jsonPathToPGArray converts a MySQL JSON path ("$.a.b", "$.messages[0]") into
// the Postgres path array form ("{a,b}", "{messages,0}").
func jsonPathToPGArray(path string) string {
	trimmed := strings.TrimSpace(path)
	trimmed = strings.TrimPrefix(trimmed, "$")
	trimmed = strings.TrimPrefix(trimmed, ".")
	var parts []string
	for _, segment := range strings.Split(trimmed, ".") {
		for {
			open := strings.Index(segment, "[")
			if open < 0 {
				break
			}
			closeIdx := strings.Index(segment, "]")
			if closeIdx < open {
				break
			}
			parts = append(parts, segment[:open])
			parts = append(parts, segment[open+1:closeIdx])
			segment = segment[closeIdx+1:]
		}
		if segment != "" {
			parts = append(parts, segment)
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// jsonTextLiteral extracts a text value at a JSON path. The path is a code
// constant, never user input.
func (d *deps) jsonTextLiteral(column, path string) string {
	if d.isPostgres() {
		return column + "::jsonb #>> '" + jsonPathToPGArray(path) + "'"
	}
	if d.isMySQL() {
		return "JSON_UNQUOTE(JSON_EXTRACT(" + column + ", '" + path + "'))"
	}
	return "json_extract(" + column + ", '" + path + "')"
}

// jsonTextParam extracts a text value at a path bound as a parameter. The
// bound parameter keeps the MySQL path form ("$.runId"); the Postgres branch
// converts it to a path array in SQL, so the call sites do not change.
func (d *deps) jsonTextParam(column string) string {
	if d.isPostgres() {
		return column + "::jsonb #>> string_to_array(ltrim(?, '$.'), '.')"
	}
	if d.isMySQL() {
		return "JSON_UNQUOTE(JSON_EXTRACT(" + column + ", ?))"
	}
	return "json_extract(" + column + ", ?)"
}

// jsonLengthLiteral counts the elements of the JSON array at a path. A
// missing or non-array value counts as zero.
func (d *deps) jsonLengthLiteral(column, path string) string {
	if d.isPostgres() {
		inner := column + "::jsonb #> '" + jsonPathToPGArray(path) + "'"
		return "COALESCE(CASE WHEN jsonb_typeof(" + inner + ") = 'array' THEN jsonb_array_length(" + inner + ") END, 0)"
	}
	if d.isMySQL() {
		return "JSON_LENGTH(JSON_EXTRACT(" + column + ", '" + path + "'))"
	}
	return "json_array_length(" + column + ", '" + path + "')"
}

// jsonTypeLiteral names the JSON type at a path ("STRING", "OBJECT", ...).
func (d *deps) jsonTypeLiteral(column, path string) string {
	if d.isPostgres() {
		inner := column + "::jsonb #> '" + jsonPathToPGArray(path) + "'"
		return "CASE WHEN jsonb_typeof(" + inner + ") IS NULL THEN NULL WHEN jsonb_typeof(" + inner + ") = 'string' THEN 'STRING' WHEN jsonb_typeof(" + inner + ") = 'number' THEN 'INTEGER' WHEN jsonb_typeof(" + inner + ") = 'boolean' THEN 'BOOLEAN' WHEN jsonb_typeof(" + inner + ") = 'object' THEN 'OBJECT' WHEN jsonb_typeof(" + inner + ") = 'array' THEN 'ARRAY' ELSE 'NULL' END"
	}
	if d.isMySQL() {
		return "JSON_TYPE(JSON_EXTRACT(" + column + ", '" + path + "'))"
	}
	return "json_type(" + column + ", '" + path + "')"
}

// jsonBuildObject builds a JSON object from alternating key/value arguments.
func (d *deps) jsonBuildObject(pairs ...string) string {
	if d.isPostgres() {
		return "jsonb_build_object(" + strings.Join(pairs, ", ") + ")"
	}
	if d.isMySQL() {
		return "JSON_OBJECT(" + strings.Join(pairs, ", ") + ")"
	}
	return "json_object(" + strings.Join(pairs, ", ") + ")"
}

// likeOperator returns the pattern-matching operator. MySQL's default
// collation matches case-insensitively, so Postgres uses ILIKE to keep the
// same search behaviour.
func (d *deps) likeOperator() string {
	if d.isPostgres() {
		return "ILIKE"
	}
	return "LIKE"
}

// nowMillisLiteral returns the current timestamp in milliseconds.
func (d *deps) nowMillisLiteral() string {
	if d.isPostgres() {
		return "now()"
	}
	return "UTC_TIMESTAMP(3)"
}
