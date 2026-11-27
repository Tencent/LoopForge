package repo

import (
	"strings"

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
	return postgresMigrator{
		Migrator: postgres.Migrator{Migrator: migrator.Migrator{Config: migrator.Config{
			DB:                          db,
			Dialector:                   d,
			CreateIndexAfterCreateTable: true,
		}}},
		stock: *d.Dialector,
	}
}

// postgresMigrator is the stock Postgres migrator running on the wrapped
// dialector.
//
// The stock driver's GetRows decides whether to use pgx's simple protocol by
// asserting that the migrator's dialector is the stock postgres.Dialector
// *value* type. The wrapper is a different type, so that assertion fails and
// GetRows falls back to pgx's cached-statement mode: a table that gained or
// lost a column invalidates the cached plan of "SELECT * FROM t LIMIT 1" and
// Postgres rejects it with "cached plan must not change result type"
// (SQLSTATE 0A000).
//
// GetRows is not part of the gorm.Migrator interface and its only caller is
// the stock ColumnTypes, whose receiver type is postgres.Migrator — Go binds
// that call statically, so an override on this outer type would never run.
// The fix therefore overrides ColumnTypes itself (depth 0) and hands the
// stock implementation a stock dialector for the duration of the call.
type postgresMigrator struct {
	postgres.Migrator
	// stock is the unwrapped dialector the driver recognises.
	stock postgres.Dialector
}

// ColumnTypes runs the stock introspection with the stock dialector.
//
// The stock ColumnTypes only reads information_schema / pg_catalog and never
// asks the dialector for a column type, so swapping the dialector back for
// the duration of the call affects nothing but the GetRows simple-protocol
// decision. Re-check this assumption when gorm.io/driver/postgres is
// upgraded.
func (m postgresMigrator) ColumnTypes(value interface{}) ([]gorm.ColumnType, error) {
	inner := m.Migrator
	inner.Migrator.Config.Dialector = m.stock
	return inner.ColumnTypes(value)
}
