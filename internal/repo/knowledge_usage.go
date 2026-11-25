package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RecordKnowledgeUsage atomically increments connector and document invocation
// counters for ownerUserID.
func (r *KnowledgeRepo) RecordKnowledgeUsage(ctx context.Context, ownerUserID int64, delta domain.KnowledgeUsageDelta) error {
	if r == nil || r.DB == nil {
		return nil
	}
	connID := strings.TrimSpace(delta.ConnectorID)
	if connID == "" {
		return errors.New("knowledge connector id is required")
	}
	now := delta.CalledAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	docHits := int64(len(delta.Documents))

	return r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		connRow := knowledgeConnectorUsageRow{
			OwnerUserID:      ownerUserID,
			ConnectorID:      connID,
			CallCount:        1,
			SearchCount:      delta.Search,
			ReadCount:        delta.Read,
			DocumentHitCount: docHits,
			ErrorCount:       delta.Error,
			LastCalledAt:     &now,
			CreatedAt:        &now,
			UpdatedAt:        &now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "owner_user_id"}, {Name: "connector_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"call_count":         gorm.Expr("call_count + 1"),
				"search_count":       gorm.Expr("search_count + ?", delta.Search),
				"read_count":         gorm.Expr("read_count + ?", delta.Read),
				"document_hit_count": gorm.Expr("document_hit_count + ?", docHits),
				"error_count":        gorm.Expr("error_count + ?", delta.Error),
				"last_called_at":     &now,
				"updated_at":         &now,
			}),
		}).Create(&connRow).Error; err != nil {
			return err
		}

		for _, doc := range delta.Documents {
			docID := strings.TrimSpace(doc.DocumentID)
			if docID == "" {
				continue
			}
			sourceID := clipUsageField(doc.SourceID, 191)
			sourceName := clipUsageField(doc.SourceName, 255)
			title := clipUsageField(doc.Title, 255)
			fileName := clipUsageField(doc.FileName, 255)
			docKey := buildKnowledgeDocumentKey(sourceID, docID)

			docRow := knowledgeDocumentUsageRow{
				OwnerUserID:    ownerUserID,
				ConnectorID:    connID,
				DocumentKey:    docKey,
				SourceID:       sourceID,
				SourceName:     sourceName,
				DocumentID:     clipUsageField(docID, 512),
				Title:          title,
				FileName:       fileName,
				CallCount:      1,
				SearchHitCount: doc.SearchHit,
				ReadCount:      doc.Read,
				LastCalledAt:   &now,
				CreatedAt:      &now,
				UpdatedAt:      &now,
			}
			updates := map[string]any{
				"call_count":       gorm.Expr("call_count + 1"),
				"search_hit_count": gorm.Expr("search_hit_count + ?", doc.SearchHit),
				"read_count":       gorm.Expr("read_count + ?", doc.Read),
				"last_called_at":   &now,
				"updated_at":       &now,
			}
			if sourceName != "" {
				updates["source_name"] = sourceName
			}
			if title != "" {
				updates["title"] = title
			}
			if fileName != "" {
				updates["file_name"] = fileName
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "owner_user_id"}, {Name: "connector_id"}, {Name: "document_key"}},
				DoUpdates: clause.Assignments(updates),
			}).Create(&docRow).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListKnowledgeConnectorUsage returns per-connector invocation counts for
// ownerUserID, optionally filtered to connectorID.
func (r *KnowledgeRepo) ListKnowledgeConnectorUsage(ownerUserID int64, connectorID string) []domain.KnowledgeConnectorUsage {
	if r == nil || r.DB == nil {
		return nil
	}
	q := r.DB.Where("owner_user_id = ?", ownerUserID)
	if id := strings.TrimSpace(connectorID); id != "" {
		q = q.Where("connector_id = ?", id)
	}
	var rows []knowledgeConnectorUsageRow
	if err := q.Order("call_count DESC, updated_at DESC, connector_id ASC").Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.KnowledgeConnectorUsage, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.KnowledgeConnectorUsage{
			ConnectorID:      row.ConnectorID,
			CallCount:        row.CallCount,
			SearchCount:      row.SearchCount,
			ReadCount:        row.ReadCount,
			DocumentHitCount: row.DocumentHitCount,
			ErrorCount:       row.ErrorCount,
			LastCalledAt:     row.LastCalledAt,
		})
	}
	return out
}

// ListKnowledgeDocumentUsage returns per-document invocation counts for
// ownerUserID, ordered by call_count DESC.
func (r *KnowledgeRepo) ListKnowledgeDocumentUsage(ownerUserID int64, connectorID string, limit int) []domain.KnowledgeDocumentUsage {
	if r == nil || r.DB == nil {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	q := r.DB.Where("owner_user_id = ?", ownerUserID)
	if id := strings.TrimSpace(connectorID); id != "" {
		q = q.Where("connector_id = ?", id)
	}
	var rows []knowledgeDocumentUsageRow
	if err := q.Order("call_count DESC, last_called_at DESC, document_key ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil
	}
	out := make([]domain.KnowledgeDocumentUsage, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.KnowledgeDocumentUsage{
			ConnectorID:    row.ConnectorID,
			SourceID:       row.SourceID,
			SourceName:     row.SourceName,
			DocumentID:     row.DocumentID,
			Title:          row.Title,
			FileName:       row.FileName,
			CallCount:      row.CallCount,
			SearchHitCount: row.SearchHitCount,
			ReadCount:      row.ReadCount,
			LastCalledAt:   row.LastCalledAt,
		})
	}
	return out
}

// ResetKnowledgeUsage clears usage counters for ownerUserID (scoped to
// connectorID if non-empty).
func (r *KnowledgeRepo) ResetKnowledgeUsage(ownerUserID int64, connectorID string) error {
	if r == nil || r.DB == nil {
		return nil
	}
	connID := strings.TrimSpace(connectorID)
	return r.DB.Transaction(func(tx *gorm.DB) error {
		qConn := tx.Where("owner_user_id = ?", ownerUserID)
		qDoc := tx.Where("owner_user_id = ?", ownerUserID)
		if connID != "" {
			qConn = qConn.Where("connector_id = ?", connID)
			qDoc = qDoc.Where("connector_id = ?", connID)
		}
		if err := qConn.Delete(&knowledgeConnectorUsageRow{}).Error; err != nil {
			return err
		}
		return qDoc.Delete(&knowledgeDocumentUsageRow{}).Error
	})
}

func buildKnowledgeDocumentKey(sourceID, documentID string) string {
	raw := sourceID + "::" + documentID
	if len(raw) <= 255 {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func clipUsageField(s string, maxBytes int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxBytes {
		return s
	}
	// Trim cleanly on UTF-8 rune boundaries.
	out := make([]byte, 0, maxBytes)
	for _, r := range s {
		rb := len(string(r))
		if len(out)+rb > maxBytes {
			break
		}
		out = append(out, string(r)...)
	}
	return string(out)
}
