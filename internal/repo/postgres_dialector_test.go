package repo

import (
	"reflect"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

func TestPostgresTypeFixup(t *testing.T) {
	cases := []struct {
		mysqlType string
		pgType    string
	}{
		{"json", "jsonb"},
		{"mediumblob", "bytea"},
		{"longblob", "bytea"},
		{"mediumtext", "text"},
	}
	for _, tc := range cases {
		got, ok := postgresTypeFixup(tc.mysqlType)
		if !ok || got != tc.pgType {
			t.Fatalf("postgresTypeFixup(%q) = %q,%v; want %q,true", tc.mysqlType, got, ok, tc.pgType)
		}
	}
	if _, ok := postgresTypeFixup("text"); ok {
		t.Fatal("postgresTypeFixup(text) must not be remapped")
	}
}

func TestPostgresDialectorDataTypeOf(t *testing.T) {
	d := postgresDialector{Dialector: &postgres.Dialector{Config: &postgres.Config{}}}
	cases := map[string]string{
		"json":       "jsonb",
		"mediumblob": "bytea",
		"longblob":   "bytea",
		"mediumtext": "text",
	}
	for mysqlType, want := range cases {
		field := &schema.Field{DataType: schema.DataType(mysqlType)}
		if got := d.DataTypeOf(field); want != got {
			t.Fatalf("DataTypeOf(%q) = %q; want %q", mysqlType, got, want)
		}
	}
}

// TestPostgresMigratorDataTypeOf proves the DDL path sees the translated
// types: gorm's migrator resolves column types through the dialector stored
// in its config, so the wrapper has to be the one bound there.
func TestPostgresMigratorDataTypeOf(t *testing.T) {
	d := postgresDialector{Dialector: &postgres.Dialector{Config: &postgres.Config{}}}
	m := postgresMigrator{postgres.Migrator{Migrator: migrator.Migrator{Config: migrator.Config{
		Dialector: d,
	}}}}
	field := &schema.Field{DataType: schema.DataType("json"), IndirectFieldType: reflect.TypeOf("")}
	if got := m.DataTypeOf(field); got != "jsonb" {
		t.Fatalf("migrator DataTypeOf(json) = %q; want jsonb", got)
	}
}

// TestPostgresMigratorKeepsSimpleProtocol pins the behaviour the stock
// Postgres driver relies on: its GetRows forces pgx's simple protocol, and
// without it a cached statement plan breaks ("cached plan must not change
// result type") as soon as a table gains a column.
func TestPostgresMigratorKeepsSimpleProtocol(t *testing.T) {
	d := postgresDialector{Dialector: &postgres.Dialector{Config: &postgres.Config{}}}
	m := postgresMigrator{postgres.Migrator{Migrator: migrator.Migrator{Config: migrator.Config{
		Dialector: d,
	}}}}
	if !m.preferSimpleProtocol() {
		t.Fatal("the migrator must keep the stock driver's simple protocol for GetRows")
	}
	// The wrapper deliberately breaks the stock type assertion; GetRows
	// compensates by reading the config instead.
	if _, ok := m.Dialector.(postgres.Dialector); ok {
		t.Fatal("the wrapper is expected to break the stock postgres.Dialector type assertion")
	}
}
