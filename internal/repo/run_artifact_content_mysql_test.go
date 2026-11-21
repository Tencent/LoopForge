package repo

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// The engine accepts one artifact body of up to 1 MiB
// (internal/engine/artifacts.go maxArtifactBytes) and the runner uploads it
// byte for byte, so run_artifacts.content has to hold more than TEXT's 65,535
// bytes. A 74 KB design document used to fail the whole run write with MySQL
// 1406 and left the node reading "running" forever.
//
// The DSN must point to a disposable local MySQL database with
// multiStatements=true: the test recreates the legacy TEXT column and replays
// migrations/040 twice to cover both the widening and its idempotence.
func TestRunArtifactContentMySQLMigration(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_RUN_ARTIFACT_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable local MySQL only")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.DB.Exec("ALTER TABLE run_artifacts MODIFY content TEXT").Error; err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", 74_768)
	artifact := runArtifactRow{
		ID: "artifact-content-tester", RunID: "run-artifact-content", NodeID: "design",
		Name: "design", Path: "design.md", MediaType: "text/markdown",
		SizeBytes: int64(len(body)), Content: body,
	}
	err = store.DB.Create(&artifact).Error
	var sqlErr *mysql.MySQLError
	if !errors.As(err, &sqlErr) || sqlErr.Number != 1406 {
		t.Fatalf("legacy TEXT column did not reproduce the failure: %v", err)
	}
	script, err := os.ReadFile("../../migrations/040_run_artifacts_content_mediumtext.sql")
	if err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		if err := store.DB.Exec(string(script)).Error; err != nil {
			t.Fatalf("migration run %d: %v", run, err)
		}
	}
	if err := store.DB.Create(&artifact).Error; err != nil {
		t.Fatal(err)
	}
	defer store.DB.Delete(&artifact)
	var stored string
	if err := store.DB.Model(&runArtifactRow{}).Where("id = ?", artifact.ID).Pluck("content", &stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored != body {
		t.Fatalf("stored content = %d bytes, want %d", len(stored), len(body))
	}
}
