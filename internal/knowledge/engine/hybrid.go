package engine

import (
	"context"
	"sort"
	"strings"
)

// 混合检索融合层，位于关键词适配器「之上一層」：
//   - KeywordIndex（MySQL ngram / 内存倒排）始终执行，是开源默认、也是降级
//     兜底，本层不改动它的任何内部实现。
//   - VectorChannel 可选。向量通道失败时降级为关键词并在结果里标记 Degraded。
//   - 两者都成功时用「查询自适应加权 RRF」融合：问句/口语查询字面匹配弱、
//     语义意图强，抬高向量通道权重并压低词法分数加成，让向量的语义排序能被
//     混合结果采纳；精确词/短语/标识符查询保持两路均衡，保护关键词强项。
//
// 融合只消费 Hit 的公共字段（出处与行号）作为身份，不要求关键词适配器暴露
// 块 ID，因此替换关键词适配器实现不影响本层。

const (
	// hybridRRFK 是 reciprocal rank fusion 的平滑常数。
	hybridRRFK = 60.0

	// 非问句（精确词/短语/标识符）权重：与早期实现一致，两路等量、词法分数
	// 保留小权重加成，避免改变这类查询已验证的排序。
	defaultVecRankWeight  = 1.0
	defaultLexScoreWeight = 0.1

	// 问句/口语权重：词法分数加成(最高 0.1)相对单个 RRF 名次项(≈0.016)过大，
	// 会把混合排序钉死在关键词顺序上、淹没向量信号。离线网格调参(见
	// results-hybrid/fusion-tuned.md)显示：问句去掉该加成(0)、并把向量名次
	// 权重抬到 2.0，走查四句的混合排序即采纳向量的改善，且 31 条查询混合
	// R@10 与 MRR 反升。非问句保持默认权重不变。
	conversationalVecRankWeight  = 2.0
	conversationalLexScoreWeight = 0.0

	// vectorWindow / keywordWindow 是两路召回窗口，量级保持一致。
	vectorWindow  = 50
	keywordWindow = 50
	// degradeReason 只给安全枚举文案，不回传底层 error（可能含 URL/凭据细节）。
	degradeReason = "语义检索暂不可用，已降级为关键词检索"
)

// conversationalMarkers 判定「问句/口语」查询。只收中文疑问词、句末语气词和
// 明确口语问法；不含普通技术词，避免把标识符/短语查询误判。与关键词适配器
// 的 normalizeSearchQuery（去疑问词）是两回事：这里只做只读判定，不改查询。
var conversationalMarkers = []string{
	"怎么", "怎样", "如何", "为什么", "为何", "什么", "啥", "多少", "几", "哪", "谁",
	"叫什么", "怎么办", "咋", "是不是", "能不能", "可不可以", "有没有", "要不要",
	"吗", "呢", "？", "?",
}

// IsConversationalQuery 判断查询是否为问句/口语。导出供调参与自检使用，
// 保证「离线调参判定的问句集合」与「生产融合实际生效的问句集合」完全一致。
func IsConversationalQuery(query string) bool {
	for _, m := range conversationalMarkers {
		if strings.Contains(query, m) {
			return true
		}
	}
	return false
}

// FusionWeights 是加权 RRF 两路的权重。导出它是为了让融合权重可测、可调：
// 生产 Fuse 用 weightsForQuery 选一组，离线调参可以传不同组直接复用同一段
// 融合代码，避免「调参用一套公式、上线用另一套」。
type FusionWeights struct {
	LexRank  float64 // 关键词 RRF 名次权重
	VecRank  float64 // 向量 RRF 名次权重
	LexScore float64 // 归一化关键词分数加成
}

// WeightsForQuery 暴露融合层对查询选用的权重，供调参与自检观察「问句/口语」
// 是否走了向量加权分支。生产 Fuse 直接复用它，行为单一来源。
func WeightsForQuery(query string) FusionWeights {
	if IsConversationalQuery(query) {
		return FusionWeights{LexRank: 1.0, VecRank: conversationalVecRankWeight, LexScore: conversationalLexScoreWeight}
	}
	return FusionWeights{LexRank: 1.0, VecRank: defaultVecRankWeight, LexScore: defaultLexScoreWeight}
}

// RetrievalResult 是一次检索的结果，附带实际生效的通道与降级标记。
type RetrievalResult struct {
	Hits     []Hit
	Mode     string // lexical / hybrid
	Degraded bool
	Reason   string
}

// Retriever 是关键词适配器之上的检索替换点。
type Retriever interface {
	Retrieve(ctx context.Context, query string, sources []string, limit int) (RetrievalResult, error)
}

// VectorChannel 是可选语义检索通道。Active 做轻量的本地判定（配置是否就绪、
// 给定来源里是否至少有一个启用了向量且索引契约一致），不发起网络调用；
// 真正的可用性由 Search 返回错误后降级处理。
type VectorChannel interface {
	Active(ctx context.Context, sources []string) bool
	Search(ctx context.Context, query string, sources []string, limit int) ([]Hit, error)
}

// HybridRetriever 组合关键词适配器与可选向量通道。vec 为 nil 或 Active() 为
// false 时，行为与纯关键词检索完全一致。
type HybridRetriever struct {
	kw  KeywordIndex
	vec VectorChannel
}

func NewHybridRetriever(kw KeywordIndex, vec VectorChannel) *HybridRetriever {
	return &HybridRetriever{kw: kw, vec: vec}
}

var _ Retriever = (*HybridRetriever)(nil)

func (h *HybridRetriever) Retrieve(ctx context.Context, query string, sources []string, limit int) (RetrievalResult, error) {
	active := h.vec != nil && h.vec.Active(ctx, sources)
	// 向量未启用时严格保持原调用（limit 条数不变），只在启用混合时放宽关键词
	// 召回窗口供 RRF 融合；输出条数仍由 limit 收口。
	kwLimit := limit
	if active {
		kwLimit = keywordWindow
	}
	kwHits, err := h.kw.Search(ctx, query, sources, SearchOptions{Limit: kwLimit})
	if err != nil {
		return RetrievalResult{}, err
	}
	if !active {
		return RetrievalResult{Hits: kwHits, Mode: "lexical"}, nil
	}
	vecHits, err := h.vec.Search(ctx, query, sources, vectorWindow)
	if err != nil {
		// 向量通道故障：降级为关键词，结果里标记，不把整次搜索打失败。
		return RetrievalResult{Hits: kwHits, Mode: "lexical", Degraded: true, Reason: degradeReason}, nil
	}
	return RetrievalResult{Hits: Fuse(query, kwHits, vecHits, limit), Mode: "hybrid"}, nil
}

// hitKey 是一个索引块的天然身份（来源+路径+起止行）。两路命中据此合并，
// 不依赖块 ID，也不依赖正文是否逐字相同。
type hitKey struct {
	source, path string
	start, end   int
}

func keyOf(h Hit) hitKey {
	return hitKey{source: h.Source, path: h.Path, start: h.StartLine, end: h.EndLine}
}

// Fuse 用查询自适应加权 RRF 融合关键词与向量两路命中，取 top limit。
// 权重由 weightsForQuery(query) 给出：问句/口语抬高向量权重、压低词法分数
// 加成；其余查询保持均衡。query 为空时按非问句处理（与早期 Fuse 等价）。
func Fuse(query string, kwHits, vecHits []Hit, limit int) []Hit {
	return FuseWithWeights(kwHits, vecHits, limit, WeightsForQuery(query))
}

// FuseWithWeights 用显式权重做加权 RRF 融合，是 Fuse 的可调核心。生产与离线
// 调参共用这一段，保证调出来的权重上线后行为一致。
func FuseWithWeights(kwHits, vecHits []Hit, limit int, w FusionWeights) []Hit {
	meta := make(map[hitKey]Hit, len(kwHits)+len(vecHits))
	lexRank := make(map[hitKey]int, len(kwHits))
	lexScore := make(map[hitKey]float64, len(kwHits))
	vecRank := make(map[hitKey]int, len(vecHits))
	maxLex := 0.0
	for i, h := range kwHits {
		k := keyOf(h)
		if _, ok := meta[k]; !ok {
			meta[k] = h
		}
		lexRank[k] = i + 1
		lexScore[k] = h.Score
		if h.Score > maxLex {
			maxLex = h.Score
		}
	}
	for i, h := range vecHits {
		k := keyOf(h)
		if _, ok := meta[k]; !ok {
			meta[k] = h // 纯向量命中：出处/摘要来自向量库载荷
		}
		vecRank[k] = i + 1
	}

	type agg struct {
		score float64
		key   hitKey
	}
	aggs := make([]agg, 0, len(meta))
	for k := range meta {
		s := 0.0
		if r, ok := lexRank[k]; ok {
			s += w.LexRank / (hybridRRFK + float64(r))
		}
		if r, ok := vecRank[k]; ok {
			s += w.VecRank / (hybridRRFK + float64(r))
		}
		if maxLex > 0 {
			if ls, ok := lexScore[k]; ok {
				s += w.LexScore * ls / maxLex
			}
		}
		aggs = append(aggs, agg{score: s, key: k})
	}
	sort.Slice(aggs, func(i, j int) bool {
		if aggs[i].score != aggs[j].score {
			return aggs[i].score > aggs[j].score
		}
		a, b := aggs[i].key, aggs[j].key
		if a.source != b.source {
			return a.source < b.source
		}
		if a.path != b.path {
			return a.path < b.path
		}
		if a.start != b.start {
			return a.start < b.start
		}
		return a.end < b.end
	})
	if limit > 0 && len(aggs) > limit {
		aggs = aggs[:limit]
	}
	out := make([]Hit, 0, len(aggs))
	for _, a := range aggs {
		h := meta[a.key]
		h.Score = a.score
		out = append(out, h)
	}
	return out
}
