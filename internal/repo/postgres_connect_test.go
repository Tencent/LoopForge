package repo

import (
	"os"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// TestPostgresConnectOpens is the smoke gate for the Postgres connection
// path: it opens a real database through the same code the platform uses and
// checks the dialect, the connection pool cap and the schema AutoMigrate
// produced. It is skipped when AGENTFLOW_TEST_POSTGRES_URL is unset.
func TestPostgresConnectOpens(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_POSTGRES_URL is not set")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if got := store.DB.Dialector.Name(); got != dialectPostgres {
		t.Fatalf("expected the postgres dialect, got %q", got)
	}
	sqlDB, err := store.DB.DB()
	if err != nil {
		t.Fatalf("sql handle: %v", err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 10 {
		t.Fatalf("connection pool cap = %d, want 10", got)
	}
	var tables int64
	if err := store.DB.Raw("SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()").Scan(&tables).Error; err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if tables == 0 {
		t.Fatal("AutoMigrate created no tables")
	}

	// The column types matter as much as the tables: the models still carry
	// MySQL type names in their tags, and AutoMigrate has to translate them
	// into Postgres types while creating the columns.
	var mysqlTyped int64
	if err := store.DB.Raw(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND data_type IN ('json', 'mediumblob', 'longblob', 'mediumtext')`).Scan(&mysqlTyped).Error; err != nil {
		t.Fatalf("count MySQL-typed columns: %v", err)
	}
	if mysqlTyped != 0 {
		t.Fatalf("%d columns kept a MySQL type name; Postgres has no such types", mysqlTyped)
	}

	modelColumns := modelJSONColumns(t, store.DB)
	checkModelJSONColumns(t, store.DB, modelColumns)
}

// modelJSONColumn is a column the models declare with `type:json`.
type modelJSONColumn struct {
	Table  string
	Column string
}

// modelJSONColumns returns the columns the models declare with `type:json`.
// Every returned column has to come out of AutoMigrate as jsonb.
func modelJSONColumns(t *testing.T, db *gorm.DB) []modelJSONColumn {
	t.Helper()
	columns := make([]modelJSONColumn, 0, len(allModels()))
	for _, model := range allModels() {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			t.Fatalf("parse schema %T: %v", model, err)
		}
		for _, field := range stmt.Schema.Fields {
			if field.DBName == "" {
				continue
			}
			if field.TagSettings["TYPE"] == "json" {
				columns = append(columns, modelJSONColumn{Table: stmt.Schema.Table, Column: field.DBName})
			}
		}
	}
	return columns
}

// checkModelJSONColumns asserts that every column the models declare with
// `type:json` came out of AutoMigrate as jsonb.
//
// The check compares the columns one by one. Counting the jsonb columns of
// the schema and comparing that count with the number of json columns the
// models declare compares two independently derived numbers: a leftover table
// — the probe table of TestPostgresColumnTypesSurvivesSchemaChange survives
// a killed test run — or a column the models do not own would make the
// comparison fail even though the port is correct.
//
// The query reads the column types of the model's tables in one round trip
// and the comparison happens in Go, so a column that is missing is reported
// next to a column of the wrong type.
func checkModelJSONColumns(t *testing.T, db *gorm.DB, columns []modelJSONColumn) {
	t.Helper()
	tables := make([]string, 0, len(columns))
	seen := map[string]bool{}
	for _, column := range columns {
		if seen[column.Table] {
			continue
		}
		seen[column.Table] = true
		tables = append(tables, column.Table)
	}
	var rows []struct {
		TableName  string
		ColumnName string
		DataType   string
	}
	if err := db.Raw(`SELECT table_name, column_name, data_type
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name IN ?`, tables).Scan(&rows).Error; err != nil {
		t.Fatalf("read the column types: %v", err)
	}
	types := make(map[string]string, len(rows))
	for _, row := range rows {
		types[row.TableName+"."+row.ColumnName] = row.DataType
	}
	var wrong []string
	for _, column := range columns {
		name := column.Table + "." + column.Column
		dataType, ok := types[name]
		switch {
		case !ok:
			wrong = append(wrong, name+" (missing)")
		case dataType != "jsonb":
			wrong = append(wrong, name+" ("+dataType+")")
		}
	}
	if len(wrong) > 0 {
		t.Fatalf("the json columns that are not jsonb = %v", wrong)
	}
}

// TestPostgresColumnTypesSurvivesSchemaChange is the behavioural guard for
// the wrapped dialector's migrator.
//
// The Postgres driver decides whether to use pgx's simple protocol by
// asserting that the migrator's dialector is the stock postgres.Dialector
// value type. The wrapper breaks that assertion, and the driver then caches
// the statement plan of its introspection query — so a table that changed
// shape in between makes Postgres reject the cached plan with "cached plan
// must not change result type" (SQLSTATE 0A000).
//
// The pool is pinned to a single backend so the cached plan provably lives
// on the connection that later sees the ALTER.
func TestPostgresColumnTypesSurvivesSchemaChange(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_POSTGRES_URL is not set")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	sqlDB, err := store.DB.DB()
	if err != nil {
		t.Fatalf("sql handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	const probe = "zz_tmp_column_types_probe"
	if err := store.DB.Exec("DROP TABLE IF EXISTS " + probe).Error; err != nil {
		t.Fatalf("drop probe table: %v", err)
	}
	t.Cleanup(func() { store.DB.Exec("DROP TABLE IF EXISTS " + probe) })
	if err := store.DB.Exec("CREATE TABLE " + probe + " (id text primary key, payload jsonb)").Error; err != nil {
		t.Fatalf("create probe table: %v", err)
	}

	migrator := store.DB.Migrator()
	if _, err := migrator.ColumnTypes(probe); err != nil {
		t.Fatalf("prime ColumnTypes: %v", err)
	}
	if err := store.DB.Exec("ALTER TABLE " + probe + " ADD COLUMN extra text").Error; err != nil {
		t.Fatalf("alter probe table: %v", err)
	}
	if _, err := migrator.ColumnTypes(probe); err != nil {
		t.Fatalf("ColumnTypes after the table changed: %v", err)
	}
}
