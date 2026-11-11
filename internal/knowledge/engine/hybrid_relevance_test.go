package engine

import (
	"testing"
)

// 走查 P1-1：关键词判空时，向量通道即使返回 50 个邻居，只要分数分布没有
// 明显头部，就不应把长尾当成结果。分数是余弦相似度，相对判断，不写死语料
// 或模型的绝对阈值。
func TestFuseDropsWeakPureVectorBatchWhenKeywordEmpty(t *testing.T) {
	vec := []Hit{
		pureVectorHit("validation.md", 0.419),
		pureVectorHit("proposals/a.md", 0.416),
		pureVectorHit("api/sync.md", 0.414),
		pureVectorHit("deployment.md", 0.411),
		pureVectorHit("token-pricing.md", 0.409),
	}
	if got := Fuse("a", nil, vec, 10); len(got) != 0 {
		t.Fatalf("关键词为空且向量批次分数接近时不应返回结果: %+v", paths(got))
	}
}

// 关键词为空、向量只有一个候选时，没有候选分布可判断强弱；这种纯向量
// 命中不能仅凭“排第一”进入结果集，否则覆盖后的旧正文词仍会命中。
func TestFuseDropsSingleWeakPureVectorHit(t *testing.T) {
	vec := []Hit{pureVectorHit("final-walkthrough/overwrite-check.md", 0.4286)}
	if got := Fuse("菠萝蜜甲", nil, vec, 10); len(got) != 0 {
		t.Fatalf("单个弱向量命中不应进入结果集: %+v", paths(got))
	}
}

// 关键词通道有真实命中时保留关键词结果；同一批次里向量头部不明显，
// 不能再用纯向量长尾把条数凑满。
func TestFuseKeepsKeywordHitsAndDropsWeakVectorTails(t *testing.T) {
	kw := []Hit{
		keywordHit("deployment.md", 3.2, "AGENTFLOW_MYSQL_DSN 是数据库连接串的环境变量"),
	}
	vec := []Hit{
		pureVectorHit("proposals/a.md", 0.512),
		pureVectorHit("proposals/b.md", 0.509),
		pureVectorHit("api/sync.md", 0.506),
		pureVectorHit("conversation-workspaces.md", 0.503),
		pureVectorHit("token-pricing.md", 0.500),
	}
	got := Fuse("数据库连接串的环境变量叫什么", kw, vec, 10)
	if len(got) != 1 || got[0].Path != "deployment.md" {
		t.Fatalf("弱向量批次不应补充纯向量长尾: %+v", paths(got))
	}
}

// 向量批次确有明显头部时，只保留头部附近的纯向量命中；不把长尾补齐到 10 条。
func TestFuseStrongVectorBatchKeepsOnlyHead(t *testing.T) {
	vec := []Hit{
		pureVectorHitAt("deployment.md", 1, 2, 0.92),
		pureVectorHitAt("deployment.md", 10, 12, 0.90),
		pureVectorHit("proposals/a.md", 0.40),
		pureVectorHit("proposals/b.md", 0.39),
		pureVectorHit("api/sync.md", 0.38),
		pureVectorHit("api/registry.md", 0.37),
		pureVectorHit("guides/a.md", 0.36),
		pureVectorHit("guides/b.md", 0.35),
		pureVectorHit("guides/c.md", 0.34),
		pureVectorHit("guides/d.md", 0.33),
	}
	got := Fuse("AGENTFLOW_MYSQL_DSN", nil, vec, 10)
	if len(got) != 2 {
		t.Fatalf("强向量批次应只保留头部两个候选，实际 %d: %+v", len(got), paths(got))
	}
	for _, h := range got {
		if h.Path != "deployment.md" {
			t.Fatalf("长尾纯向量命中不应进入结果: %+v", paths(got))
		}
	}
}

// 融合后不为凑满 limit 补长尾：关键词两条真实命中，弱向量长尾不补充，结果就是两条。
func TestFuseDoesNotPadToLimit(t *testing.T) {
	kw := []Hit{
		keywordHit("deployment.md", 4.0, "部署文档"),
		keywordHit("api/sync.md", 2.0, "同步接口"),
	}
	vec := []Hit{
		pureVectorHit("deployment.md", 0.93),
		pureVectorHit("api/sync.md", 0.91),
		pureVectorHit("proposals/a.md", 0.52),
		pureVectorHit("proposals/b.md", 0.51),
		pureVectorHit("token-pricing.md", 0.50),
	}
	got := Fuse("怎么在本机把平台跑起来", kw, vec, 10)
	if len(got) != 2 {
		t.Fatalf("结果应只有两路真实命中，不应补齐: %+v", paths(got))
	}
}

func pureVectorHit(path string, score float64) Hit {
	return pureVectorHitAt(path, 1, 2, score)
}

func pureVectorHitAt(path string, start, end int, score float64) Hit {
	return Hit{Source: "s", Path: path, StartLine: start, EndLine: end, Text: "向量候选正文", Score: score}
}

func keywordHit(path string, score float64, text string) Hit {
	return Hit{Source: "s", Path: path, StartLine: 1, EndLine: 2, Text: text, Score: score}
}

// 两路都不可靠时（向量批次没有明显头部，关键词前两名分数也接近），
// 用关键词自身的相关度分数打破名次噪声，避免语义近邻把关键词首选挤掉。
// 走查口语问句「数据库连接串的环境变量叫什么」属于这种情况。
func TestFuseWeakBothChannelsProtectsKeywordHeadScore(t *testing.T) {
	kw := []Hit{
		keywordHit("deployment.md", 0.654, "AGENTFLOW_MYSQL_DSN 是数据库连接串的环境变量"),
		keywordHit("bot-sop-controls.md", 0.645, "操作返回 job 仅表示受理"),
		keywordHit("api/registry.md", 0.50, "接口注册"),
		keywordHit("conversation-workspaces.md", 0.40, "工作区"),
		keywordHit("proposals/frontend-preview-delivery-lifecycle.md", 0.331, "前端预览"),
	}
	vec := []Hit{
		pureVectorHit("other/guide-a.md", 0.500),
		pureVectorHit("proposals/frontend-preview-delivery-lifecycle.md", 0.495),
		pureVectorHit("other/guide-b.md", 0.490),
		pureVectorHit("other/guide-c.md", 0.485),
		pureVectorHit("other/guide-d.md", 0.480),
	}
	got := Fuse("数据库连接串的环境变量叫什么", kw, vec, 10)
	if len(got) == 0 || got[0].Path != "deployment.md" {
		t.Fatalf("两路都不确定时应由关键词分数把部署文档排在首位: %+v", paths(got))
	}
}

// 关键词头部明确时不要用分数重排：错别字查询「测试环竟怎么搭建」的首选
// 是无关块，向量才能把正确块救回来。这个用例锁住不把上面那条规则扩大化。
func TestFuseDominantKeywordHeadKeepsVectorRescue(t *testing.T) {
	kw := []Hit{
		keywordHit("noise.md", 0.746, "无关词命中的长文档"),
		keywordHit("deployment.md", 0.410, "部署文档"),
		keywordHit("proposals/frontend-preview-delivery-lifecycle.md", 0.250, "前端预览"),
	}
	vec := []Hit{
		pureVectorHit("proposals/frontend-preview-delivery-lifecycle.md", 0.50),
		pureVectorHit("other/guide-a.md", 0.495),
		pureVectorHit("other/guide-b.md", 0.490),
		pureVectorHit("other/guide-c.md", 0.485),
		pureVectorHit("other/guide-d.md", 0.480),
	}
	got := Fuse("测试环竟怎么搭建", kw, vec, 10)
	if len(got) == 0 || got[0].Path != "proposals/frontend-preview-delivery-lifecycle.md" {
		t.Fatalf("关键词头部明确时不应压过向量的纠错排序: %+v", paths(got))
	}
}

// 关键词完全判空时没有第二路证据，纯向量批次要更陡才放行；有关键词命中时
// 同一批次可以补充头部纯向量命中。这个差异防止小语料里单个短块的余弦
// 离群值在无关查询下被当成命中。
func TestFuseNoKeywordRequiresStrongerVectorHead(t *testing.T) {
	vec := []Hit{
		pureVectorHit("validation.md", 0.392),
		pureVectorHit("proposals/a.md", 0.323),
		pureVectorHit("api/sync.md", 0.311),
		pureVectorHit("deployment.md", 0.281),
		pureVectorHit("token-pricing.md", 0.280),
		pureVectorHit("guides/a.md", 0.279),
		pureVectorHit("guides/b.md", 0.278),
		pureVectorHit("guides/c.md", 0.277),
		pureVectorHit("guides/d.md", 0.276),
		pureVectorHit("guides/e.md", 0.275),
	}
	if got := Fuse("量子纠缠香蕉皮运输规范", nil, vec, 10); len(got) != 0 {
		t.Fatalf("关键词判空时中等向量头部不应放行: %+v", paths(got))
	}
	kw := []Hit{keywordHit("deployment.md", 2.0, "部署文档")}
	got := Fuse("数据库连接串的环境变量叫什么", kw, vec, 10)
	if len(got) != 2 || got[0].Path != "deployment.md" {
		t.Fatalf("有关键词命中时应保留关键词结果与向量头部: %+v", paths(got))
	}
}
