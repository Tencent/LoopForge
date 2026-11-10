// Package engine 是内置知识引擎，knowledge.Upstream 的唯一实现。
// 分块、frontmatter 校验、关键词检索都在模块内部；调用方只看到五个
// Upstream 方法。关键词检索是模块的内部替换点，换适配器不改 Upstream 和表。
package engine

import "context"

const (
	// DefaultSearchLimit 是调用方不指定条数时的默认召回条数。
	DefaultSearchLimit = 10
	// MaxSearchLimit 是关键词查询的条数上限。
	MaxSearchLimit = 50
)

// SearchOptions 配置一次关键词查询。留成可扩展的结构体，是为了让以后
// 的重排、标识符走 BOOLEAN、导航页降权等能力加进 KeywordIndex 实现里，
// 而不必改 knowledge.Upstream 的签名或 knowledge_chunks 的表结构。
type SearchOptions struct {
	Limit int
}

// Hit 是关键词适配器的一次命中。召回与打分由适配器负责，引擎把 Hit
// 映射成工具的 JSON 契约（出处、摘要、行号）。
type Hit struct {
	Source    string
	Commit    string
	Path      string
	Title     string
	Heading   string
	StartLine int
	EndLine   int
	Text      string
	Score     float64
}

// KeywordIndex 是引擎内部的检索替换点。生产默认接 MySQL ngram 适配器，
// 备选是内存倒排。更换实现不影响 knowledge.Upstream 与内容表。
type KeywordIndex interface {
	Search(ctx context.Context, query string, sources []string, opts SearchOptions) ([]Hit, error)
}
