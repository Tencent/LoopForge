package repo

import (
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// KnowledgeContentRepo 拥有内置知识引擎的内容面：文档正文、托管修订与索引块。
// 它不嵌入 deps，自己持有 *gorm.DB：默认与主库共用同一个连接池，配置了
// AGENTFLOW_KNOWLEDGE_MYSQL_DSN 且指向不同库时才另开连接。内容行只用
// source_id 字符串指向控制面，不建跨组外键。
type KnowledgeContentRepo struct {
	db       *gorm.DB
	separate bool
}

func (r *KnowledgeContentRepo) PutKnowledgeDocument(doc domain.KnowledgeDocument) error {
	now := time.Now().UTC()
	row := knowledgeDocumentRow{
		SourceID: doc.SourceID, Path: doc.Path, Title: doc.Title, Body: doc.Body,
		LineCount: doc.LineCount, SHA256: doc.SHA256, Commit: doc.Commit,
		Revision: doc.Revision, Updated: &now,
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_id"}, {Name: "path"}},
		DoUpdates: clause.AssignmentColumns([]string{"title", "body", "line_count", "sha256", "commit", "revision", "updated_at"}),
	}).Create(&row).Error
}

func (r *KnowledgeContentRepo) KnowledgeDocument(sourceID, path string) (domain.KnowledgeDocument, bool) {
	var row knowledgeDocumentRow
	if err := r.db.Where("source_id = ? AND path = ?", sourceID, path).Take(&row).Error; err != nil {
		return domain.KnowledgeDocument{}, false
	}
	return knowledgeDocumentFromRow(row), true
}

func (r *KnowledgeContentRepo) KnowledgeDocuments(sourceID string) []domain.KnowledgeDocument {
	out := make([]domain.KnowledgeDocument, 0)
	var rows []knowledgeDocumentRow
	if err := r.db.Where("source_id = ?", sourceID).Order("path").Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeDocumentFromRow(row))
	}
	return out
}

func (r *KnowledgeContentRepo) PutKnowledgeRevision(rev domain.KnowledgeRevision) error {
	created := rev.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	row := knowledgeRevisionRow{
		ID: rev.ID, SourceID: rev.SourceID, Path: rev.Path, Revision: rev.Revision,
		Body: rev.Body, Author: rev.Author, Created: &created,
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"revision", "body", "author"}),
	}).Create(&row).Error
}

func (r *KnowledgeContentRepo) KnowledgeRevisions(sourceID, path string) []domain.KnowledgeRevision {
	out := make([]domain.KnowledgeRevision, 0)
	var rows []knowledgeRevisionRow
	if err := r.db.Where("source_id = ? AND path = ?", sourceID, path).Order("revision").Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeRevisionFromRow(row))
	}
	return out
}

// ReplaceKnowledgeChunks 用新的索引块整体替换某篇文档的块，覆盖后旧正文不再命中。
func (r *KnowledgeContentRepo) ReplaceKnowledgeChunks(sourceID, path string, chunks []domain.KnowledgeChunk) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("source_id = ? AND path = ?", sourceID, path).Delete(&knowledgeChunkRow{}).Error; err != nil {
			return err
		}
		if len(chunks) == 0 {
			return nil
		}
		rows := make([]knowledgeChunkRow, 0, len(chunks))
		for _, ch := range chunks {
			rows = append(rows, knowledgeChunkRow{
				SourceID: sourceID, Path: path, Idx: ch.Idx,
				Heading: ch.Heading, Title: ch.Title, StartLine: ch.StartLine, EndLine: ch.EndLine,
				Commit: ch.Commit, Revision: ch.Revision, Text: ch.Text,
			})
		}
		return tx.Create(&rows).Error
	})
}

func (r *KnowledgeContentRepo) KnowledgeChunks(sourceID, path string) []domain.KnowledgeChunk {
	out := make([]domain.KnowledgeChunk, 0)
	var rows []knowledgeChunkRow
	if err := r.db.Where("source_id = ? AND path = ?", sourceID, path).Order("idx").Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeChunkFromRow(row))
	}
	return out
}

func knowledgeDocumentFromRow(row knowledgeDocumentRow) domain.KnowledgeDocument {
	out := domain.KnowledgeDocument{
		SourceID: row.SourceID, Path: row.Path, Title: row.Title, Body: row.Body,
		LineCount: row.LineCount, SHA256: row.SHA256, Commit: row.Commit, Revision: row.Revision,
	}
	if row.Updated != nil {
		out.UpdatedAt = *row.Updated
	}
	return out
}

func knowledgeRevisionFromRow(row knowledgeRevisionRow) domain.KnowledgeRevision {
	out := domain.KnowledgeRevision{
		ID: row.ID, SourceID: row.SourceID, Path: row.Path, Revision: row.Revision,
		Body: row.Body, Author: row.Author,
	}
	if row.Created != nil {
		out.CreatedAt = *row.Created
	}
	return out
}

func knowledgeChunkFromRow(row knowledgeChunkRow) domain.KnowledgeChunk {
	return domain.KnowledgeChunk{
		ID: row.ID, SourceID: row.SourceID, Path: row.Path, Idx: row.Idx,
		Heading: row.Heading, Title: row.Title, StartLine: row.StartLine, EndLine: row.EndLine,
		Commit: row.Commit, Revision: row.Revision, Text: row.Text,
	}
}
