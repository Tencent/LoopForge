package engine

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// normalizeSearchQuery 去掉问句成分后，仍要把中文技术口语补成文档里常用的
// 英文缩写；否则“数据库连接串的环境变量”这类问题在英文部署文档上永远排
// 不到前面。这里锁定期望的下推查询，防止以后又悄悄把疑问词加回索引。
func TestNormalizeSearchQueryDropsQuestionWords(t *testing.T) {
	cases := []struct{ in, want string }{
		{"数据库连接串的环境变量叫什么", "数据库连接串的环境变量 DATABASE DSN ENV"},
		{"怎么在本机把平台跑起来", "在本机把平台跑起来 LOCAL PLATFORM RUN"},
		{"默认端口是多少", "默认端口 DEFAULT PORT"},
		{"今天天气怎么样？", "今天天气"},
		{"ngram", "ngram"},
	}
	for _, tc := range cases {
		if got := normalizeSearchQuery(tc.in); got != tc.want {
			t.Fatalf("normalizeSearchQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 单字虚词只降权，不直接从查询里删除；“目的”“和平”这类正常词不能被拆坏。
func TestFunctionWordTermsAreDownweighted(t *testing.T) {
	if got := termWeight("目的"); got >= 1 {
		t.Fatalf("含“的”的二字片段应降权，got %v", got)
	}
	if got := termWeight("端口"); got != 1 {
		t.Fatalf("普通二字片段不应降权，got %v", got)
	}
	q := &fakeFulltextQuerier{matches: []domain.KnowledgeChunkMatch{
		chunkMatch("answer.md", "部署手册", "", "数据库连接串的环境变量是 AGENTFLOW_MYSQL_DSN。"),
	}}
	hits, err := NewMySQLKeywordIndex(q).Search(context.Background(), "数据库连接串的环境变量叫什么", []string{"s"}, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(q.query, "什么") || strings.Contains(q.query, "叫") {
		t.Fatalf("疑问词不应下推到索引：%q", q.query)
	}
	if len(hits) != 1 {
		t.Fatalf("应命中一条，got %d", len(hits))
	}
}

// 词根本不在候选里时，不能靠 ngram 片段凑满 10 条。
func TestKeywordIndexDropsFragmentOnlyMatches(t *testing.T) {
	fragments := make([]domain.KnowledgeChunkMatch, 0, 10)
	for i := 0; i < 10; i++ {
		fragments = append(fragments, chunkMatch(fmt.Sprintf("fragment-%02d.md", i), "英文片段", "", "ng gr ra am 的二字片段，不含完整查询词。"))
	}
	q := &fakeFulltextQuerier{matches: fragments}
	hits, err := NewMySQLKeywordIndex(q).Search(context.Background(), "ngram", []string{"s"}, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("只有片段的候选应返回空，got %d", len(hits))
	}

	relevant := append([]domain.KnowledgeChunkMatch{chunkMatch("answer.md", "ngram 配置", "", "ngram 是 MySQL 全文解析器。")}, fragments...)
	q = &fakeFulltextQuerier{matches: relevant}
	hits, err = NewMySQLKeywordIndex(q).Search(context.Background(), "ngram", []string{"s"}, SearchOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "answer.md" {
		t.Fatalf("含完整查询词的块应保留：%+v", hits)
	}
}

// 相关度门槛按候选分数分布判断：第一名没有明显高于中位数且覆盖度低时，
// 整批按空处理；真实命中则只保留分数接近第一名的候选。
func TestRelevanceGateUsesCandidateDistribution(t *testing.T) {
	weak := []*rerankCandidate{
		{score: 0.11, termCoverage: 0.10, queryCoverage: 0.10},
		{score: 0.105, termCoverage: 0.10, queryCoverage: 0.10},
		{score: 0.10, termCoverage: 0.10, queryCoverage: 0.10},
		{score: 0.095, termCoverage: 0.10, queryCoverage: 0.10},
	}
	if got := relevantCandidates(weak); len(got) != 0 {
		t.Fatalf("弱匹配批次应清空，got %d", len(got))
	}

	strong := []*rerankCandidate{
		{score: 1.0, termCoverage: 0.6, queryCoverage: 0.6},
		{score: 0.01, termCoverage: 0.1, queryCoverage: 0.1},
		{score: 0.005, termCoverage: 0.1, queryCoverage: 0.1},
		{score: 0.001, termCoverage: 0.1, queryCoverage: 0.1},
	}
	got := relevantCandidates(strong)
	if len(got) != 1 || got[0] != strong[0] {
		t.Fatalf("只应保留明显领先的第一名：%+v", got)
	}

	single := []*rerankCandidate{{score: 0.2, termCoverage: 1, queryCoverage: 1}}
	if got := relevantCandidates(single); len(got) != 1 {
		t.Fatalf("单候选真实命中不应被门槛丢掉：%+v", got)
	}
	noCoverage := []*rerankCandidate{{score: 1, termCoverage: 0, queryCoverage: 0}, {score: 0.5, termCoverage: 0, queryCoverage: 0}}
	if got := relevantCandidates(noCoverage); len(got) != 0 {
		t.Fatalf("没有任何查询词覆盖时应清空：%+v", got)
	}
}

// 走查环境索引的是 f21b960 的 25 篇 docs 快照，设计提案
// docs/proposals/builtin-knowledge-engine.md 是之后才加入的；回归语料保持
// 走查时的快照，用一个人工构造的、文档里不存在的英文词验证空召回。
const docsRegressionSkip = "proposals/builtin-knowledge-engine.md"

// openDocsRegressionStore 为回归用例准备一个干净的全文索引。MySQL 的
// InnoDB FULLTEXT 在删除行后仍会保留统计信息，同一库重复跑会让 ngram 排序
// 漂移；用例建一个临时库，跑完删除。没有建库权限时跳过，避免改动共享测试库
// 里其他用例的全文表。
func openDocsRegressionStore(t *testing.T, dsn string) (*repo.Store, func()) {
	t.Helper()
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	dbName := fmt.Sprintf("kb_docs_regression_%d", time.Now().UTC().UnixNano())
	adminCfg := *cfg
	adminCfg.DBName = ""
	admin, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		t.Fatalf("打开 MySQL 管理连接失败：%v", err)
	}
	_, createErr := admin.Exec("CREATE DATABASE `" + dbName + "` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	admin.Close()
	if createErr != nil {
		t.Skipf("回归用例需要 CREATE DATABASE 权限来隔离全文索引：%v", createErr)
	}
	tmp := *cfg
	tmp.DBName = dbName
	store, err := repo.Open(tmp.FormatDSN())
	if err != nil {
		dropDocsRegressionDB(t, adminCfg.FormatDSN(), dbName)
		t.Fatal(err)
	}
	return store, func() {
		store.Close()
		dropDocsRegressionDB(t, adminCfg.FormatDSN(), dbName)
	}
}

func dropDocsRegressionDB(t *testing.T, adminDSN, dbName string) {
	t.Helper()
	admin, err := sql.Open("mysql", adminDSN)
	if err != nil {
		t.Logf("打开 MySQL 管理连接失败：%v", err)
		return
	}
	defer admin.Close()
	if _, err := admin.Exec("DROP DATABASE IF EXISTS `" + dbName + "`"); err != nil {
		t.Logf("删除临时库 %s 失败：%v", dbName, err)
	}
}

// TestMySQLKeywordIndexLoopForgeDocsQuestions 用走查报告里的四句在 LoopForge
// docs 语料上做回归：问句指向的部署文档要进入前三，库里没有的词返回空。
func TestMySQLKeywordIndexLoopForgeDocsQuestions(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	t.Setenv("AGENTFLOW_KNOWLEDGE_MYSQL_DSN", "")
	s, cleanup := openDocsRegressionStore(t, dsn)
	defer cleanup()
	script, err := os.ReadFile("../../../migrations/028_builtin_knowledge.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range s5SplitSQL(string(script)) {
		if err := s.DB.Exec(stmt).Error; err != nil && !s5IsDuplicateSchemaError(err) {
			t.Fatalf("执行 028 失败：%v\nSQL: %s", err, stmt)
		}
	}

	ctx := context.Background()
	srcID := fmt.Sprintf("docs-regression-%d", time.Now().UTC().UnixNano())
	if err := s.PutKnowledgeSource(domain.KnowledgeSource{
		ID: srcID, BaseID: "docs-regression", Kind: "hosted", Name: "LoopForge docs",
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
	root := "../../../docs"
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == docsRegressionSkip {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := eng.WriteDocument(ctx, srcID, rel, string(body)); err != nil {
			return fmt.Errorf("写入 %s: %w", rel, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	search := func(query string) []string {
		res, err := eng.Search(ctx, query, []string{srcID})
		if err != nil {
			t.Fatalf("检索 %q 失败：%v", query, err)
		}
		paths := make([]string, 0, len(res.Results))
		for _, hit := range res.Results {
			paths = append(paths, hit.Path)
		}
		return paths
	}
	assertDeploymentNearTop := func(query string) {
		t.Helper()
		paths := search(query)
		for i, path := range paths {
			if path == "deployment.md" {
				if i > 2 {
					t.Fatalf("%q 的部署文档应在前三，实际第 %d：%v", query, i+1, paths)
				}
				return
			}
		}
		t.Fatalf("%q 未召回 deployment.md：%v", query, paths)
	}

	assertDeploymentNearTop("数据库连接串的环境变量叫什么")
	assertDeploymentNearTop("怎么在本机把平台跑起来")
	assertDeploymentNearTop("默认端口是多少")
	if paths := search("zzzzunindexed"); len(paths) != 0 {
		t.Fatalf("库里没有 zzzzunindexed 时应返回空，got %v", paths)
	}
}
