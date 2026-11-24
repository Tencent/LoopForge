package repo

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"agentflow/internal/domain"

	"github.com/go-sql-driver/mysql"
)

func TestMySQLSkillBodyMigrationPreflight(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_SKILL_MIGRATION_TEST_DSN")
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
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var tables int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if config.DBName == "" || tables != 0 {
		t.Fatal("requires an empty, disposable MySQL database")
	}
	if _, err := conn.ExecContext(ctx, `
CREATE TABLE skills (
  id VARCHAR(64) PRIMARY KEY, payload JSON NOT NULL, updated_at DATETIME(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE skill_files (
  skill_id VARCHAR(64) NOT NULL, path VARCHAR(512) NOT NULL, content MEDIUMBLOB,
  size BIGINT, sha256 CHAR(64), unix_mode INT, updated_at DATETIME(3), PRIMARY KEY (skill_id, path)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
INSERT INTO skills (id, payload) VALUES
  ('valid', JSON_OBJECT('content', '# Valid')),
  ('case-collision', JSON_OBJECT('content', '# Legacy')),
  ('oversized', JSON_OBJECT('content', REPEAT('x', 1048577))),
  ('uploaded', JSON_OBJECT('content', REPEAT('y', 1048577)));
INSERT INTO skill_files (skill_id, path, content, size, sha256, unix_mode) VALUES
  ('case-collision', 'Skill.MD', 'Keep this file', 14, SHA2('Keep this file', 256), 420),
  ('uploaded', 'SKILL.md', 'Keep the upload', 15, SHA2('Keep the upload', 256), 420);
`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/042_skill_bodies_to_files.sql")
	if err != nil {
		t.Fatal(err)
	}

	_, err = conn.ExecContext(ctx, string(migration))
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 || !strings.Contains(mysqlErr.Message, "Migration 042 blocked by preflight") {
		t.Fatalf("expected preflight rejection, got %v", err)
	}
	var reasons string
	if err := conn.QueryRowContext(ctx, "SELECT GROUP_CONCAT(CONCAT(skill_id, ':', reason) ORDER BY skill_id) FROM skill_body_migration_blockers").Scan(&reasons); err != nil {
		t.Fatal(err)
	}
	if reasons != "case-collision:skill_md_path_collision,oversized:body_exceeds_1_mib" {
		t.Fatalf("unexpected blockers %q", reasons)
	}

	// Exercise the remaining statements as a client configured to continue
	// after the preflight error would.
	_, writes, found := strings.Cut(string(migration), "START TRANSACTION;")
	if !found {
		t.Fatal("migration transaction missing")
	}
	if _, err := conn.ExecContext(ctx, "START TRANSACTION;"+writes); err != nil {
		t.Fatal(err)
	}
	var legacyBodies, files int
	err = conn.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM skills WHERE JSON_CONTAINS_PATH(payload, 'one', '$.content')),
  (SELECT COUNT(*) FROM skill_files)`).Scan(&legacyBodies, &files)
	if err != nil || legacyBodies != 4 || files != 2 {
		t.Fatalf("blocked migration changed the catalog: bodies=%d files=%d err=%v", legacyBodies, files, err)
	}

	if _, err := conn.ExecContext(ctx, `
UPDATE skill_files SET path = 'SKILL.md' WHERE skill_id = 'case-collision';
UPDATE skills SET payload = JSON_SET(payload, '$.content', REPEAT('x', 1048576)) WHERE id = 'oversized';
`); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := conn.ExecContext(ctx, string(migration)); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt, err)
		}
	}
	var migratedSize int64
	var uploaded string
	err = conn.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM skills WHERE JSON_CONTAINS_PATH(payload, 'one', '$.content')),
  (SELECT COUNT(*) FROM skill_files),
  (SELECT size FROM skill_files WHERE skill_id = 'oversized' AND path = 'SKILL.md'),
  (SELECT content FROM skill_files WHERE skill_id = 'uploaded' AND path = 'SKILL.md')`).Scan(&legacyBodies, &files, &migratedSize, &uploaded)
	if err != nil || legacyBodies != 0 || files != 4 || migratedSize != domain.SkillFileMaxBytes || uploaded != "Keep the upload" {
		t.Fatalf("migration readback: bodies=%d files=%d size=%d upload=%q err=%v", legacyBodies, files, migratedSize, uploaded, err)
	}
}
