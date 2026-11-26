package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/knowledge"
	"agentflow/internal/knowledge/engine/embed"
)

// TestHybridRelevanceOnLiveVector 在真实 MySQL + Qdrant + embedding 上跑走查
// P1-1 的回归：无关词/单字/旧正文词返回空，口语问句把部署文档排在首位，
// 覆盖后旧正文的向量随块替换删除。测试用独立临时库与 collection，未配置
// AGENTFLOW_TEST_MYSQL_DSN / AGENTFLOW_TEST_KB_QDRANT_URL /
// AGENTFLOW_TEST_KB_EMBEDDING_URL 时跳过。
func TestHybridRelevanceOnLiveVector(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_MYSQL_DSN"))
	qdrantURL := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_KB_QDRANT_URL"))
	embeddingURL := strings.TrimSpace(os.Getenv("AGENTFLOW_TEST_KB_EMBEDDING_URL"))
	if dsn == "" || qdrantURL == "" || embeddingURL == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN / AGENTFLOW_TEST_KB_QDRANT_URL / AGENTFLOW_TEST_KB_EMBEDDING_URL is not set")
	}
	t.Setenv("AGENTFLOW_KNOWLEDGE_MYSQL_DSN", "")
	s, cleanupDB := openDocsRegressionStore(t, dsn)
	defer cleanupDB()

	script, err := os.ReadFile("../../../migrations/044_builtin_knowledge.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range s5SplitSQL(string(script)) {
		if err := s.DB.Exec(stmt).Error; err != nil && !s5IsDuplicateSchemaError(err) {
			t.Fatalf("执行 044 失败：%v\nSQL: %s", err, stmt)
		}
	}

	cfg := VectorConfig{
		QdrantURL: qdrantURL, Collection: fmt.Sprintf("kb_r3_hybrid_%d", time.Now().UTC().UnixNano()),
		EmbeddingKind: embed.Sidecar, EmbeddingURL: embeddingURL,
		Model: "gpahal/bge-m3-onnx-int8", Dim: 1024, MaxLength: 128,
	}
	rt := newVectorRuntime(cfg, s, s)
	restore := setGlobalVectorForTest(rt)
	defer restore()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = rt.qdrant.DeleteCollection(ctx, cfg.Collection)
	}()

	svc := knowledge.NewService(s, nil, nil)
	base, err := svc.EnsureHostedBase(fmt.Sprintf("r3-hybrid-%d", time.Now().UTC().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	sources := s.KnowledgeSources(base.ID)
	if len(sources) == 0 {
		t.Fatal("托管知识库没有来源")
	}
	src := sources[0].ID

	eng := NewEngine(s, s, NewMySQLKeywordIndex(s))
	ctx := context.Background()
	docs := map[string]string{
		"deployment.md":                        "# Deployment\n\n## Configuration\n\n数据库连接串的环境变量是 `AGENTFLOW_MYSQL_DSN`。\n\nThe MySQL DSN is read from AGENTFLOW_MYSQL_DSN.\n",
		"guides/qingluan.md":                   "# 青鸾手册\n\n青鸾新羽替换了原来的标记，窗口布局保持原样。\n",
		"guides/pixiu.md":                      "# 貔貅账册\n\n貔貅账册记录每日结算和库存快照。\n",
		"guides/layout.md":                     "# 前端布局\n\n前端页面使用网格布局，抽屉从右侧滑出，主题支持深色模式。\n",
		"guides/runner.md":                     "# 执行器\n\n执行器注册后上报心跳，任务队列按先后顺序调度。\n",
		"guides/token.md":                      "# 令牌价格\n\n令牌按调用量计价，价格版本按月更新。\n",
		"final-walkthrough/overwrite-check.md": "# 覆盖测试\n\n菠萝蜜甲只存在于第一版。\n",
	}
	for path, body := range docs {
		if _, err := eng.WriteDocument(ctx, src, path, body); err != nil {
			t.Fatalf("写入 %s 失败：%v", path, err)
		}
	}
	if err := EnableVectorForBase(ctx, base.ID); err != nil {
		t.Fatal(err)
	}

	search := func(query string) knowledge.SearchResult {
		t.Helper()
		res, err := eng.Search(ctx, query, []string{src})
		if err != nil {
			t.Fatalf("检索 %q 失败：%v", query, err)
		}
		if res.Mode != "hybrid" || res.Degraded {
			t.Fatalf("向量通道应可用，实际 mode=%s degraded=%v reason=%s", res.Mode, res.Degraded, res.DegradeReason)
		}
		return res
	}
	assertEmpty := func(query string) {
		t.Helper()
		res := search(query)
		if len(res.Results) != 0 {
			t.Fatalf("%q 应返回空，实际 %d 条：%+v", query, len(res.Results), hitPaths(res))
		}
	}
	assertFirst := func(query, path string) {
		t.Helper()
		res := search(query)
		if len(res.Results) == 0 || res.Results[0].Path != path {
			t.Fatalf("%q 首条应为 %s，实际：%+v", query, path, hitPaths(res))
		}
	}

	// 走查 P1-1：单字、库里没有的词、未收录的长查询不应凑满 10 条。
	assertEmpty("a")
	assertEmpty("ngram")
	assertEmpty("量子纠缠香蕉皮运输规范")
	// 口语问句应把部署文档排在首位，而不是语义近邻。
	assertFirst("数据库连接串的环境变量叫什么", "deployment.md")

	// 旧正文词覆盖前能命中；覆盖为新正文后，关键词与向量都不应再返回它。
	assertFirst("菠萝蜜甲", "final-walkthrough/overwrite-check.md")
	if _, err := eng.WriteDocument(ctx, src, "final-walkthrough/overwrite-check.md", "# 覆盖测试\n\n火龙果乙只存在于第二版。\n"); err != nil {
		t.Fatal(err)
	}
	assertEmpty("菠萝蜜甲")
	assertFirst("火龙果乙", "final-walkthrough/overwrite-check.md")

	vecHits, err := rt.Search(ctx, "菠萝蜜甲", []string{src}, vectorWindow)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range vecHits {
		if strings.Contains(h.Text, "菠萝蜜甲") {
			t.Fatalf("覆盖后旧正文的向量点仍存在：%s#%d %q", h.Path, h.StartLine, h.Text)
		}
	}
}

func hitPaths(res knowledge.SearchResult) []string {
	out := make([]string, 0, len(res.Results))
	for _, h := range res.Results {
		out = append(out, h.Path)
	}
	return out
}
