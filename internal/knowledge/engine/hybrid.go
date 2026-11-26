package engine

import (
	"context"
	"math"
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
// 融合以出处与行号标识命中。关键词适配器可以提供短语覆盖证据，无需共享
// 内部的块 ID。

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

	// 纯向量命中的相对相关度门槛。关键词适配器已经有 relevantCandidates，
	// 但融合层不经过那段重排；这里用同一套「候选分布相对判断」补上，避免
	// 关键词判空时把 Qdrant 的 50 个近邻全部当结果，也避免为凑满条数保留
	// 纯向量长尾。余弦相似度的分布比关键词分数平，因此头部比值门槛低于
	// 关键词重排的 relevanceGateRatio；比值和离群度必须同时成立。
	vectorHeadRatio     = 1.20
	vectorHeadMAD       = 3.0
	vectorKeepRatio     = 0.85
	vectorCandidateMAD  = 2.0
	vectorMinCandidates = 10
	// 关键词完全判空时没有第二路证据，纯向量批次必须更陡才算强：否则
	// 小语料里一个短块的余弦离群值也会被当成命中。关键词有命中时保持
	// 较低比值门槛，避免误伤「关键词只覆盖一部分、向量补足语义」的查询。
	vectorHeadRatioNoKeyword = 1.50

	// keywordHeadRatio 判断关键词第一名是否明显领先第二名。低于它说明
	// 关键词通道自己的名次也不确定；此时若向量批次也没有明显头部，两路
	// 名次都不可靠，融合层用关键词自身的相关度分数打破噪声，而不是让
	// 一个偶然靠前的语义近邻把关键词首选挤掉。关键词头部明确时保持
	// 现有权重，保留向量对错别字/口语查询的纠错能力。
	keywordHeadRatio = 1.2

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
// 融合前会过滤掉没有关键词覆盖且向量相对分数弱的纯向量命中；结果条数
// 只由真实命中决定，不补齐到 limit。
func Fuse(query string, kwHits, vecHits []Hit, limit int) []Hit {
	return FuseWithWeights(kwHits, vecHits, limit, WeightsForQuery(query))
}

// FuseWithWeights 用显式权重做加权 RRF 融合，是 Fuse 的可调核心。生产与离线
// 调参共用这一段，保证调出来的权重上线后行为一致。纯向量命中必须通过
// vectorRelevance 的相对门槛：既有明显高于候选池中位数的头部，单个命中
// 也要靠近头部并显著高于中位数；关键词零覆盖且向量弱的结果不进入结果集。
func FuseWithWeights(kwHits, vecHits []Hit, limit int, w FusionWeights) []Hit {
	meta := make(map[hitKey]Hit, len(kwHits)+len(vecHits))
	lexRank := make(map[hitKey]int, len(kwHits))
	lexScore := make(map[hitKey]float64, len(kwHits))
	vecRank := make(map[hitKey]int, len(vecHits))
	vecScore := make(map[hitKey]float64, len(vecHits))
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
		if _, ok := vecScore[k]; !ok {
			vecScore[k] = h.Score
		}
	}
	headRatio := vectorHeadRatio
	if len(kwHits) == 0 {
		headRatio = vectorHeadRatioNoKeyword
	}
	vectorGate := assessVectorRelevance(vecHits, headRatio)
	// 向量头部不明确时，用词法分数打破弱名次信号。关键词首选有足够的
	// 短语覆盖时，即使分差刚越过头部阈值也保留该证据。缺少短语证据的
	// 关键词强首选仍可被向量纠错，例如查询有错别字时。
	if w.LexScore < defaultLexScoreWeight && !vectorGate.strong && len(kwHits) > 0 &&
		(kwHits[0].intentCoverage >= relevanceStrongCoverage ||
			len(kwHits) > 1 && kwHits[0].Score < keywordHeadRatio*kwHits[1].Score) {
		w.LexScore = defaultLexScoreWeight
	}

	type agg struct {
		score float64
		key   hitKey
	}
	aggs := make([]agg, 0, len(meta))
	for k := range meta {
		// 有任一关键词命中就保留；纯向量命中要过相对相关度门槛。
		if _, keywordCovered := lexRank[k]; !keywordCovered {
			if !vectorGate.accepts(vecScore[k]) {
				continue
			}
		}
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

// vectorRelevance 是向量候选分布的相对门槛。它不依赖固定语料或模型分值的
// 绝对大小，只用本批候选中位数、MAD 和第一名的相对差判断「有没有相关度
// 头部」；候选数少于 vectorMinCandidates 时无法估计分布，按弱批次处理。
type vectorRelevance struct {
	usable bool
	strong bool
	top    float64
	median float64
	scale  float64
}

func assessVectorRelevance(hits []Hit, headRatio float64) vectorRelevance {
	if len(hits) < vectorMinCandidates {
		return vectorRelevance{}
	}
	scores := make([]float64, 0, len(hits))
	for _, h := range hits {
		scores = append(scores, h.Score)
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(scores)))
	top := scores[0]
	if top <= 0 {
		return vectorRelevance{}
	}
	rest := scores[1:]
	median := medianScore(rest)
	scale := math.Max(medianAbsoluteDeviation(rest, median), math.Abs(median)*0.05)
	v := vectorRelevance{usable: true, top: top, median: median, scale: scale}
	ratio := math.Inf(1)
	if median > 0 {
		ratio = top / median
	}
	v.strong = ratio >= headRatio && v.z(top) >= vectorHeadMAD
	return v
}

func (v vectorRelevance) z(score float64) float64 {
	if v.scale <= 0 {
		if score > v.median {
			return math.Inf(1)
		}
		return 0
	}
	return (score - v.median) / v.scale
}

// accepts 判断一个纯向量命中能否进入融合结果：批次要有明显头部，命中
// 还要靠近第一名并显著高于候选池中位数，避免用长尾凑满 10 条。
func (v vectorRelevance) accepts(score float64) bool {
	if !v.usable || !v.strong {
		return false
	}
	return score >= v.top*vectorKeepRatio && v.z(score) >= vectorCandidateMAD
}
