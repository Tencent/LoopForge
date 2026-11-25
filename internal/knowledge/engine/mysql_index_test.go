package engine

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// overwriteMarkdown 覆盖 testMarkdown：去掉正文里的「麒麟」，换成「貔貅」，
// frontmatter 里的「狻猊标记」保持不变，用于验证旧正文不再命中。
const overwriteMarkdown = `---
title: "部署手册"
category: "guides"
keywords: "狻猊标记"
---

# 部署手册

貔貅服务器需要配置网关策略。
`

// TestMySQLKeywordIndexAndHostedWrite 用真实 MySQL 的 ngram 全文索引跑通
// 托管写入与关键词检索。未配置 AGENTFLOW_TEST_MYSQL_DSN 时跳过，与
// internal/repo 的集成测试一致。
func TestMySQLKeywordIndexAndHostedWrite(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	// 内容面与主库共用连接池，保证下面的清理 SQL 落在同一个库。
	t.Setenv("AGENTFLOW_KNOWLEDGE_MYSQL_DSN", "")
	s, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 043 脚本补建带 ngram 解析器的全文索引；gorm 的增量 DDL 建不出它。
	script, err := os.ReadFile("../../../migrations/043_builtin_knowledge.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range s5SplitSQL(string(script)) {
		if err := s.DB.Exec(stmt).Error; err != nil && !s5IsDuplicateSchemaError(err) {
			t.Fatalf("执行 043 失败：%v\nSQL: %s", err, stmt)
		}
	}

	ctx := context.Background()
	srcID := fmt.Sprintf("s5-%d", time.Now().UTC().UnixNano())
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{
		ID: srcID, BaseID: "base1", Kind: "hosted", Name: "S5 测试库",
		Status: "ready", IndexStatus: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = s.DB.Exec("DELETE FROM knowledge_chunks WHERE source_id=?", srcID)
		_ = s.DB.Exec("DELETE FROM knowledge_revisions WHERE source_id=?", srcID)
		_ = s.DB.Exec("DELETE FROM knowledge_documents WHERE source_id=?", srcID)
		_ = s.DeleteKnowledgeSource(srcID)
	}()

	eng := NewEngine(s, s, NewMySQLKeywordIndex(s))

	if _, err := eng.WriteDocument(ctx, srcID, "guides/a.md", testMarkdown); err != nil {
		t.Fatal(err)
	}
	hit := func(query string) bool {
		res, err := eng.Search(ctx, query, []string{srcID})
		if err != nil {
			t.Fatalf("检索 %q 失败：%v", query, err)
		}
		return len(res.Results) > 0
	}

	// 连续二字在 ngram=2 下命中；单字查询不报错（可以没有命中）。
	if !hit("麒麟") {
		t.Fatal("连续二字「麒麟」应命中 ngram 全文索引")
	}
	if _, err := eng.Search(ctx, "麒", []string{srcID}); err != nil {
		t.Fatalf("单字查询不应报错：%v", err)
	}
	// frontmatter 只用于元数据，不进可检索文本。
	if hit("狻猊") {
		t.Fatal("frontmatter 里的词不应命中")
	}

	// 覆盖后旧正文不再命中，新正文可命中。
	if _, err := eng.WriteDocument(ctx, srcID, "guides/a.md", overwriteMarkdown); err != nil {
		t.Fatal(err)
	}
	if hit("麒麟") {
		t.Fatal("覆盖后旧正文不应再命中")
	}
	if !hit("貔貅") {
		t.Fatal("覆盖后新正文应命中")
	}

	// 修订号递增，出处版本是修订号而不是 commit。
	doc, ok := s.KnowledgeDocument(srcID, "guides/a.md")
	if !ok || doc.Revision != 2 || doc.Commit != "2" || strings.Contains(doc.Body, "麒麟") {
		t.Fatalf("覆盖后文档/版本错误：%+v ok=%v", doc, ok)
	}
	if revs := s.KnowledgeRevisions(srcID, "guides/a.md"); len(revs) != 2 {
		t.Fatalf("应追加两条修订，got %d", len(revs))
	}

	// 检索只查调用方传入的来源。
	other, err := eng.Search(ctx, "貔貅", []string{"no-such-source"})
	if err != nil || len(other.Results) != 0 {
		t.Fatalf("未授权来源应返回空：%+v err=%v", other, err)
	}

	// Fetch 的行号与正文一致，出处版本是修订号。
	fetched, err := eng.Fetch(ctx, srcID, "guides/a.md", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Commit != "2" || fetched.StartLine != 1 || fetched.EndLine != 1 || len(fetched.Lines) != 1 {
		t.Fatalf("Fetch 结果错误：%+v", fetched)
	}
	if !strings.HasPrefix(fetched.Citation, srcID+"@2:guides/a.md#L1") {
		t.Fatalf("出处版本应是修订号：%q", fetched.Citation)
	}
}

type fakeFulltextQuerier struct {
	query   string
	sources []string
	limit   int
	matches []domain.KnowledgeChunkMatch
}

func (f *fakeFulltextQuerier) FulltextChunks(_ context.Context, query string, sourceIDs []string, limit int) ([]domain.KnowledgeChunkMatch, error) {
	f.query = query
	f.sources = append([]string(nil), sourceIDs...)
	f.limit = limit
	return f.matches, nil
}

// TestMySQLKeywordIndexLimitContract 验证输出条数契约仍然默认 10、上限 50；
// 适配器为了重排会多取候选，但不会把超过调用方 limit 的结果返回出去。
func TestMySQLKeywordIndexLimitContract(t *testing.T) {
	matches := make([]domain.KnowledgeChunkMatch, 0, MaxSearchLimit+10)
	for i := 0; i < MaxSearchLimit+10; i++ {
		matches = append(matches, domain.KnowledgeChunkMatch{
			Chunk: domain.KnowledgeChunk{
				SourceID: "s", Commit: "1", Path: fmt.Sprintf("doc-%02d.md", i),
				StartLine: 1, EndLine: 2, Text: fmt.Sprintf("麒麟内容 %d", i),
			},
			Score: float64(100 - i),
		})
	}
	cases := []struct{ in, want int }{
		{0, DefaultSearchLimit},
		{-5, DefaultSearchLimit},
		{MaxSearchLimit + 30, MaxSearchLimit},
		{25, 25},
	}
	for _, tc := range cases {
		q := &fakeFulltextQuerier{matches: matches}
		idx := NewMySQLKeywordIndex(q)
		hits, err := idx.Search(context.Background(), "麒麟", []string{"s"}, SearchOptions{Limit: tc.in})
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != tc.want {
			t.Fatalf("limit %d -> %d hits, want %d", tc.in, len(hits), tc.want)
		}
		if q.limit < tc.want || q.limit > MaxSearchLimit {
			t.Fatalf("候选取回 %d 条，应覆盖输出上限且不超过 %d", q.limit, MaxSearchLimit)
		}
		if q.query != "麒麟" || !reflect.DeepEqual(q.sources, []string{"s"}) {
			t.Fatalf("查询未原样下推: query=%q sources=%v", q.query, q.sources)
		}
		if hits[0].Source != "s" || hits[0].Score <= 0 {
			t.Fatalf("命中映射错误: %+v", hits[0])
		}
	}
}

// s5SplitSQL 把迁移脚本按分号拆成单条语句，跳过注释与空行。
func s5SplitSQL(script string) []string {
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

func s5IsDuplicateSchemaError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "1061") || strings.Contains(msg, "Duplicate key") || strings.Contains(msg, "already exists")
}

// TestMySQLKeywordIndexIdentifierChannel 用真实 MySQL 验证：自然语言候选被
// 高频中文块占满时，只有正文含完整 API 名的块能通过 BOOLEAN 精确通道补进来，
// 并在重排后进入前排。未配置 AGENTFLOW_TEST_MYSQL_DSN 时跳过。
func TestMySQLKeywordIndexIdentifierChannel(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	t.Setenv("AGENTFLOW_KNOWLEDGE_MYSQL_DSN", "")
	s, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	script, err := os.ReadFile("../../../migrations/043_builtin_knowledge.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range s5SplitSQL(string(script)) {
		if err := s.DB.Exec(stmt).Error; err != nil && !s5IsDuplicateSchemaError(err) {
			t.Fatalf("执行 043 失败：%v\nSQL: %s", err, stmt)
		}
	}

	ctx := context.Background()
	srcID := fmt.Sprintf("rank-%d", time.Now().UTC().UnixNano())
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{
		ID: srcID, BaseID: "base1", Kind: "hosted", Name: "排序测试库",
		Status: "ready", IndexStatus: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = s.DB.Exec("DELETE FROM knowledge_chunks WHERE source_id=?", srcID)
		_ = s.DB.Exec("DELETE FROM knowledge_revisions WHERE source_id=?", srcID)
		_ = s.DB.Exec("DELETE FROM knowledge_documents WHERE source_id=?", srcID)
		_ = s.DeleteKnowledgeSource(srcID)
	}()

	eng := NewEngine(s, s, NewMySQLKeywordIndex(s))
	generic := "# 防火墙规则\n\n" + strings.Repeat("防火墙规则是实例的网络入口，需要核对协议与端口。\n", 80)
	for i := 0; i < 8; i++ {
		path := fmt.Sprintf("generic-%02d.md", i)
		if _, err := eng.WriteDocument(ctx, srcID, path, generic); err != nil {
			t.Fatal(err)
		}
	}
	apiBody := "# 接口文档\n\n查询防火墙规则的接口是 DescribeInstanceFirewallRules，返回规则列表。\n"
	if _, err := eng.WriteDocument(ctx, srcID, "api-doc.md", apiBody); err != nil {
		t.Fatal(err)
	}

	res, err := eng.Search(ctx, "查询实例防火墙规则接口 DescribeInstanceFirewallRules", []string{srcID})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) == 0 {
		t.Fatal("检索结果为空")
	}
	found := -1
	for i, hit := range res.Results {
		if hit.Path == "api-doc.md" {
			found = i
			break
		}
	}
	if found < 0 || found > 2 {
		t.Fatalf("含完整 API 名的块应进入前三，实际位置 %d：%+v", found, res.Results)
	}
}
