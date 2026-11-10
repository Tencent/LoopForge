package engine

import (
	"context"

	"agentflow/internal/domain"
)

// FulltextQuerier 执行内容面的 ngram 全文查询。*repo.Store 通过内嵌的
// KnowledgeContentRepo 满足它，所以引擎只依赖这个小接口，不依赖 repo 包。
type FulltextQuerier interface {
	FulltextChunks(ctx context.Context, query string, sourceIDs []string, limit int) ([]domain.KnowledgeChunkMatch, error)
}

// NewMySQLKeywordIndex 构造默认适配器：MySQL ngram 全文索引的自然语言模式，
// 只在调用方传入的来源里检索，条数默认 10、上限 50。
func NewMySQLKeywordIndex(q FulltextQuerier) KeywordIndex {
	return &mysqlKeywordIndex{q: q}
}

type mysqlKeywordIndex struct {
	q FulltextQuerier
}

func (m *mysqlKeywordIndex) Search(ctx context.Context, query string, sources []string, opts SearchOptions) ([]Hit, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	matches, err := m.q.FulltextChunks(ctx, query, sources, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Hit, 0, len(matches))
	for _, match := range matches {
		c := match.Chunk
		out = append(out, Hit{
			Source: c.SourceID, Commit: c.Commit, Path: c.Path,
			Title: c.Title, Heading: c.Heading,
			StartLine: c.StartLine, EndLine: c.EndLine,
			Text: c.Text, Score: match.Score,
		})
	}
	return out, nil
}
