package repo

import "testing"

// The Postgres port has to end up with a schema that is natively Postgres.
// The models still carry MySQL type names in their gorm tags, so the migrator
// has to translate them while it creates the columns; the smoke test below
// asserts the end state of the translated schema against a real Postgres
// database.
//
// The test is skipped when AGENTFLOW_TEST_POSTGRES_URL is unset. A skip is
// not a pass: the case only proves something when it ran.
//
// The generated columns live in a second case
// (TestPostgresSmokeGeneratedColumns) because they are not part of the
// AutoMigrate schema: only migrations/001_run_obs_generated_columns.pg.sql,
// applied by scripts/install-schema-extensions-pg.sh, creates them.

// TestPostgresSmoke asserts that the ported schema is Postgres-native: the
// tables exist, the model's json columns came out as jsonb, and no column
// kept a MySQL-only type.
func TestPostgresSmoke(t *testing.T) {
	store := OpenPostgresTest(t)

	if got := store.DB.Dialector.Name(); got != dialectPostgres {
		t.Fatalf("the dialect = %q, want %q", got, dialectPostgres)
	}

	var tables int64
	if err := store.DB.Raw("SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()").Scan(&tables).Error; err != nil {
		t.Fatalf("count the tables: %v", err)
	}
	if tables < 50 {
		t.Fatalf("the schema looks incomplete: %d tables", tables)
	}

	// Postgres has both json and jsonb; the port has to use jsonb, because
	// MySQL's json is a binary blob and Postgres' json keeps the text.
	var jsonColumns int64
	if err := store.DB.Raw("SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND data_type = 'json'").Scan(&jsonColumns).Error; err != nil {
		t.Fatalf("count the json columns: %v", err)
	}
	if jsonColumns != 0 {
		t.Fatalf("%d columns are still of the json type; the port has to use jsonb", jsonColumns)
	}

	// The rest of the MySQL-only types must not survive either: Postgres has
	// no blob and no text of that width.
	var mysqlTypes int64
	if err := store.DB.Raw(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND data_type IN ('json','mediumblob','longblob','mediumtext')`).Scan(&mysqlTypes).Error; err != nil {
		t.Fatalf("count the MySQL-only columns: %v", err)
	}
	if mysqlTypes != 0 {
		t.Fatalf("%d columns kept a MySQL-only type", mysqlTypes)
	}

	// Every column the models declare with type:json has to have come out of
	// AutoMigrate as jsonb. The columns are compared one by one: a leftover
	// table of an interrupted test run would otherwise turn a count into a
	// false failure.
	modelColumns, _ := modelJSONColumns(t, store.DB)
	checkModelJSONColumns(t, store.DB, modelColumns)
}

// TestPostgresSmokeGeneratedColumns asserts the deployment end state: the two
// observability generated columns exist and are generated.
//
// gorm's AutoMigrate never creates a generated column, so this case fails on
// a database that only went through step 2 of the preparation order (see
// docs/testing/postgres.md). scripts/install-schema-extensions-pg.sh adds
// them, together with the seed rows.
func TestPostgresSmokeGeneratedColumns(t *testing.T) {
	store := OpenPostgresTest(t)

	var columns []struct {
		TableName  string
		ColumnName string
	}
	if err := store.DB.Raw(`SELECT table_name, column_name, is_generated
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND is_generated = 'ALWAYS'`).Scan(&columns).Error; err != nil {
		t.Fatalf("read the generated columns: %v", err)
	}

	want := map[string]string{"run_telemetry": "adapter", "run_logs": "level"}
	if len(columns) != len(want) {
		t.Fatalf("the generated columns = %d, want %d: run_telemetry.adapter and run_logs.level have to come from migrations/001_run_obs_generated_columns.pg.sql",
			len(columns), len(want))
	}
	for _, column := range columns {
		if want[column.TableName] != column.ColumnName {
			t.Fatalf("the generated column %s.%s is not one of the observability columns", column.TableName, column.ColumnName)
		}
	}
}
