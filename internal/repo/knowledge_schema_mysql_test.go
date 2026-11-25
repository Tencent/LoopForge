package repo

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"

	mysqldriver "github.com/go-sql-driver/mysql"
)

type migrationSegment struct {
	plane string
	sql   string
}

// Keep the test independent of internal/knowledge (which imports repo) while
// matching HostedSourceID's exact hash seed and truncation.
func hostedSourceIDForTest(baseID string) string {
	sum := sha256.Sum256([]byte("hosted:" + baseID))
	return "kb-hosted-" + hex.EncodeToString(sum[:16])
}

// TestKnowledgeMigrationsOnSeparateContentDatabase 覆盖审查报告 P1-4/P2-5：
// 043–055 每份脚本连续执行两次都不报错；控制段只落主库、内容段只落内容库；
// ngram 全文索引、路径 utf8mb4_bin、可空 submission_id 与升级前 payload 正文
// 搬迁都生效。未配置 AGENTFLOW_TEST_MYSQL_DSN 时跳过。
func TestKnowledgeMigrationsOnSeparateContentDatabase(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().UnixNano()
	controlDB := fmt.Sprintf("kb_schema_%d", stamp)
	contentDB := fmt.Sprintf("kb_schema_content_%d", stamp)
	adminCfg := *cfg
	adminCfg.DBName = ""
	admin, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, name := range []string{controlDB, contentDB} {
		if _, err := admin.Exec("CREATE DATABASE `" + name + "` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
			t.Skipf("需要 CREATE DATABASE 权限来隔离控制面/内容面：%v", err)
		}
		defer admin.Exec("DROP DATABASE IF EXISTS `" + name + "`")
	}
	controlCfg, contentCfg := *cfg, *cfg
	controlCfg.DBName, contentCfg.DBName = controlDB, contentDB
	controlDSN, contentDSN := controlCfg.FormatDSN(), contentCfg.FormatDSN()

	// 模拟升级前的安装：先有 gorm 增量建出的表，再插入一条仍把正文放在
	// payload 里的待审记录，随后执行迁移。
	t.Setenv("AGENTFLOW_KNOWLEDGE_MYSQL_DSN", contentDSN)
	before, err := Open(controlDSN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := before.KnowledgeSchemaVersion(); err != nil {
		t.Fatal(err)
	}
	if err := before.CheckKnowledgeSchema(); err == nil {
		t.Fatal("a database without 054 must fail the startup schema check")
	}
	legacy := `{"id":"kbs-legacy","projectId":"p1","title":"旧待审","source":"kb-hosted-p1","path":"docs/legacy.md","content":"LEGACY-BODY-SECRET","status":"pending"}`
	if err := before.DB.Exec("INSERT INTO knowledge_submissions (id, project_id, status, payload, created_at, updated_at) VALUES (?, ?, ?, CAST(? AS JSON), UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))",
		"kbs-legacy", "p1", domain.KnowledgeSubmissionPending, legacy).Error; err != nil {
		t.Fatal(err)
	}
	// Pre-054 data: a hosted source named kb-hosted-<project>, its content, and
	// a retracted fence. A stale fence under the target ID must be merged.
	oldSource, newSource := "kb-hosted-p1", hostedSourceIDForTest("kb-p1")
	if err := before.PutKnowledgeBase(domain.KnowledgeBase{ID: "kb-p1", ProjectID: "p1", Name: "旧项目库", Layer: "project"}); err != nil {
		t.Fatal(err)
	}
	if err := before.PutKnowledgeSource(domain.KnowledgeSource{ID: oldSource, BaseID: "kb-p1", Kind: "hosted"}); err != nil {
		t.Fatal(err)
	}
	if err := before.PutKnowledgeDocument(domain.KnowledgeDocument{SourceID: oldSource, Path: "docs/fenced.md", Title: "Fenced", Body: "FENCED-CONTENT", Commit: "7", Revision: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := execOn(t, contentDSN, "INSERT INTO knowledge_content_fences (source_id, path, token, retracted) VALUES (?, ?, 5, FALSE)", oldSource, "docs/fenced.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := execOn(t, contentDSN, "INSERT INTO knowledge_content_fences (source_id, path, token, retracted) VALUES (?, ?, 9, TRUE)", newSource, "docs/fenced.md"); err != nil {
		t.Fatal(err)
	}
	before.Close()

	files := knowledgeMigrationFiles(t)
	if len(files) == 0 {
		t.Fatal("migrations/043+ not found")
	}
	for run := 1; run <= 2; run++ {
		for _, file := range files {
			for _, segment := range splitMigrationSegments(t, file) {
				target := controlDSN
				prefix := ""
				if segment.plane == "content" {
					target = contentDSN
					prefix = "SET @knowledge_control_schema = '" + controlDB + "';\n"
				}
				execMigrationSegment(t, target, prefix+segment.sql, file, run)
			}
		}
	}

	// 054 renames hosted source IDs on both planes and moves fences with them.
	var migratedSource string
	if err := queryRow(t, controlDSN, "SELECT id FROM knowledge_sources WHERE base_id = 'kb-p1' AND kind = 'hosted'", &migratedSource); err != nil {
		t.Fatal(err)
	}
	if migratedSource != newSource {
		t.Fatalf("hosted source id = %q, want %q", migratedSource, newSource)
	}
	var documentSource, fencedSource string
	if err := queryRow(t, contentDSN, "SELECT source_id FROM knowledge_documents WHERE path = 'docs/fenced.md'", &documentSource); err != nil {
		t.Fatal(err)
	}
	if documentSource != newSource {
		t.Fatalf("content document source_id = %q, want %q", documentSource, newSource)
	}
	var fenceCount, fenceToken int
	var fenceRetracted bool
	if err := queryRow(t, contentDSN, "SELECT COUNT(*) FROM knowledge_content_fences WHERE path = 'docs/fenced.md'", &fenceCount); err != nil {
		t.Fatal(err)
	}
	if fenceCount != 1 {
		t.Fatalf("fence rows = %d, want 1", fenceCount)
	}
	if err := queryRow(t, contentDSN, "SELECT source_id, token, retracted FROM knowledge_content_fences WHERE path = 'docs/fenced.md'", &fencedSource, &fenceToken, &fenceRetracted); err != nil {
		t.Fatal(err)
	}
	if fencedSource != newSource || fenceToken != 9 || !fenceRetracted {
		t.Fatalf("fence was not merged: source=%q token=%d retracted=%t", fencedSource, fenceToken, fenceRetracted)
	}
	var reviewSource string
	if err := queryRow(t, controlDSN, "SELECT JSON_UNQUOTE(JSON_EXTRACT(payload, '$.source')) FROM knowledge_submissions WHERE id = 'kbs-legacy'", &reviewSource); err != nil {
		t.Fatal(err)
	}
	if reviewSource != newSource {
		t.Fatalf("review source = %q, want %q", reviewSource, newSource)
	}
	var version int
	if err := queryRow(t, controlDSN, "SELECT MAX(version) FROM knowledge_schema_versions", &version); err != nil {
		t.Fatal(err)
	}
	if version != KnowledgeSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, KnowledgeSchemaVersion)
	}

	// 主库不能出现内容表；内容库必须有正文表与 staging。
	for _, table := range []string{"knowledge_documents", "knowledge_revisions", "knowledge_chunks", "knowledge_submission_contents"} {
		if tableExists(t, controlDSN, controlDB, table) {
			t.Fatalf("主库不应创建内容表 %s", table)
		}
		if !tableExists(t, contentDSN, contentDB, table) {
			t.Fatalf("内容库缺少表 %s", table)
		}
	}

	// 升级前待在 payload 里的正文应搬到内容面 staging，并从控制面删除。
	var staged string
	if err := queryRow(t, contentDSN, "SELECT body FROM knowledge_submission_contents WHERE submission_id = 'kbs-legacy'", &staged); err != nil {
		t.Fatalf("staging 正文未迁移：%v", err)
	}
	if !strings.Contains(staged, "LEGACY-BODY-SECRET") {
		t.Fatalf("staging 正文 = %q", staged)
	}
	var payload string
	if err := queryRow(t, controlDSN, "SELECT payload FROM knowledge_submissions WHERE id = 'kbs-legacy'", &payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "LEGACY-BODY-SECRET") || strings.Contains(payload, `"content"`) {
		t.Fatalf("迁移后控制面 payload 仍含正文：%s", payload)
	}

	// ngram 全文索引必须由 043 内容段建出来，gorm 建不出解析器。
	var indexType string
	if err := queryRow(t, contentDSN, "SELECT INDEX_TYPE FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_chunks' AND INDEX_NAME = 'ft_knowledge_chunks_text'", &indexType); err != nil {
		t.Fatal(err)
	}
	if indexType != "FULLTEXT" {
		t.Fatalf("全文索引类型 = %q", indexType)
	}
	var tableName, ddl string
	if err := queryRow(t, contentDSN, "SHOW CREATE TABLE knowledge_chunks", &tableName, &ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "ngram") {
		t.Fatalf("全文索引没有 ngram 解析器：%s", ddl)
	}

	// 路径唯一键按字节比较；写回记录允许 NULL submission_id。
	for _, spec := range []struct {
		dsn, table string
	}{
		{controlDSN, "knowledge_writebacks"},
		{controlDSN, "knowledge_tombstones"},
		{controlDSN, "knowledge_feedback"},
		{controlDSN, "knowledge_governance_issues"},
		{controlDSN, "knowledge_citations"},
		{contentDSN, "knowledge_documents"},
		{contentDSN, "knowledge_revisions"},
		{contentDSN, "knowledge_chunks"},
	} {
		var collation string
		if err := queryRow(t, spec.dsn, "SELECT COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = '"+spec.table+"' AND COLUMN_NAME = 'path'", &collation); err != nil {
			t.Fatalf("%s.path: %v", spec.table, err)
		}
		if collation != "utf8mb4_bin" {
			t.Fatalf("%s.path collation = %q, want utf8mb4_bin", spec.table, collation)
		}
	}
	var nullable string
	if err := queryRow(t, controlDSN, "SELECT IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'knowledge_writebacks' AND COLUMN_NAME = 'submission_id'", &nullable); err != nil {
		t.Fatal(err)
	}
	if nullable != "YES" {
		t.Fatalf("knowledge_writebacks.submission_id IS_NULLABLE = %q", nullable)
	}
	for _, id := range []string{"kwb-null-a", "kwb-null-b"} {
		if _, err := execOn(t, controlDSN, "INSERT INTO knowledge_writebacks (id, submission_id) VALUES (?, NULL)", id); err != nil {
			t.Fatalf("插 NULL submission_id %s: %v", id, err)
		}
	}

	// 047 对 permission IS NULL 也要归一为 read（老库可能存在可空列）。
	if _, err := execOn(t, controlDSN, "ALTER TABLE project_knowledge_bindings MODIFY permission VARCHAR(32) NULL"); err != nil {
		t.Fatal(err)
	}
	if _, err := execOn(t, controlDSN, "INSERT INTO knowledge_bases (id, project_id, name, layer) VALUES ('kb-null-base', NULL, '公共库', 'shared')"); err != nil {
		t.Fatal(err)
	}
	if _, err := execOn(t, controlDSN, "INSERT INTO project_knowledge_bindings (project_id, base_id, permission) VALUES ('p-null', 'kb-null-base', NULL)"); err != nil {
		t.Fatal(err)
	}
	if err := execMigrationFile(t, controlDSN, "../../migrations/047_shared_knowledge.sql"); err != nil {
		t.Fatal(err)
	}
	var permission string
	if err := queryRow(t, controlDSN, "SELECT permission FROM project_knowledge_bindings WHERE project_id = 'p-null' AND base_id = 'kb-null-base'", &permission); err != nil {
		t.Fatal(err)
	}
	if permission != "read" {
		t.Fatalf("047 未归一 NULL permission：%q", permission)
	}

	// 真实独立内容库 DSN：写入正文、检索命中，再验证控制面只留元数据。
	store, err := Open(controlDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CheckKnowledgeSchema(); err != nil {
		t.Fatalf("startup schema check after 054: %v", err)
	}
	src := "src-separate"
	if err := store.PutKnowledgeSource(domain.KnowledgeSource{ID: src, BaseID: "kb-separate", Kind: "hosted", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	doc := domain.KnowledgeDocument{SourceID: src, Path: "docs/guide.md", Title: "部署指南", Body: "# 部署指南\n\n知识检索使用 ngram 全文索引。", LineCount: 3, Commit: "1", Revision: 1}
	chunks := []domain.KnowledgeChunk{{Idx: 0, Heading: "部署指南", Title: "部署指南", StartLine: 1, EndLine: 3, Commit: "1", Revision: 1, Text: "知识检索使用 ngram 全文索引。"}}
	if err := store.WriteHostedDocument(doc, domain.KnowledgeRevision{ID: "rev-separate", SourceID: src, Path: doc.Path, Revision: 1, Body: doc.Body}, chunks); err != nil {
		t.Fatal(err)
	}
	hits, err := store.FulltextChunks(context.Background(), "检索", []string{src}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Chunk.Path != doc.Path {
		t.Fatalf("独立内容库检索未命中：%+v", hits)
	}
	body := "STAGED-AFTER-MIGRATION"
	if err := store.PutKnowledgeSubmission(domain.KnowledgeSubmission{ID: "kbs-after", ProjectID: "p1", Content: body, Status: domain.KnowledgeSubmissionPending}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.KnowledgeSubmission("kbs-after"); got.Content != body {
		t.Fatalf("独立内容库提交正文读取 = %q", got.Content)
	}
	var controlPayload string
	if err := queryRow(t, controlDSN, "SELECT payload FROM knowledge_submissions WHERE id = 'kbs-after'", &controlPayload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(controlPayload, body) {
		t.Fatalf("控制面 payload 仍含正文：%s", controlPayload)
	}
}

func execMigrationFile(t *testing.T, dsn, file string) error {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	for _, stmt := range splitSQLStatements(string(raw)) {
		if _, err := execOn(t, dsn, stmt); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
	}
	return nil
}

func knowledgeMigrationFiles(t *testing.T) []string {
	t.Helper()
	// Keep this list explicit and in sync with scripts/knowledge-schema.sh.
	// Upstream 026_assistant_session_reads.sql and 027_cli_model_catalog.sql are
	// published optional manual DDL and must not be part of the knowledge sweep.
	names := []string{
		"043_builtin_knowledge.sql",
		"044_knowledge_activity.sql",
		"045_knowledge_vector.sql",
		"046_knowledge_git_credentials.sql",
		"047_shared_knowledge.sql",
		"048_knowledge_writeback.sql",
		"049_knowledge_sediment.sql",
		"050_knowledge_governance.sql",
		"051_knowledge_citation.sql",
		"052_knowledge_concurrency.sql",
		"053_knowledge_schema_cleanup.sql",
		"054_knowledge_source_namespace.sql",
		"055_knowledge_governance_noise.sql",
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		file := filepath.Join("..", "..", "migrations", name)
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("knowledge migration %s: %v", name, err)
		}
		out = append(out, file)
	}
	return out
}

func splitMigrationSegments(t *testing.T, file string) []migrationSegment {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	segments := []migrationSegment{}
	plane := "control"
	var b strings.Builder
	flush := func() {
		if strings.TrimSpace(b.String()) == "" {
			b.Reset()
			return
		}
		segments = append(segments, migrationSegment{plane: plane, sql: b.String()})
		b.Reset()
	}
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "-- @plane control" || trimmed == "-- @plane content" {
			flush()
			plane = strings.TrimPrefix(trimmed, "-- @plane ")
			continue
		}
		b.WriteString(line)
	}
	flush()
	return segments
}

func execMigrationSegment(t *testing.T, dsn, script, file string, run int) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, stmt := range splitSQLStatements(script) {
		if _, err := conn.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("%s 第 %d 次执行失败：%v\nSQL: %s", filepath.Base(file), run, err, stmt)
		}
	}
}

func tableExists(t *testing.T, dsn, database, table string) bool {
	t.Helper()
	var count int
	if err := queryRow(t, dsn, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = '"+database+"' AND TABLE_NAME = '"+table+"'", &count); err != nil {
		t.Fatal(err)
	}
	return count > 0
}

func queryRow(t *testing.T, dsn, statement string, dest ...any) error {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	row := db.QueryRow(statement)
	return row.Scan(dest...)
}

func execOn(t *testing.T, dsn, statement string, args ...any) (sql.Result, error) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return db.Exec(statement, args...)
}
