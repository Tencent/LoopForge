package repo

import (
	"errors"
	"os"
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestMySQLTelemetryMissingAdapterGuidance checks the query error and the shipped SQL repair.
func TestMySQLTelemetryMissingAdapterGuidance(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{TranslateError: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	// Temporary tables isolate this test from persistent application tables.
	for _, statement := range []string{
		"CREATE TEMPORARY TABLE runs (id VARCHAR(255) PRIMARY KEY, telemetry_sequence BIGINT)",
		"CREATE TEMPORARY TABLE run_telemetry (run_id VARCHAR(255), seq BIGINT, payload JSON)",
		"CREATE TEMPORARY TABLE run_logs (run_id VARCHAR(255), seq BIGINT, payload JSON)",
		"INSERT INTO runs VALUES ('schema-test', 1)",
		`INSERT INTO run_telemetry VALUES ('schema-test', 1, '{"sequence":1,"message":"live event","source":{"adapter":"anthropic-stream-v1"}}')`,
		`INSERT INTO run_logs VALUES ('schema-test', 1, '{"level":"warn"}')`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := newStore(db)
	t.Run("missing adapter", func(t *testing.T) {
		page, err := s.QueryRunTelemetry("schema-test", "", "all", 0, 0, false, false, 100)
		if err != nil || len(page.Events) != 1 || page.Events[0].Message != "live event" {
			t.Fatalf("unfiltered telemetry: page=%+v err=%v", page, err)
		}
		_, err = s.QueryRunTelemetry("schema-test", "", "live", 0, 0, false, false, 100)
		var driverErr *mysqldriver.MySQLError
		if !errors.As(err, &driverErr) || driverErr.Number != 1054 {
			t.Fatalf("expected the underlying missing-column error, got %v", err)
		}
		for _, hint := range []string{"run_telemetry.adapter", "migrations/001_run_obs_generated_columns.sql", "existing columns"} {
			if !strings.Contains(err.Error(), hint) {
				t.Errorf("missing guidance %q: %v", hint, err)
			}
		}
		if err := db.Exec("SELECT adapter FROM run_telemetry LIMIT 0").Error; err == nil {
			t.Error("failed telemetry query created the missing column")
		}
	})
	t.Run("prepared schema", func(t *testing.T) {
		script, err := os.ReadFile("../../migrations/001_run_obs_generated_columns.sql")
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range strings.Split(string(script), ";") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if err := db.Exec(statement).Error; err != nil {
				t.Fatal(err)
			}
		}
		page, err := s.QueryRunTelemetry("schema-test", "", "live", 0, 0, false, false, 100)
		if err != nil || len(page.Events) != 1 || page.Events[0].Message != "live event" {
			t.Fatalf("prepared telemetry: page=%+v err=%v", page, err)
		}
		var level string
		if err := db.Raw("SELECT level FROM run_logs").Scan(&level).Error; err != nil || level != "warn" {
			t.Fatalf("generated log level=%q err=%v", level, err)
		}
	})
	t.Run("unrelated missing column", func(t *testing.T) {
		if err := db.Exec("ALTER TABLE run_telemetry RENAME COLUMN seq TO event_seq").Error; err != nil {
			t.Fatal(err)
		}
		_, err := s.QueryRunTelemetry("schema-test", "", "live", 0, 0, false, false, 100)
		var driverErr *mysqldriver.MySQLError
		if !errors.As(err, &driverErr) || driverErr.Number != 1054 || !strings.Contains(driverErr.Message, "'seq'") {
			t.Fatalf("expected the unrelated missing-column error, got %v", err)
		}
		if strings.Contains(err.Error(), "001_run_obs_generated_columns.sql") {
			t.Fatalf("unrelated error has schema-extension guidance: %v", err)
		}
	})
}
