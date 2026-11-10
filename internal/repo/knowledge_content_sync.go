package repo

import (
	"agentflow/internal/domain"
	"context"
	"gorm.io/gorm"
)

// ReplaceGitKnowledge publishes a complete source snapshot atomically in the
// content database. No control database connection or revision rows are used.
func (r *KnowledgeContentRepo) ReplaceGitKnowledge(ctx context.Context, source string, docs []domain.KnowledgeDocument, chunks []domain.KnowledgeChunk) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("source_id = ?", source).Delete(&knowledgeChunkRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", source).Delete(&knowledgeDocumentRow{}).Error; err != nil {
			return err
		}
		for _, doc := range docs {
			if err := putKnowledgeDocument(tx, doc); err != nil {
				return err
			}
		}
		byPath := map[string][]domain.KnowledgeChunk{}
		for _, c := range chunks {
			byPath[c.Path] = append(byPath[c.Path], c)
		}
		for p, cs := range byPath {
			if err := replaceKnowledgeChunks(tx, source, p, cs); err != nil {
				return err
			}
		}
		return nil
	})
}
