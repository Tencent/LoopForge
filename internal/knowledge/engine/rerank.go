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
	identifierHitWeight = 1.5
	// lengthPenaltyExponent 用块长做 BM25 式长度惩罚，抑制 8k 长块。
	lengthPenaltyExponent = 0.3
	// navigationPenalty 只降权目录/导航页，不把它们排除出候选。
	navigationPenalty = 0.3
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
	applyIdentifierCoverage(ordered, latin)

	for _, c := range ordered {
		c.score = c.rerankScore()
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		return candidateLess(ordered[i], ordered[j])
	})

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
	score := base * (1 + titleHitWeight*c.titleCoverage + headingHitWeight*c.headingCoverage + identifierHitWeight*c.identifierCoverage)
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
	type termWeight struct {
		title   float64
		heading float64
	}
	n := float64(len(candidates))
	weights := make(map[string]termWeight, len(terms))
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
		w := termWeight{
			title:   math.Log(1 + n/float64(1+titleDF)),
			heading: math.Log(1 + n/float64(1+headingDF)),
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

// applyIdentifierCoverage 统计查询里的英文标识符有多少在块内完整出现。
// 这是对已召回候选的加权，不会扩大召回范围。
func applyIdentifierCoverage(candidates []*rerankCandidate, tokens []string) {
	if len(tokens) == 0 {
		return
	}
	for _, c := range candidates {
		hit := 0
		for _, token := range tokens {
			if strings.Contains(c.titleLower, token) || strings.Contains(c.headingLower, token) || strings.Contains(c.bodyLower, token) {
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
