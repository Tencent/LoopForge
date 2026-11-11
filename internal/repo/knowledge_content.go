package repo

import (
	"context"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 内容面全文检索的条数边界。默认 10 条、上限 50 条；超限由这里收口，
// 调用方（关键词适配器）不必各自实现。
const (
	knowledgeFulltextDefaultLimit = 10
	knowledgeFulltextMaxLimit     = 50
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
	return putKnowledgeDocument(r.db, doc)
}

func putKnowledgeDocument(tx *gorm.DB, doc domain.KnowledgeDocument) error {
	now := time.Now().UTC()
	row := knowledgeDocumentRow{
		SourceID: doc.SourceID, Path: doc.Path, Title: doc.Title, Body: doc.Body,
		LineCount: doc.LineCount, SHA256: doc.SHA256, Commit: doc.Commit,
		Revision: doc.Revision, Updated: &now,
	}
	return tx.Clauses(clause.OnConflict{
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
	return putKnowledgeRevision(r.db, rev)
}

func putKnowledgeRevision(tx *gorm.DB, rev domain.KnowledgeRevision) error {
	created := rev.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	row := knowledgeRevisionRow{
		ID: rev.ID, SourceID: rev.SourceID, Path: rev.Path, Revision: rev.Revision,
		Body: rev.Body, Author: rev.Author, Created: &created,
	}
	return tx.Clauses(clause.OnConflict{
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

// WriteHostedDocument 把托管库一次写入的三件事放进同一个内容库事务：
// 写/覆盖正文、追加修订、替换该文档的索引块。控制面的来源修订号与篇数
// 由调用方在事务成功后单独更新，不与内容库做跨库事务。
func (r *KnowledgeContentRepo) WriteHostedDocument(doc domain.KnowledgeDocument, rev domain.KnowledgeRevision, chunks []domain.KnowledgeChunk) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := putKnowledgeDocument(tx, doc); err != nil {
			return err
		}
		if err := putKnowledgeRevision(tx, rev); err != nil {
			return err
		}
		return replaceKnowledgeChunks(tx, doc.SourceID, doc.Path, chunks)
	})
}

// DeleteKnowledgeSourceContent 删除某个来源在内容面的全部行：索引块、托管
// 修订和正文。控制面来源由 Service 在本方法成功后单独删除，不做级联。
func (r *KnowledgeContentRepo) DeleteKnowledgeSourceContent(sourceID string) error {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return nil
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("source_id = ?", sourceID).Delete(&knowledgeChunkRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", sourceID).Delete(&knowledgeRevisionRow{}).Error; err != nil {
			return err
		}
		return tx.Where("source_id = ?", sourceID).Delete(&knowledgeDocumentRow{}).Error
	})
}

// DeleteKnowledgeDocument 撤回一篇托管文档：正文、修订历史和索引块一起删，
// 检索与读原文随即失效。方法幂等，路径已不存在时也返回成功。
func (r *KnowledgeContentRepo) DeleteKnowledgeDocument(sourceID, path string) error {
	sourceID, path = strings.TrimSpace(sourceID), strings.TrimSpace(path)
	if sourceID == "" || path == "" {
		return nil
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("source_id = ? AND path = ?", sourceID, path).Delete(&knowledgeChunkRow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ? AND path = ?", sourceID, path).Delete(&knowledgeRevisionRow{}).Error; err != nil {
			return err
		}
		return tx.Where("source_id = ? AND path = ?", sourceID, path).Delete(&knowledgeDocumentRow{}).Error
	})
}

// ReplaceKnowledgeChunks 用新的索引块整体替换某篇文档的块，覆盖后旧正文不再命中。
func (r *KnowledgeContentRepo) ReplaceKnowledgeChunks(sourceID, path string, chunks []domain.KnowledgeChunk) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return replaceKnowledgeChunks(tx, sourceID, path, chunks)
	})
}

func replaceKnowledgeChunks(tx *gorm.DB, sourceID, path string, chunks []domain.KnowledgeChunk) error {
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

// chunkMatchRow 是全文查询的单行结果，字段与 knowledge_chunks 列对齐。
type chunkMatchRow struct {
	ID        int64   `gorm:"column:id"`
	SourceID  string  `gorm:"column:source_id"`
	Path      string  `gorm:"column:path"`
	Idx       int     `gorm:"column:idx"`
	Heading   string  `gorm:"column:heading"`
	Title     string  `gorm:"column:title"`
	StartLine int     `gorm:"column:start_line"`
	EndLine   int     `gorm:"column:end_line"`
	Commit    string  `gorm:"column:commit"`
	Revision  int64   `gorm:"column:revision"`
	Text      string  `gorm:"column:text"`
	Score     float64 `gorm:"column:score"`
}

// FulltextChunks 是默认的关键词检索：对 (title, heading, text) 的 ngram
// 全文索引跑 NATURAL LANGUAGE MODE，只查 sourceIDs 里的来源，按相关度降序，
// 条数默认 10、上限 50。空查询或空来源集合直接返回空，不查库。查询词与
// 来源 id 都走绑定参数，不拼进 SQL。全文索引由 026 脚本补建，gorm 的增量
// DDL 建不出带 ngram 解析器的索引，因此缺索引的环境须先执行 026。
func (r *KnowledgeContentRepo) FulltextChunks(ctx context.Context, query string, sourceIDs []string, limit int) ([]domain.KnowledgeChunkMatch, error) {
	return r.fulltextChunks(ctx, query, sourceIDs, limit, false)
}

// BooleanChunks 是关键词适配器的可选精确通道，对同一全文索引跑 BOOLEAN
// MODE。query 由适配器从用户查询里抽出的英文标识符拼成，只含字母、数字和
// 下划线；这里仍走绑定参数。精确通道失败由适配器降级处理，不影响默认检索。
func (r *KnowledgeContentRepo) BooleanChunks(ctx context.Context, query string, sourceIDs []string, limit int) ([]domain.KnowledgeChunkMatch, error) {
	return r.fulltextChunks(ctx, query, sourceIDs, limit, true)
}

func (r *KnowledgeContentRepo) fulltextChunks(ctx context.Context, query string, sourceIDs []string, limit int, boolean bool) ([]domain.KnowledgeChunkMatch, error) {
	out := make([]domain.KnowledgeChunkMatch, 0)
	query = strings.TrimSpace(query)
	if query == "" || len(sourceIDs) == 0 {
		return out, nil
	}
	if limit <= 0 {
		limit = knowledgeFulltextDefaultLimit
	}
	if limit > knowledgeFulltextMaxLimit {
		limit = knowledgeFulltextMaxLimit
	}
	mode := "NATURAL LANGUAGE MODE"
	if boolean {
		mode = "BOOLEAN MODE"
	}

	// 手动拼 IN 占位符：占位符个数只由来源数量决定，值全部走绑定参数。
	placeholders := strings.TrimRight(strings.Repeat("?,", len(sourceIDs)), ",")
	args := make([]any, 0, len(sourceIDs)+3)
	args = append(args, query) // SELECT 里的 AGAINST
	for _, id := range sourceIDs {
		args = append(args, id)
	}
	args = append(args, query) // WHERE 里的 AGAINST
	args = append(args, limit)

	statement := "SELECT id, source_id, path, idx, heading, title, start_line, end_line, `commit`, revision, `text`, " +
		"MATCH(title, heading, `text`) AGAINST (? IN " + mode + ") AS score " +
		"FROM knowledge_chunks " +
		"WHERE source_id IN (" + placeholders + ") " +
		"AND MATCH(title, heading, `text`) AGAINST (? IN " + mode + ") " +
		"ORDER BY score DESC LIMIT ?"

	var rows []chunkMatchRow
	if err := r.db.WithContext(ctx).Raw(statement, args...).Scan(&rows).Error; err != nil {
		return out, err
	}
	for _, row := range rows {
		out = append(out, domain.KnowledgeChunkMatch{
			Chunk: domain.KnowledgeChunk{
				ID: row.ID, SourceID: row.SourceID, Path: row.Path, Idx: row.Idx,
				Heading: row.Heading, Title: row.Title, StartLine: row.StartLine, EndLine: row.EndLine,
				Commit: row.Commit, Revision: row.Revision, Text: row.Text,
			},
			Score: row.Score,
		})
	}
	return out, nil
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
