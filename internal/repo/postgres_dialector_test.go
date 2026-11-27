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
	m := postgresMigrator{
		Migrator: postgres.Migrator{Migrator: migrator.Migrator{Config: migrator.Config{
			Dialector: d,
		}}},
		stock: *d.Dialector,
	}
	field := &schema.Field{DataType: schema.DataType("json"), IndirectFieldType: reflect.TypeOf("")}
	if got := m.DataTypeOf(field); got != "jsonb" {
		t.Fatalf("migrator DataTypeOf(json) = %q; want jsonb", got)
	}
}

// TestPostgresMigratorKeepsStockDialector checks the wiring the driver
// depends on: the DDL path must keep the wrapped dialector (so column types
// are translated) while the migrator also carries the unwrapped one (so the
// driver's own type assertion in GetRows keeps working).
func TestPostgresMigratorKeepsStockDialector(t *testing.T) {
	stock := &postgres.Dialector{Config: &postgres.Config{}}
	d := postgresDialector{Dialector: stock}
	m, ok := d.Migrator(nil).(postgresMigrator)
	if !ok {
		t.Fatal("the postgres dialector must hand out a postgresMigrator")
	}
	if _, wrapped := m.Migrator.Config.Dialector.(postgresDialector); !wrapped {
		t.Fatal("the DDL path must keep the wrapped dialector")
	}
	if m.stock.Config != stock.Config {
		t.Fatal("the migrator must carry the stock dialector for the driver's type assertion")
	}
}
