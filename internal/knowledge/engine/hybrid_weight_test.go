package engine

import (
	"context"
	"testing"
)

// 问句/口语查询应走向量加权分支，精确词/标识符查询保持均衡。
func TestIsConversationalQuery(t *testing.T) {
	conversational := []string{
		"数据库连接串的环境变量叫什么",
		"怎么在本机把平台跑起来",
		"默认端口是多少",
		"kb_search 工具怎么用",
		"搭建 example 测试环境需要谁审批",
		"为什么镜像删除不掉",
		"默认端口是多少？",
	}
	for _, q := range conversational {
		if !IsConversationalQuery(q) {
			t.Fatalf("问句应识别为 conversational: %q", q)
		}
	}
	exact := []string{
		"ngram",
		"AGENTFLOW_MYSQL_DSN",
		"ResetInstance 操作超时告警",
		"DescribeInstanceFirewallRules",
		"CLS 日志主题登记",
	}
	for _, q := range exact {
		if IsConversationalQuery(q) {
			t.Fatalf("精确词不应识别为 conversational: %q", q)
		}
	}
}

func TestWeightsForQuery(t *testing.T) {
	conv := WeightsForQuery("怎么在本机把平台跑起来")
	exact := WeightsForQuery("AGENTFLOW_MYSQL_DSN")
	if conv.VecRank <= exact.VecRank {
		t.Fatalf("问句向量权重应高于精确查询: conv=%+v exact=%+v", conv, exact)
	}
	if conv.LexScore >= exact.LexScore {
		t.Fatalf("问句词法分数加成分应更低: conv=%+v exact=%+v", conv, exact)
	}
	// 精确查询保持早期等权口径，不改动已验证行为。
	if exact.VecRank != 1.0 || exact.LexRank != 1.0 || exact.LexScore != 0.1 {
		t.Fatalf("非问句应保持默认权重: %+v", exact)
	}
}

// Fuse 与 FuseWithWeights(生产权重) 对同一查询必须完全一致，避免「调参用一套、
// 上线用另一套」。
func TestFuseMatchesWeightsForQuery(t *testing.T) {
	kw := []Hit{
		{Source: "s", Path: "api/sync.md", StartLine: 1, EndLine: 2, Text: "同步配置", Score: 5},
		{Source: "s", Path: "proposals/a.md", StartLine: 1, EndLine: 2, Text: "方案", Score: 3},
	}
	vec := []Hit{
		{Source: "s", Path: "deployment.md", StartLine: 1, EndLine: 2, Text: "部署与端口", Score: 0.9},
		{Source: "s", Path: "proposals/a.md", StartLine: 1, EndLine: 2, Text: "方案", Score: 0.8},
	}
	q := "怎么在本机把平台跑起来"
	got := Fuse(q, kw, vec, 10)
	want := FuseWithWeights(kw, vec, 10, WeightsForQuery(q))
	if len(got) != len(want) {
		t.Fatalf("长度不一致: %d vs %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Path != want[i].Path || got[i].Score != want[i].Score {
			t.Fatalf("第 %d 项不一致: got=%+v want=%+v", i, got[i], want[i])
		}
	}
}

// 核心目标：对问句，混合排序应采纳「向量通道排第一、关键词通道里缺席」的
// 正确文档；这正是走查第 1 条的现象——关键词把一堆噪声排在前面，而向量把
// 部署文档排到了第一。用等权旧权重时噪声会赢，用问句权重时部署文档会赢。
func TestConversationalFusionAdoptsVectorRanking(t *testing.T) {
	// 关键词通道：与查询字面沾边但无关的噪声（走查里排在前面的那些）。
	kw := []Hit{
		{Source: "s", Path: "conversation-workspaces.md", StartLine: 1, EndLine: 2, Text: "工作区", Score: 5},
		{Source: "s", Path: "proposals/a.md", StartLine: 1, EndLine: 2, Text: "方案", Score: 4},
		{Source: "s", Path: "proposals/b.md", StartLine: 1, EndLine: 2, Text: "方案2", Score: 3},
	}
	// 向量通道：语义命中部署文档（关键词通道里没有它）。同时补足候选
	// 分布，让纯向量的 deployment.md 能通过融合层的相对相关度门槛。
	vec := []Hit{
		{Source: "s", Path: "deployment.md", StartLine: 10, EndLine: 12, Text: "端口与 DSN", Score: 0.95},
		{Source: "s", Path: "api/orchestration.md", StartLine: 1, EndLine: 2, Text: "编排", Score: 0.55},
		{Source: "s", Path: "other/guide-a.md", StartLine: 1, EndLine: 2, Text: "其他文档", Score: 0.54},
		{Source: "s", Path: "other/guide-b.md", StartLine: 1, EndLine: 2, Text: "其他文档二", Score: 0.53},
		{Source: "s", Path: "other/guide-c.md", StartLine: 1, EndLine: 2, Text: "其他文档三", Score: 0.52},
		{Source: "s", Path: "other/guide-d.md", StartLine: 1, EndLine: 2, Text: "其他文档四", Score: 0.51},
		{Source: "s", Path: "other/guide-e.md", StartLine: 1, EndLine: 2, Text: "其他文档五", Score: 0.50},
		{Source: "s", Path: "other/guide-f.md", StartLine: 1, EndLine: 2, Text: "其他文档六", Score: 0.49},
		{Source: "s", Path: "other/guide-g.md", StartLine: 1, EndLine: 2, Text: "其他文档七", Score: 0.48},
		{Source: "s", Path: "other/guide-h.md", StartLine: 1, EndLine: 2, Text: "其他文档八", Score: 0.47},
	}

	question := "怎么在本机把平台跑起来"
	conv := Fuse(question, kw, vec, 10)
	if len(conv) == 0 || conv[0].Path != "deployment.md" {
		t.Fatalf("问句混合应把向量首选 deployment.md 排第一: %+v", paths(conv))
	}
	// 对比：等权旧权重下噪声靠词法分数加成占先——这正是要修的行为。
	equal := FuseWithWeights(kw, vec, 10, FusionWeights{LexRank: 1, VecRank: 1, LexScore: 0.1})
	if len(equal) == 0 || equal[0].Path == "deployment.md" {
		t.Fatalf("等权旧权重下部署文档不应排第一（证明是调权带来改善）: %+v", paths(equal))
	}

	// 精确标识符查询：默认等权，关键词强命中不被向量压过。
	exactKW := []Hit{
		{Source: "s", Path: "deployment.md", StartLine: 1, EndLine: 2, Text: "AGENTFLOW_MYSQL_DSN 定义", Score: 8},
		{Source: "s", Path: "api/sync.md", StartLine: 1, EndLine: 2, Text: "无关", Score: 1},
	}
	exactVec := []Hit{
		{Source: "s", Path: "api/sync.md", StartLine: 1, EndLine: 2, Text: "无关", Score: 0.9},
		{Source: "s", Path: "deployment.md", StartLine: 1, EndLine: 2, Text: "AGENTFLOW_MYSQL_DSN 定义", Score: 0.5},
	}
	exact := Fuse("AGENTFLOW_MYSQL_DSN", exactKW, exactVec, 10)
	if len(exact) == 0 || exact[0].Path != "deployment.md" {
		t.Fatalf("精确查询关键词强命中应排第一: %+v", paths(exact))
	}
}

// 经 HybridRetriever 时，问句的融合层同样采纳向量改善。
func TestHybridRetrieverQuestionUsesConversationalWeights(t *testing.T) {
	kw := &fakeIndex{hits: []Hit{
		{Source: "s", Path: "conversation-workspaces.md", StartLine: 1, EndLine: 2, Text: "工作区", Score: 5},
		{Source: "s", Path: "proposals/a.md", StartLine: 1, EndLine: 2, Text: "方案", Score: 4},
	}}
	vec := &fakeVectorChannel{active: true, hits: []Hit{
		{Source: "s", Path: "deployment.md", StartLine: 10, EndLine: 12, Text: "端口", Score: 0.95},
		{Source: "s", Path: "api/orchestration.md", StartLine: 1, EndLine: 2, Text: "编排", Score: 0.55},
		{Source: "s", Path: "other/guide-a.md", StartLine: 1, EndLine: 2, Text: "其他文档", Score: 0.54},
		{Source: "s", Path: "other/guide-b.md", StartLine: 1, EndLine: 2, Text: "其他文档二", Score: 0.53},
		{Source: "s", Path: "other/guide-c.md", StartLine: 1, EndLine: 2, Text: "其他文档三", Score: 0.52},
		{Source: "s", Path: "other/guide-d.md", StartLine: 1, EndLine: 2, Text: "其他文档四", Score: 0.51},
		{Source: "s", Path: "other/guide-e.md", StartLine: 1, EndLine: 2, Text: "其他文档五", Score: 0.50},
		{Source: "s", Path: "other/guide-f.md", StartLine: 1, EndLine: 2, Text: "其他文档六", Score: 0.49},
		{Source: "s", Path: "other/guide-g.md", StartLine: 1, EndLine: 2, Text: "其他文档七", Score: 0.48},
		{Source: "s", Path: "other/guide-h.md", StartLine: 1, EndLine: 2, Text: "其他文档八", Score: 0.47},
	}}
	hr := NewHybridRetriever(kw, vec)
	rr, err := hr.Retrieve(context.Background(), "默认端口是多少", []string{"s"}, DefaultSearchLimit)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Mode != "hybrid" || rr.Degraded {
		t.Fatalf("应为 hybrid 未降级: %+v", rr)
	}
	if len(rr.Hits) == 0 || rr.Hits[0].Path != "deployment.md" {
		t.Fatalf("问句混合首选应为 deployment.md: %+v", paths(rr.Hits))
	}
}
