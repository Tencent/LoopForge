package repo

import (
	"os"
	"strings"
	"testing"
)

// TestBuiltinKnowledgeMySQLFulltextIndex 断言 028 可重复执行，且执行后
// knowledge_chunks 上存在带 ngram 解析器的全文索引。未配置
// AGENTFLOW_TEST_MYSQL_DSN 时跳过。
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

	script, err := os.ReadFile("../../migrations/028_builtin_knowledge.sql")
	if err != nil {
		t.Fatal(err)
	}
	// dev.sh 每次启动都执行 028，运维也可能重跑；第二遍必须是空操作。
	for run := 1; run <= 2; run++ {
		for _, stmt := range splitSQLStatements(string(script)) {
			if err := s.DB.Exec(stmt).Error; err != nil {
				t.Fatalf("第 %d 次执行 028 失败：%v\nSQL: %s", run, err, stmt)
			}
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

// splitSQLStatements 把迁移脚本拆成单条语句。迁移里有同一行写多条语句的
// 动态 SQL，因此不能按行尾分号切；这里按引号/反引号/注释之外的分号切。
func splitSQLStatements(script string) []string {
	var out []string
	var b strings.Builder
	inSingle, inDouble, inBacktick, inLineComment, inBlockComment := false, false, false, false, false
	for i := 0; i < len(script); i++ {
		c := script[i]
		switch {
		case inLineComment:
			if c == '\n' {
				inLineComment = false
				b.WriteByte(c)
			}
			continue
		case inBlockComment:
			if c == '*' && i+1 < len(script) && script[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		case inSingle:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(script) {
				i++
				b.WriteByte(script[i])
			} else if c == '\'' {
				if i+1 < len(script) && script[i+1] == '\'' {
					i++
					b.WriteByte(script[i])
				} else {
					inSingle = false
				}
			}
			continue
		case inDouble:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(script) {
				i++
				b.WriteByte(script[i])
			} else if c == '"' {
				if i+1 < len(script) && script[i+1] == '"' {
					i++
					b.WriteByte(script[i])
				} else {
					inDouble = false
				}
			}
			continue
		case inBacktick:
			b.WriteByte(c)
			if c == '`' {
				if i+1 < len(script) && script[i+1] == '`' {
					i++
					b.WriteByte(script[i])
				} else {
					inBacktick = false
				}
			}
			continue
		}
		switch c {
		case '\'':
			inSingle = true
			b.WriteByte(c)
		case '"':
			inDouble = true
			b.WriteByte(c)
		case '`':
			inBacktick = true
			b.WriteByte(c)
		case '#':
			inLineComment = true
		case '-':
			if i+1 < len(script) && script[i+1] == '-' && (i+2 == len(script) || script[i+2] == ' ' || script[i+2] == '\t' || script[i+2] == '\n') {
				inLineComment = true
				i++
			} else {
				b.WriteByte(c)
			}
		case '/':
			if i+1 < len(script) && script[i+1] == '*' {
				inBlockComment = true
				i++
			} else {
				b.WriteByte(c)
			}
		case ';':
			if stmt := strings.TrimSpace(b.String()); stmt != "" {
				out = append(out, stmt)
			}
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	if stmt := strings.TrimSpace(b.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}
