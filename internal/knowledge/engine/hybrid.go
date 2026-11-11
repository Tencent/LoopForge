package engine

import (
	"context"
	"sort"
)

// 混合检索融合层，位于关键词适配器「之上一層」：
//   - KeywordIndex（MySQL ngram / 内存倒排）始终执行，是开源默认、也是降级
//     兜底，本层不改动它的任何内部实现。
//   - VectorChannel 可选。向量通道失败时降级为关键词并在结果里标记 Degraded。
//   - 两者都成功时用 RRF 融合，并给关键词侧的相关度分数一点小权重。
//
// 融合只消费 Hit 的公共字段（出处与行号）作为身份，不要求关键词适配器暴露
// 块 ID，因此替换关键词适配器实现不影响本层。

const (
	// hybridRRFK 是 reciprocal rank fusion 的平滑常数。
	hybridRRFK = 60.0
	// hybridLexScoreWeight 是归一化关键词分数在融合分里的小权重，使 TF 信号
	// 可观但不压过 RRF 主体。
	hybridLexScoreWeight = 0.1
	// vectorWindow / keywordWindow 是两路召回窗口，量级保持一致。
	vectorWindow  = 50
	keywordWindow = 50
	// degradeReason 只给安全枚举文案，不回传底层 error（可能含 URL/凭据细节）。
	degradeReason = "语义检索暂不可用，已降级为关键词检索"
)

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
	return RetrievalResult{Hits: Fuse(kwHits, vecHits, limit), Mode: "hybrid"}, nil
}

// hitKey 是一个索引块的天然身份（来源+路径+起止行）。两路命中据此合并，
// 不依赖块 ID，也不依赖正文是否逐字相同。
type hitKey struct {
	source, path   string
	start, end     int
}

func keyOf(h Hit) hitKey {
	return hitKey{source: h.Source, path: h.Path, start: h.StartLine, end: h.EndLine}
}

// Fuse 用 RRF 融合关键词与向量两路命中，取 top limit。关键词侧的原始分数按
// 本次查询内最大值归一后加一点小权重；纯关键词命中在降级路径中 rank 与
// lexScore 同序，加权不改变相对顺序。
func Fuse(kwHits, vecHits []Hit, limit int) []Hit {
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
			s += 1.0 / (hybridRRFK + float64(r))
		}
		if r, ok := vecRank[k]; ok {
			s += 1.0 / (hybridRRFK + float64(r))
		}
		if maxLex > 0 {
			if ls, ok := lexScore[k]; ok {
				s += hybridLexScoreWeight * ls / maxLex
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
