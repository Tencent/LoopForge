package repo

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"agentflow/internal/apperr"

	"github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestMissingSchemaExtensionsReportsAbsentColumns(t *testing.T) {
	present := map[string]bool{"run_telemetry.adapter": true}
	missing, err := missingSchemaExtensions(func(table, column string) (bool, error) {
		return present[table+"."+column], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0].Table != "run_logs" || missing[0].Column != "level" {
		t.Fatalf("missing = %+v, want only run_logs.level", missing)
	}
	hint := schemaExtensionsHint(missing)
	for _, want := range []string{"run_logs.level", "scripts/install-schema-extensions.sh", "migrations/001_run_obs_generated_columns.sql"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q does not mention %q", hint, want)
		}
	}
	if strings.Contains(hint, "run_telemetry.adapter") {
		t.Fatalf("hint %q names a column that exists", hint)
	}
}

func TestMissingSchemaExtensionsNamesMigrationOnce(t *testing.T) {
	missing, err := missingSchemaExtensions(func(string, string) (bool, error) { return false, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 2 {
		t.Fatalf("missing = %+v, want both columns", missing)
	}
	hint := schemaExtensionsHint(missing)
	if n := strings.Count(hint, "001_run_obs_generated_columns.sql"); n != 1 {
		t.Fatalf("hint %q names the migration %d times, want once", hint, n)
	}
}

func TestMissingSchemaExtensionsStopsOnLookupError(t *testing.T) {
	boom := errors.New("information_schema unavailable")
	missing, err := missingSchemaExtensions(func(string, string) (bool, error) { return false, boom })
	if !errors.Is(err, boom) || missing != nil {
		t.Fatalf("missing=%+v err=%v, want lookup error and no partial result", missing, err)
	}
}

func TestSchemaExtensionsWarningSkipsNonMySQL(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	warning, err := schemaExtensionsWarning(s.deps)
	if err != nil || warning != "" {
		t.Fatalf("sqlite warning=%q err=%v, want none", warning, err)
	}
}

func TestAsSchemaExtensionErrorExplainsMissingGeneratedColumn(t *testing.T) {
	raw := fmt.Errorf("scan: %w", &mysql.MySQLError{Number: 1054, Message: "Unknown column 'adapter' in 'where clause'"})
	got := asSchemaExtensionError(raw)
	ae, ok := apperr.As(got)
	if !ok || ae.Kind != apperr.Unavailable {
		t.Fatalf("got %v, want an Unavailable apperr", got)
	}
	if strings.Contains(ae.Message, "Error 1054") || !strings.Contains(ae.Message, "run_telemetry.adapter") || !strings.Contains(ae.Message, "install-schema-extensions.sh") {
		t.Fatalf("message %q should name the column and the installer, not the driver error", ae.Message)
	}
}

func TestAsSchemaExtensionErrorLeavesOtherErrorsAlone(t *testing.T) {
	other := []error{
		nil,
		errors.New("driver: bad connection"),
		&mysql.MySQLError{Number: 1054, Message: "Unknown column 'nickname' in 'field list'"},
		&mysql.MySQLError{Number: 1146, Message: "Table 'loopforge.run_logs' doesn't exist"},
	}
	for _, err := range other {
		if got := asSchemaExtensionError(err); got != err {
			t.Fatalf("asSchemaExtensionError(%v) = %v, want it unchanged", err, got)
		}
	}
}

// Read-only check of the information_schema lookup against a real MySQL.
func TestMySQLSchemaExtensionsLookupReadOnly(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open MySQL failed (%T); DSN omitted", err)
	}
	conn, _ := db.DB()
	defer conn.Close()
	has := mysqlColumnExists(db)
	if ok, err := has("run_telemetry", "no_such_column_for_schema_check"); err != nil || ok {
		t.Fatalf("absent column reported ok=%v err=%v", ok, err)
	}
	if _, err := schemaExtensionsWarning(&deps{DB: db}); err != nil {
		t.Fatalf("schema extension lookup failed: %v", err)
	}
}
