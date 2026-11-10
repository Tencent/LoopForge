package repo

import (
	"os"
	"strings"
	"testing"
)

// TestBuiltinKnowledgeMySQLFulltextIndex 断言执行 026 之后 knowledge_chunks 上
// 存在带 ngram 解析器的全文索引。未配置 AGENTFLOW_TEST_MYSQL_DSN 时跳过。
func TestBuiltinKnowledgeMySQLFulltextIndex(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	t.Setenv("AGENTFLOW_KNOWLEDGE_MYSQL_DSN", "")
	s, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	script, err := os.ReadFile("../../migrations/026_builtin_knowledge.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitSQLStatements(string(script)) {
		if err := s.DB.Exec(stmt).Error; err != nil && !isDuplicateSchemaError(err) {
			t.Fatalf("执行 026 失败：%v\nSQL: %s", err, stmt)
		}
	}

	var indexType string
	if err := s.DB.Raw(`SELECT INDEX_TYPE FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='knowledge_chunks' AND INDEX_NAME='ft_knowledge_chunks_text' LIMIT 1`).Scan(&indexType).Error; err != nil {
		t.Fatal(err)
	}
	if strings.ToUpper(indexType) != "FULLTEXT" {
		t.Fatalf("ft_knowledge_chunks_text 不是 FULLTEXT：%q", indexType)
	}

	var tableName, ddl string
	if err := s.DB.Raw("SHOW CREATE TABLE knowledge_chunks").Row().Scan(&tableName, &ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "ngram") {
		t.Fatalf("全文索引没有 ngram 解析器：%s", ddl)
	}
}

// splitSQLStatements 把迁移脚本按分号拆成单条语句，跳过注释与空行。
func splitSQLStatements(script string) []string {
	var out []string
	var b strings.Builder
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if strings.HasSuffix(trimmed, ";") {
			stmt := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(b.String()), ";"))
			if stmt != "" {
				out = append(out, stmt)
			}
			b.Reset()
		}
	}
	return out
}

func isDuplicateSchemaError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "1061") || strings.Contains(msg, "Duplicate key") || strings.Contains(msg, "already exists")
}
