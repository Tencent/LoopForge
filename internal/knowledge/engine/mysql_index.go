package engine

import (
	"context"
	"strings"

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
	// 先去掉问句里的疑问词和语气词，再下推给 ngram 索引。保留空结果而不是
	// 用原句再查一次：问句成分不承载检索意图，重新加回只会带回弱匹配。
	rawQuery := query
	query = normalizeSearchQuery(query)
	if len([]rune(query)) < 2 || len(cjkBigrams(query))+len(latinTokens(query)) == 0 {
		return []Hit{}, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	// 多取候选用于重排；调用方看到的条数仍由 limit 收口。MySQL 在 LIMIT
	// 较大时仍只返回匹配行，代价主要是排序，不引入新表或新索引。
	fetch := rerankDepth
	if limit > fetch {
		fetch = limit
	}
	if fetch > MaxSearchLimit {
		fetch = MaxSearchLimit
	}
	matches, err := m.q.FulltextChunks(ctx, query, sources, fetch)
	if err != nil {
		return nil, err
	}

	// 标识符精确通道是可选的：内容面没实现 BooleanQuerier，或精确查询
	// 失败，都保留自然语言候选继续检索，不把整次搜索打失败。
	var exact []domain.KnowledgeChunkMatch
	strong := strongIdentifiers(query)
	if len(strong) > 0 {
		if boolean, ok := m.q.(BooleanQuerier); ok {
			if rows, err := boolean.BooleanChunks(ctx, strings.Join(strong, " "), sources, fetch); err == nil {
				exact = rows
			}
		}
	}
	return rerank(rawQuery, matches, exact, strong, limit), nil
}
