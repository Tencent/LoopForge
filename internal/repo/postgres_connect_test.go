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

	wantJSONB := modelJSONColumnCount(t, store.DB)
	var jsonb int64
	if err := store.DB.Raw(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND data_type = 'jsonb'`).Scan(&jsonb).Error; err != nil {
		t.Fatalf("count jsonb columns: %v", err)
	}
	if jsonb != int64(wantJSONB) {
		t.Fatalf("jsonb columns = %d, want %d (the model's type:json columns)", jsonb, wantJSONB)
	}
}

// modelJSONColumnCount counts the columns the models declare with
// `type:json`. Every one of them has to come out of AutoMigrate as jsonb.
func modelJSONColumnCount(t *testing.T, db *gorm.DB) int {
	t.Helper()
	count := 0
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
				count++
			}
		}
	}
	return count
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
