package repo

import (
	"database/sql"
	"encoding/json"
	"os"
	"testing"

	"agentflow/internal/domain"

	"github.com/go-sql-driver/mysql"
)

func TestMySQLSandboxMigrationAllocatesUnusedIDs(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_SANDBOX_MIGRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("requires an empty, disposable MySQL database")
	}
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MultiStatements = true
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()

	var tables int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if config.DBName == "" || tables != 0 {
		t.Fatal("requires an empty, disposable MySQL database")
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE sandboxes (
  id VARCHAR(64) PRIMARY KEY, name VARCHAR(255), payload JSON
) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE workflows LIKE sandboxes;
CREATE TABLE dockerfiles LIKE sandboxes;
SET @legacy_id = REPEAT('a', 64);
SET @hash_id = UPPER(CONCAT('dockerfile-', LEFT(SHA2(@legacy_id, 256), 53)));
INSERT INTO sandboxes (id, name, payload) VALUES
  (@legacy_id, 'Original', JSON_OBJECT('id', @legacy_id, 'driver', 'native')),
  (@hash_id, 'Prior fallback', JSON_OBJECT('id', @hash_id, 'driver', 'native')),
  ('DOCKERFILE-1', 'Occupied number', JSON_OBJECT('id', 'DOCKERFILE-1', 'driver', 'native'));
INSERT INTO dockerfiles (id, name, payload) VALUES
  (@legacy_id, 'Legacy', JSON_OBJECT('content', 'FROM alpine:3.20', 'projectId', 'project', 'shared', CAST('true' AS JSON))),
  ('dockerfile-2', 'Keep original ID', JSON_OBJECT('content', 'FROM alpine:3.21'));
`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/041_native_sandbox_inline_assets.sql")
	if err != nil {
		t.Fatal(err)
	}
	var migratedID string
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := db.ExecContext(ctx, string(migration)); err != nil {
			t.Fatalf("migration %d: %v", attempt, err)
		}
		var id string
		var payload []byte
		if err := db.QueryRowContext(ctx, "SELECT id, payload FROM sandboxes WHERE name = 'Legacy'").Scan(&id, &payload); err != nil {
			t.Fatal(err)
		}
		var migrated domain.SandboxProfile
		if err := json.Unmarshal(payload, &migrated); err != nil {
			t.Fatal(err)
		}
		if id == "" || id != migrated.ID || migrated.Driver != domain.SandboxDriverDocker || migrated.ProjectID != "project" ||
			!migrated.Shared || migrated.Dockerfile == nil || migrated.Dockerfile.Content != "FROM alpine:3.20" {
			t.Fatalf("migration %d: unexpected sandbox %q %s", attempt, id, payload)
		}
		if attempt == 2 && id != migratedID {
			t.Fatalf("rerun changed the allocated ID from %q to %q", migratedID, id)
		}
		migratedID = id
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sandboxes").Scan(&count); err != nil || count != 5 {
			t.Fatalf("migration %d: sandbox count %d, err=%v", attempt, count, err)
		}
	}
}
