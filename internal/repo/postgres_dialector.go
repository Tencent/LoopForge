package repo

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// Postgres types for the MySQL-flavoured column tags the models use. `json`
// has no equality operator in Postgres (upserts and equality lookups break on
// it) and `mediumblob`/`longblob`/`mediumtext` do not exist there at all, so
// the DDL has to be written in Postgres types from the start.
var postgresTypeFixups = map[string]string{
	"json":       "jsonb",
	"mediumblob": "bytea",
	"longblob":   "bytea",
	"mediumtext": "text",
}

// postgresTypeFixup returns the Postgres type for a MySQL column type.
func postgresTypeFixup(mysqlType string) (string, bool) {
	pg, ok := postgresTypeFixups[strings.ToLower(strings.TrimSpace(mysqlType))]
	return pg, ok
}

// postgresDialector is the stock Postgres dialector with the models' MySQL
// column types translated on the way into DDL.
type postgresDialector struct {
	*postgres.Dialector
}

// DataTypeOf maps the MySQL type names used in the model tags onto their
// Postgres equivalents and leaves every other type to the stock driver.
func (d postgresDialector) DataTypeOf(field *schema.Field) string {
	if pg, ok := postgresTypeFixup(string(field.DataType)); ok {
		return pg
	}
	return d.Dialector.DataTypeOf(field)
}

// Migrator binds gorm's migrator to this dialector so AutoMigrate creates the
// columns with the translated types.
func (d postgresDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return postgresMigrator{postgres.Migrator{Migrator: migrator.Migrator{Config: migrator.Config{
		DB:                          db,
		Dialector:                   d,
		CreateIndexAfterCreateTable: true,
	}}}}
}

// postgresMigrator is the stock Postgres migrator running on the wrapped
// dialector.
//
// It restores one guarantee the stock driver relies on. The driver's GetRows
// forces pgx's simple protocol by asserting that the migrator's dialector is
// the stock postgres.Dialector *value* type. The wrapper is a different type,
// so that assertion fails and GetRows would silently fall back to pgx's
// default cached-statement mode: once a table gained or lost a column, the
// cached plan for "SELECT * FROM t LIMIT 1" no longer matches the table and
// Postgres rejects it with "cached plan must not change result type"
// (SQLSTATE 0A000). Mirroring GetRows keeps the stock behaviour.
type postgresMigrator struct {
	postgres.Migrator
}

// GetRows mirrors postgres.Migrator.GetRows. It decides the simple protocol
// from the wrapped dialector's config instead of the stock type assertion the
// wrapper breaks.
func (m postgresMigrator) GetRows(currentSchema interface{}, table interface{}) (*sql.Rows, error) {
	name := table.(string)
	if _, ok := currentSchema.(string); ok {
		name = fmt.Sprintf("%v.%v", currentSchema, table)
	}
	return m.DB.Session(&gorm.Session{}).Table(name).Limit(1).Scopes(func(d *gorm.DB) *gorm.DB {
		// use simple protocol
		if !m.DB.PrepareStmt && m.preferSimpleProtocol() {
			d.Statement.Vars = append([]interface{}{pgx.QueryExecModeSimpleProtocol}, d.Statement.Vars...)
		}
		return d
	}).Rows()
}

// preferSimpleProtocol reports whether pgx may serve the migrator's
// introspection queries with the simple protocol, which is what the stock
// driver does for its own introspection queries.
func (m postgresMigrator) preferSimpleProtocol() bool {
	dialector, ok := m.Dialector.(postgresDialector)
	if !ok || dialector.Dialector == nil {
		return false
	}
	cfg := dialector.Dialector.Config
	return cfg != nil && (cfg.DriverName == "" || cfg.DriverName == "pgx")
}
