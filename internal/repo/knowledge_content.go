package repo

import (
	knowledge "agentflow/internal/knowledge/publication"
	"context"
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge/engine/frontmatter"

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

// PutKnowledgeSubmissionContent stages a review body on the content plane. The
// control plane only keeps metadata and the fingerprint; the body is upserted
// by submission ID so re-saving an item does not duplicate rows.
func (r *KnowledgeContentRepo) PutKnowledgeSubmissionContent(submissionID, body string) error {
	submissionID = strings.TrimSpace(submissionID)
	if submissionID == "" {
		return errors.New("knowledge submission content requires a submission id")
	}
	now := time.Now().UTC()
	row := knowledgeSubmissionContentRow{SubmissionID: submissionID, Body: body, Updated: &now}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "submission_id"}, {Name: "version_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"body", "updated_at"}),
	}).Create(&row).Error
}

func (r *KnowledgeContentRepo) KnowledgeSubmissionContent(submissionID string) (string, bool) {
	submissionID = strings.TrimSpace(submissionID)
	if submissionID == "" {
		return "", false
	}
	var row knowledgeSubmissionContentRow
	if err := r.db.Where("submission_id = ? AND version_key = ?", submissionID, "").Take(&row).Error; err != nil {
		return "", false
	}
	return row.Body, true
}

func (r *KnowledgeContentRepo) DeleteKnowledgeSubmissionContent(submissionID string) error {
	submissionID = strings.TrimSpace(submissionID)
	if submissionID == "" {
		return nil
	}
	return r.db.Where("submission_id = ?", submissionID).Delete(&knowledgeSubmissionContentRow{}).Error
}

// maxKnowledgeLabelRunes matches the VARCHAR(512) label columns. A generated
// heading can be longer than a database label; truncating the display/search
// label keeps the full body and chunk text intact instead of failing the whole
// source sync on MySQL's strict mode.
const maxKnowledgeLabelRunes = 500

func clipKnowledgeLabel(value string) string {
	runes := []rune(value)
	if len(runes) <= maxKnowledgeLabelRunes {
		return value
	}
	return string(runes[:maxKnowledgeLabelRunes])
}

func knowledgeDocumentRecord(doc domain.KnowledgeDocument) knowledgeDocumentRow {
	now := time.Now().UTC()
	// 元数据在写入索引内容时解析并落列。没有 frontmatter 或字段缺失时保持
	// 零值，由健康检查报告；托管文档不会被自动补 frontmatter。
	meta, _ := frontmatter.Parse([]byte(doc.Body))
	row := knowledgeDocumentRow{
		SourceID: doc.SourceID, Path: doc.Path, Title: clipKnowledgeLabel(doc.Title), Body: doc.Body,
		LineCount: doc.LineCount, SHA256: doc.SHA256, Commit: doc.Commit,
		Revision: doc.Revision, Owner: meta.Owner, DocStatus: meta.Status,
		FrontmatterUpdated: meta.Updated, Updated: &now,
	}
	return row
}

func putKnowledgeDocument(tx *gorm.DB, doc domain.KnowledgeDocument) error {
	row := knowledgeDocumentRecord(doc)
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "source_id"}, {Name: "path"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"title", "body", "line_count", "sha256", "commit", "revision",
			"owner", "doc_status", "frontmatter_updated_at", "updated_at",
		}),
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

// KnowledgeDocumentPage reads one path-ordered page of document bodies. kb_grep
// uses it so the time/byte budget is checked while pages arrive instead of
// materializing an entire source (and its bodies) before the first check.
func (r *KnowledgeContentRepo) KnowledgeDocumentPage(sourceID, afterPath string, limit int) []domain.KnowledgeDocument {
	out := make([]domain.KnowledgeDocument, 0)
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return out
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := r.db.Where("source_id = ?", sourceID)
	if afterPath != "" {
		q = q.Where("path > ?", afterPath)
	}
	var rows []knowledgeDocumentRow
	if err := q.Order("path").Limit(limit).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out = append(out, knowledgeDocumentFromRow(row))
	}
	return out
}

// KnowledgeDocumentMetas 按来源批量读取检索后处理需要的治理元数据，不返回
// 正文。为兼容 035 之前写入的历史行，元数据列为空时再读一次正文并解析；
// 这是读时兜底，不修改数据，下一次覆盖写入会把元数据落到列上。
func (r *KnowledgeContentRepo) KnowledgeDocumentMetas(sourceIDs []string) []domain.KnowledgeDocumentMeta {
	out := make([]domain.KnowledgeDocumentMeta, 0)
	sourceIDs = cleanKnowledgeIDs(sourceIDs)
	if len(sourceIDs) == 0 {
		return out
	}
	var rows []knowledgeDocumentRow
	if err := r.db.Select("source_id, path, title, owner, doc_status, frontmatter_updated_at, line_count, `commit`, revision, updated_at").
		Where("source_id IN ?", sourceIDs).Order("source_id, path").Find(&rows).Error; err != nil {
		return out
	}
	missing := map[string][]string{}
	for _, row := range rows {
		if row.Owner == "" && row.DocStatus == "" && row.FrontmatterUpdated == nil {
			missing[row.SourceID] = append(missing[row.SourceID], row.Path)
		}
	}
	bodies := map[string]string{}
	for sourceID, paths := range missing {
		for start := 0; start < len(paths); start += 500 {
			end := start + 500
			if end > len(paths) {
				end = len(paths)
			}
			var bodyRows []knowledgeDocumentRow
			if err := r.db.Select("path, body").Where("source_id = ? AND path IN ?", sourceID, paths[start:end]).Find(&bodyRows).Error; err != nil {
				continue
			}
			for _, bodyRow := range bodyRows {
				bodies[lifecycleDocumentKey(sourceID, bodyRow.Path)] = bodyRow.Body
			}
		}
	}
	for _, row := range rows {
		if row.Owner == "" && row.DocStatus == "" && row.FrontmatterUpdated == nil {
			meta, _ := frontmatter.Parse([]byte(bodies[lifecycleDocumentKey(row.SourceID, row.Path)]))
			row.Owner, row.DocStatus, row.FrontmatterUpdated = meta.Owner, meta.Status, meta.Updated
		}
		out = append(out, knowledgeDocumentMetaFromRow(row))
	}
	return out
}

func lifecycleDocumentKey(sourceID, path string) string { return sourceID + "\x00" + path }

func cleanKnowledgeIDs(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
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
	return r.WriteHostedDocumentContext(context.Background(), doc, rev, chunks)
}
func (r *KnowledgeContentRepo) WriteHostedDocumentContext(ctx context.Context, doc domain.KnowledgeDocument, rev domain.KnowledgeRevision, chunks []domain.KnowledgeChunk) error {
	return r.contentTransaction(ctx, doc.SourceID, doc.Path, false, func(tx *gorm.DB) error {
		row := knowledgeDocumentRecord(doc)
		var result *gorm.DB
		if doc.Revision == 1 {
			result = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		} else {
			result = tx.Model(&knowledgeDocumentRow{}).Where("source_id = ? AND path = ? AND revision = ?", doc.SourceID, doc.Path, doc.Revision-1).
				Select("title", "body", "line_count", "sha256", "commit", "revision", "owner", "doc_status", "frontmatter_updated_at", "updated_at").Updates(&row)
		}
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrKnowledgeRevisionConflict
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
	return r.DeleteKnowledgeSourceContentContext(context.Background(), sourceID)
}
func (r *KnowledgeContentRepo) DeleteKnowledgeSourceContentContext(ctx context.Context, sourceID string) error {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return nil
	}
	return r.contentTransaction(ctx, sourceID, "", false, func(tx *gorm.DB) error {
		if err := tx.Where("source_id = ? AND path <> ''", sourceID).Delete(&knowledgeContentFenceRow{}).Error; err != nil {
			return err
		}
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
	return r.contentTransaction(context.Background(), sourceID, path, true, func(tx *gorm.DB) error {
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
			Heading: clipKnowledgeLabel(ch.Heading), Title: clipKnowledgeLabel(ch.Title), StartLine: ch.StartLine, EndLine: ch.EndLine,
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
// 来源 id 都走绑定参数，不拼进 SQL。全文索引由 028 脚本补建，gorm 的增量
// DDL 建不出带 ngram 解析器的索引，因此缺索引的环境须先执行 028。
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
		Owner: row.Owner, Status: row.DocStatus, FrontmatterUpdated: row.FrontmatterUpdated,
	}
	if out.Owner == "" && out.Status == "" && out.FrontmatterUpdated == nil {
		// 035 之前写入的历史行没有元数据列；读时从正文解析，不写回。
		meta, _ := frontmatter.Parse([]byte(row.Body))
		out.Owner, out.Status, out.FrontmatterUpdated = meta.Owner, meta.Status, meta.Updated
	}
	if row.Updated != nil {
		out.UpdatedAt = *row.Updated
	}
	return out
}

func knowledgeDocumentMetaFromRow(row knowledgeDocumentRow) domain.KnowledgeDocumentMeta {
	out := domain.KnowledgeDocumentMeta{
		SourceID: row.SourceID, Path: row.Path, Title: row.Title,
		Owner: row.Owner, Status: row.DocStatus, FrontmatterUpdated: row.FrontmatterUpdated,
		LineCount: row.LineCount, Commit: row.Commit, Revision: row.Revision,
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

// ErrKnowledgeRevisionConflict asks a hosted writer to reread and retry its CAS.
var ErrKnowledgeRevisionConflict = knowledge.ErrRevisionConflict

func (r *KnowledgeContentRepo) contentTransaction(ctx context.Context, source, path string, retract bool, write func(*gorm.DB) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fence := knowledgeContentFenceRow{SourceID: source, Path: path}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&fence).Error; err != nil {
			return err
		}
		// A no-op UPDATE locks only the content fence. Lease renewals use a different row/connection.
		if err := tx.Model(&knowledgeContentFenceRow{}).Where("source_id = ? AND path = ?", source, path).Update("retracted", gorm.Expr("retracted")).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ? AND path = ?", source, path).Take(&fence).Error; err != nil {
			return err
		}
		incoming := knowledge.FromContext(ctx)
		if incoming.Token > 0 && incoming.Source == source {
			if fence.Token > incoming.Token {
				return errors.New("stale content fencing token")
			}
			if err := tx.Model(&fence).Update("token", incoming.Token).Error; err != nil {
				return err
			}
		}
		generation := knowledge.GenerationFrom(ctx)
		if !retract && path != "" && generation.Set && generation.Source == source && generation.Path == path {
			if generation.Value != fence.Token {
				return ErrKnowledgeRevisionConflict
			}
			if fence.Retracted {
				if err := tx.Model(&fence).Update("retracted", false).Error; err != nil {
					return err
				}
			}
		} else if fence.Retracted && !retract {
			return errors.New("document has been retracted")
		}
		if retract {
			if err := tx.Model(&fence).Updates(map[string]any{"retracted": true, "token": gorm.Expr("token + 1")}).Error; err != nil {
				return err
			}
		}
		if err := write(tx); err != nil {
			return err
		}
		return ctx.Err()
	})
}

func (r *KnowledgeContentRepo) KnowledgeDocumentGeneration(source, path string) (int64, error) {
	var fence knowledgeContentFenceRow
	err := r.db.Where("source_id = ? AND path = ?", source, path).Take(&fence).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return fence.Token, err
}

// Staging bodies are immutable versions. A failed control CAS can leave an
// unreferenced version, but can never replace the body selected by the winner.
func (r *KnowledgeContentRepo) PutKnowledgeSubmissionContentVersion(id, key, body string) error {
	now := time.Now().UTC()
	row := knowledgeSubmissionContentRow{SubmissionID: id, VersionKey: key, Body: body, Updated: &now}
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}
func (r *KnowledgeContentRepo) KnowledgeSubmissionContentVersion(id, key string) (string, bool) {
	var row knowledgeSubmissionContentRow
	if err := r.db.Where("submission_id = ? AND version_key = ?", id, key).Take(&row).Error; err != nil {
		return "", false
	}
	return row.Body, true
}
