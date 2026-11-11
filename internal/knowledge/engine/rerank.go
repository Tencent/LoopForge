package engine

import (
	"context"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"agentflow/internal/domain"
)

// 重排参数。MySQL ngram 全文索引只给出“词频式”相关度，没有长度归一化，
// 目录/导航页又天然短、链接密。适配器多取一些候选，再用下面几项通用信号
// 重排；这些参数按本机实测选定，不依赖某个语料的具体路径。
const (
	// rerankDepth 是每个通道取回的候选数。上游 limit 仍是 10/50，只影响输出。
	rerankDepth = 50
	// rrfK 是 reciprocal rank fusion 的平滑常数，抑制相邻名次的分数跳变。
	rrfK = 60.0
	// booleanChannelWeight 是标识符精确通道在融合分里的权重。
	booleanChannelWeight = 0.8
	// titleHitWeight / headingHitWeight 是查询词命中标题与小节的加权。
	titleHitWeight   = 3.0
	headingHitWeight = 0.2
	// identifierHitWeight 是查询里的英文标识符在块内完整出现的加权。
	identifierHitWeight = 2.2
	// lengthPenaltyExponent 用块长做 BM25 式长度惩罚，抑制 8k 长块。
	lengthPenaltyExponent = 0.3
	// navigationPenalty 只降权目录/导航页，不把它们排除出候选。
	navigationPenalty = 0.3
	// phraseHitWeight 是查询里的连续短语在块内完整出现的加权。只对至少
	// 3 个连续汉字生效，避免“平台”“环境”这类常见词普涨。
	phraseHitWeight = 1.5
	// relevanceKeepRatio 只保留分数不低于第一名这个比例的候选，避免在
	// 真实命中后面再凑满 10 条弱匹配。
	relevanceKeepRatio = 0.02
	// relevanceGateRatio / relevanceGateMAD 判断第一名是否明显高于候选
	// 池中位数：达不到时说明查询只命中了常见片段，整批结果按空处理。
	relevanceGateRatio = 2.0
	relevanceGateMAD   = 3.0
	// relevanceStrongCoverage 是“查询词大部分都在块内”的覆盖度；达到
	// 这个比例时不再要求分数显著高于中位数。
	relevanceStrongCoverage = 0.5
	// Coverage includes absent terms; this is a ratio, not a corpus score cutoff.
	relevanceMinQueryCoverage = 0.2
	intentCoverageWeight      = 3.0
	intentIdentifierWeight    = 3.0
	// termCoverageWeight 把查询词在正文里的覆盖度并入重排分。标题/小节
	// 没命中时，正文覆盖更多查询词的块应排在只命中一个常见片段的块前。
	termCoverageWeight = 0.5
)

// BooleanQuerier 是 FulltextQuerier 的可选扩展：用 BOOLEAN MODE 查应用侧
// 抽出的英文标识符。内容面实现它时，适配器会多走一条精确通道；不实现时
// 自动退回只用自然语言模式。
type BooleanQuerier interface {
	BooleanChunks(ctx context.Context, query string, sourceIDs []string, limit int) ([]domain.KnowledgeChunkMatch, error)
}

// rerankCandidate 是合并两个通道后的一次候选。nlRank/boolRank 都是 0 起始，
// -1 表示该通道没有这条候选。
type rerankCandidate struct {
	hit Hit

	nlRank   int
	boolRank int

	titleLower   string
	headingLower string
	bodyLower    string
	runes        int
	nav          bool

	titleCoverage      float64
	headingCoverage    float64
	identifierCoverage float64
	termCoverage       float64
	phraseHit          bool
	intentCoverage     float64
	queryCoverage      float64

	score float64
}

type candidateKey struct {
	source string
	path   string
	start  int
	end    int
	text   string
}

// rerank 合并自然语言候选和可选的标识符精确候选，再按标题/小节命中、长度、
// 标识符精确出现和导航页降权重新排序。strongIDs 只用于过滤精确通道：
// ngram BOOLEAN 查询仍会把片段相近的块带进来，必须再要求完整标识符出现。
func rerank(query string, nl, exact []domain.KnowledgeChunkMatch, strongIDs []string, limit int) []Hit {
	conversational := IsConversationalQuery(query)
	query = normalizeSearchQuery(query)
	candidates := make(map[candidateKey]*rerankCandidate, len(nl)+len(exact))
	ordered := make([]*rerankCandidate, 0, len(nl)+len(exact))

	add := func(match domain.KnowledgeChunkMatch, boolean bool, rank int) {
		chunk := match.Chunk
		key := candidateKey{chunk.SourceID, chunk.Path, chunk.StartLine, chunk.EndLine, chunk.Text}
		c := candidates[key]
		if c == nil {
			c = newRerankCandidate(chunk)
			candidates[key] = c
			ordered = append(ordered, c)
		}
		if boolean {
			c.boolRank = rank
		} else {
			c.nlRank = rank
		}
	}

	for rank, match := range nl {
		add(match, false, rank)
	}
	for rank, match := range exact {
		if !containsIdentifier(match.Chunk, strongIDs) {
			continue
		}
		add(match, true, rank)
	}

	terms, latin := queryTerms(query)
	applyMetadataCoverage(ordered, terms)
	applyIdentifierCoverage(ordered, latin, conversational)
	phrase := exactPhrase(query)
	coverageTerms := append(append([]string(nil), terms...), latin...)
	applyTermCoverage(ordered, coverageTerms, phrase)
	if conversational {
		applyIntentCoverage(ordered, query)
	}

	for _, c := range ordered {
		c.score = c.rerankScore()
		if c.intentCoverage > 0 {
			c.score += intentIdentifierWeight * c.intentCoverage * c.identifierCoverage
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		return candidateLess(ordered[i], ordered[j])
	})

	ordered = relevantCandidates(ordered)
	if limit <= 0 || limit > len(ordered) {
		limit = len(ordered)
	}
	out := make([]Hit, 0, limit)
	for _, c := range ordered[:limit] {
		hit := c.hit
		hit.Score = c.score
		out = append(out, hit)
	}
	return out
}

func newRerankCandidate(chunk domain.KnowledgeChunk) *rerankCandidate {
	runes := utf8.RuneCountInString(chunk.Text)
	if runes < 1 {
		runes = 1
	}
	return &rerankCandidate{
		hit: Hit{
			Source: chunk.SourceID, Commit: chunk.Commit, Path: chunk.Path,
			Title: chunk.Title, Heading: chunk.Heading,
			StartLine: chunk.StartLine, EndLine: chunk.EndLine, Text: chunk.Text,
		},
		nlRank:       -1,
		boolRank:     -1,
		titleLower:   strings.ToLower(chunk.Title),
		headingLower: strings.ToLower(chunk.Heading),
		bodyLower:    strings.ToLower(chunk.Text),
		runes:        runes,
		nav:          navigationPage(chunk.Path, chunk.Text),
	}
}

// rerankScore 先按 RRF 融合两个通道的名次，再乘标题/小节/标识符加权和
// 长度惩罚；导航页只乘一个降权系数，保留在候选里。
func (c *rerankCandidate) rerankScore() float64 {
	base := 0.0
	if c.nlRank >= 0 {
		base += (rrfK + 1) / (rrfK + float64(c.nlRank+1))
	}
	if c.boolRank >= 0 {
		base += booleanChannelWeight * (rrfK + 1) / (rrfK + float64(c.boolRank+1))
	}
	score := base * (1 + titleHitWeight*c.titleCoverage + headingHitWeight*c.headingCoverage + identifierHitWeight*c.identifierCoverage + termCoverageWeight*c.termCoverage + intentCoverageWeight*c.intentCoverage)
	if c.phraseHit {
		score *= phraseHitWeight
	}
	if c.nav {
		score *= navigationPenalty
	}
	return score / math.Pow(float64(c.runes), lengthPenaltyExponent)
}

func candidateLess(a, b *rerankCandidate) bool {
	if a.nlRank != b.nlRank {
		return rankOrder(a.nlRank) < rankOrder(b.nlRank)
	}
	if a.boolRank != b.boolRank {
		return rankOrder(a.boolRank) < rankOrder(b.boolRank)
	}
	if a.hit.Path != b.hit.Path {
		return a.hit.Path < b.hit.Path
	}
	if a.hit.StartLine != b.hit.StartLine {
		return a.hit.StartLine < b.hit.StartLine
	}
	if a.hit.EndLine != b.hit.EndLine {
		return a.hit.EndLine < b.hit.EndLine
	}
	return a.hit.Text < b.hit.Text
}

func rankOrder(rank int) int {
	if rank < 0 {
		return math.MaxInt
	}
	return rank
}

// queryTerms 把查询拆成中文二字组和拉丁标识符，用作标题/小节覆盖度。
// ngram=2 的索引本身只认二字以上，单个中文字不进拆词结果。
func queryTerms(query string) (cjk, latin []string) {
	cjk = cjkBigrams(query)
	raw := latinTokens(query)
	latin = make([]string, 0, len(raw))
	for _, token := range raw {
		latin = append(latin, strings.ToLower(token))
	}
	sort.Strings(latin)
	return cjk, latin
}

func cjkBigrams(s string) []string {
	seen := make(map[string]bool)
	var out []string
	var run []rune
	flush := func() {
		for i := 0; i+1 < len(run); i++ {
			term := string(run[i : i+2])
			if seen[term] {
				continue
			}
			seen[term] = true
			out = append(out, term)
		}
		run = run[:0]
	}
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			run = append(run, r)
			continue
		}
		if len(run) > 0 {
			flush()
		}
	}
	if len(run) > 0 {
		flush()
	}
	sort.Strings(out)
	return out
}

// latinTokens 抽出 ASCII 字母开头的标识符，去重但保留原始大小写；大小写
// 只用于判定强标识符，匹配与加权在调用处统一转小写。
func latinTokens(s string) []string {
	seen := make(map[string]bool)
	var out []string
	start := -1
	for i, r := range s {
		if isLatinIdentRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			appendLatinToken(&out, seen, s[start:i])
			start = -1
		}
	}
	if start >= 0 {
		appendLatinToken(&out, seen, s[start:])
	}
	sort.Strings(out)
	return out
}

func appendLatinToken(out *[]string, seen map[string]bool, token string) {
	if token == "" {
		return
	}
	r, _ := utf8.DecodeRuneInString(token)
	if !isASCIILetter(r) {
		return
	}
	key := strings.ToLower(token)
	if len(key) < 2 || seen[key] {
		return
	}
	seen[key] = true
	*out = append(*out, token)
}

// strongIdentifiers 是送 BOOLEAN 通道的词：下划线词、CamelCase、全大写缩写、
// 含数字的协议名。普通小写英文词只参与正文精确出现加权，不另开 SQL。
func strongIdentifiers(query string) []string {
	var out []string
	for _, token := range latinTokens(query) {
		if isStrongIdentifier(token) {
			out = append(out, token)
		}
	}
	return out
}

func isStrongIdentifier(token string) bool {
	if len(token) < 2 {
		return false
	}
	var lower, upper, digit, underscore bool
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		case r == '_':
			underscore = true
		}
	}
	if underscore {
		return true
	}
	if lower && upper {
		return true
	}
	if upper && !lower {
		return true
	}
	return digit && (lower || upper)
}

func isLatinIdentRune(r rune) bool {
	return isASCIILetter(r) || (r >= '0' && r <= '9') || r == '_'
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// applyMetadataCoverage 用候选池内的文档频率给查询词加权：在候选标题/小节
// 里越少见的词，命中时权重越高。这样“测试”“环境”这类泛词不会把标题加权
// 变成对所有测试环境文档的普涨。
func applyMetadataCoverage(candidates []*rerankCandidate, terms []string) {
	if len(candidates) == 0 || len(terms) == 0 {
		return
	}
	type termIDF struct {
		title   float64
		heading float64
	}
	n := float64(len(candidates))
	weights := make(map[string]termIDF, len(terms))
	var totalTitle, totalHeading float64
	for _, term := range terms {
		titleDF, headingDF := 0, 0
		for _, c := range candidates {
			if strings.Contains(c.titleLower, term) {
				titleDF++
			}
			if strings.Contains(c.headingLower, term) {
				headingDF++
			}
		}
		weight := termWeight(term)
		w := termIDF{
			title:   weight * math.Log(1+n/float64(1+titleDF)),
			heading: weight * math.Log(1+n/float64(1+headingDF)),
		}
		weights[term] = w
		totalTitle += w.title
		totalHeading += w.heading
	}
	if totalTitle == 0 {
		totalTitle = 1
	}
	if totalHeading == 0 {
		totalHeading = 1
	}
	for _, c := range candidates {
		var titleHit, headingHit float64
		for _, term := range terms {
			w := weights[term]
			if strings.Contains(c.titleLower, term) {
				titleHit += w.title
			}
			if strings.Contains(c.headingLower, term) {
				headingHit += w.heading
			}
		}
		c.titleCoverage = titleHit / totalTitle
		c.headingCoverage = headingHit / totalHeading
	}
}

// applyTermCoverage 统计查询词在标题、小节和正文里的加权覆盖度。虚词
// 二字片段只按 functionTermWeight 计入，因此“环境选择”不会因为一个
// “的环”之类的片段被当成强命中。phrase 是查询里最长连续汉字短语。
func applyTermCoverage(candidates []*rerankCandidate, terms []string, phrase string) {
	if len(candidates) == 0 || len(terms) == 0 {
		return
	}
	n := float64(len(candidates))
	weights := make(map[string]float64, len(terms))
	total := 0.0
	presentTotal := 0.0
	for _, term := range terms {
		df := 0
		for _, c := range candidates {
			if containsTerm(c, term) {
				df++
			}
		}
		weight := termWeight(term) * math.Log(1+n/float64(1+df))
		weights[term] = weight
		total += weight
		if df > 0 {
			presentTotal += weight
		}
	}
	if total == 0 {
		return
	}
	phrase = strings.ToLower(phrase)
	for _, c := range candidates {
		hit := 0.0
		for term, weight := range weights {
			if containsTerm(c, term) {
				hit += weight
			}
		}
		// Keep missing content terms in the gate denominator. The present-term
		// ratio remains the ranking signal for short phrases and synonyms.
		c.queryCoverage = hit / total
		if presentTotal > 0 {
			c.termCoverage = hit / presentTotal
		}
		if len([]rune(phrase)) >= 3 && (strings.Contains(c.titleLower, phrase) || strings.Contains(c.headingLower, phrase) || strings.Contains(c.bodyLower, phrase)) {
			c.phraseHit = true
		}
	}
}

func containsTerm(c *rerankCandidate, term string) bool {
	return strings.Contains(c.titleLower, term) || strings.Contains(c.headingLower, term) || strings.Contains(c.bodyLower, term)
}

// Latin query terms match whole identifier components: ENV must not match
// environment, while DSN still matches AGENTFLOW_MYSQL_DSN.
func containsQueryTerm(text, term string) bool {
	if term == "" {
		return false
	}
	if term[0] < 'a' || term[0] > 'z' {
		return strings.Contains(text, term)
	}
	for offset := 0; offset < len(text); {
		i := strings.Index(text[offset:], term)
		if i < 0 {
			return false
		}
		i += offset
		end := i + len(term)
		word := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' }
		if (i == 0 || !word(text[i-1])) && (end == len(text) || !word(text[end])) {
			return true
		}
		offset = i + 1
	}
	return false
}

// relevantCandidates 按候选分数分布做相对门槛：第一名没有明显高于候选
// 池中位数、并且查询词覆盖度也不高时，说明只是弱片段命中，整批返回空；
// 否则只保留分数接近第一名的候选，不为了凑数保留长尾。
func relevantCandidates(ordered []*rerankCandidate) []*rerankCandidate {
	if len(ordered) == 0 {
		return nil
	}
	maxCoverage := 0.0
	for _, c := range ordered {
		maxCoverage = math.Max(maxCoverage, math.Max(c.queryCoverage, c.intentCoverage))
	}
	// A score outlier cannot rescue a query whose content words are absent.
	if maxCoverage < relevanceMinQueryCoverage {
		return nil
	}
	top := ordered[0]
	if top.termCoverage <= 0 {
		return nil
	}
	if len(ordered) >= 4 && top.termCoverage < relevanceStrongCoverage {
		rest := make([]float64, 0, len(ordered)-1)
		for _, c := range ordered[1:] {
			rest = append(rest, c.score)
		}
		median := medianScore(rest)
		if median > 0 {
			mad := medianAbsoluteDeviation(rest, median)
			denom := math.Max(mad, median*0.05)
			ratio := top.score / median
			z := (top.score - median) / denom
			if ratio < relevanceGateRatio && z < relevanceGateMAD {
				return nil
			}
		}
	}
	out := make([]*rerankCandidate, 0, len(ordered))
	cutoff := top.score * relevanceKeepRatio
	for _, c := range ordered {
		if c.termCoverage <= 0 || c.score < cutoff {
			continue
		}
		out = append(out, c)
	}
	return out
}

func medianScore(scores []float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	cp := append([]float64(nil), scores...)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 0 {
		return (cp[mid-1] + cp[mid]) / 2
	}
	return cp[mid]
}

func medianAbsoluteDeviation(scores []float64, median float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	dev := make([]float64, len(scores))
	for i, s := range scores {
		dev[i] = math.Abs(s - median)
	}
	return medianScore(dev)
}

// applyIdentifierCoverage 统计查询里的英文标识符有多少在块内完整出现。
// 这是对已召回候选的加权，不会扩大召回范围。
func applyIdentifierCoverage(candidates []*rerankCandidate, tokens []string, wholeComponents bool) {
	if len(tokens) == 0 {
		return
	}
	for _, c := range candidates {
		hit := 0
		for _, token := range tokens {
			matched := containsTerm(c, token)
			if wholeComponents {
				matched = containsQueryTerm(c.titleLower, token) || containsQueryTerm(c.headingLower, token) || containsQueryTerm(c.bodyLower, token)
			}
			if matched {
				hit++
			}
		}
		c.identifierCoverage = float64(hit) / float64(len(tokens))
	}
}

// containsIdentifier 判断精确通道候选是否至少含一个完整标识符，避免把
// 只命中片段（例如 kb search 两个词）的块合并进来。
func containsIdentifier(chunk domain.KnowledgeChunk, tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	haystack := strings.ToLower(chunk.Title + "\n" + chunk.Heading + "\n" + chunk.Text)
	for _, token := range tokens {
		if strings.Contains(haystack, strings.ToLower(token)) {
			return true
		}
	}
	return false
}

// navigationPage 识别通用目录/导航页：常见索引文件名、中文目录/索引/导航
// 路径，或链接密度明显偏高的块。命中只降权，不排除。
func navigationPage(path, text string) bool {
	base := path
	if slash := strings.LastIndexByte(base, '/'); slash >= 0 {
		base = base[slash+1:]
	}
	switch strings.ToLower(base) {
	case "readme.md", "index.md", "_index.md", "summary.md", "toc.md", "nav.md", "home.md":
		return true
	}
	if strings.Contains(path, "目录") || strings.Contains(path, "索引") || strings.Contains(path, "导航") {
		return true
	}
	links := strings.Count(text, "](")
	runes := utf8.RuneCountInString(text)
	return links >= 8 && links*20 >= runes
}

// applyIntentCoverage preserves content phrases after removing question language
// and function words. Existing query expansions are alternatives for a phrase,
// but a compound phrase requires all its translated concepts, not just ENV.
func applyIntentCoverage(candidates []*rerankCandidate, query string) {
	clean := stripPhrases(query, questionPhrases)
	parts := strings.FieldsFunc(clean, func(r rune) bool { return !unicode.Is(unicode.Han, r) || functionRunes[r] })
	var phrases []string
	for _, p := range parts {
		if utf8.RuneCountInString(p) >= 3 {
			phrases = append(phrases, p)
		}
	}
	if len(phrases) == 0 {
		return
	}
	weights := make([]float64, len(phrases))
	matches := make([][]bool, len(candidates))
	for i := range matches {
		matches[i] = make([]bool, len(phrases))
	}
	total := 0.0
	for j, p := range phrases {
		df := 0
		for i, c := range candidates {
			match := containsTerm(c, p)
			if !match {
				covered := p
				all := true
				count := 0
				for term, aliases := range queryExpansions {
					if strings.Contains(p, term) {
						count++
						found := false
						for _, a := range aliases {
							if (containsQueryTerm(c.titleLower, strings.ToLower(a)) || containsQueryTerm(c.headingLower, strings.ToLower(a)) || containsQueryTerm(c.bodyLower, strings.ToLower(a))) || containsTerm(c, term) {
								found = true
							}
						}
						all = all && found
						covered = strings.ReplaceAll(covered, term, "")
					}
				}
				match = count > 0 && covered == "" && all
			}
			matches[i][j] = match
			if match {
				df++
			}
		}
		weights[j] = math.Log(1 + float64(len(candidates))/float64(1+df))
		total += weights[j]
	}
	for i, c := range candidates {
		for j, w := range weights {
			if matches[i][j] {
				c.intentCoverage += w / total
			}
		}
	}
}
