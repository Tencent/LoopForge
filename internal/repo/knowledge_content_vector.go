package repo

import (
	"agentflow/internal/domain"
)

// 向量索引只从内容面（MySQL）读取索引块：向量库是缓存，事实源是正文切出的
// 块。全量重建、增量更新都走这些只读方法，不读 Qdrant 作为输入。

// KnowledgeChunksBySource 返回一个来源的全部索引块（含自增 ID，用作向量
// 点 ID）。按 (path, idx) 稳定排序。
func (r *KnowledgeContentRepo) KnowledgeChunksBySource(sourceID string) []domain.KnowledgeChunk {
	out := make([]domain.KnowledgeChunk, 0)
	var rows []knowledgeChunkRow
	if err := r.db.Where("source_id = ?", sourceID).Order("path, idx, id").Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeChunkFromRow(row))
	}
	return out
}

// KnowledgeChunksForSources 返回多个来源的全部索引块，供全量重建一次读出。
func (r *KnowledgeContentRepo) KnowledgeChunksForSources(sourceIDs []string) []domain.KnowledgeChunk {
	out := make([]domain.KnowledgeChunk, 0)
	if len(sourceIDs) == 0 {
		return out
	}
	var rows []knowledgeChunkRow
	if err := r.db.Where("source_id IN ?", sourceIDs).Order("source_id, path, idx, id").Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeChunkFromRow(row))
	}
	return out
}
