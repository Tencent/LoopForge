package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	knowledgeUsageEventRetentionDays = 30
	knowledgeUsageEventMaxPerUser    = 2000
	knowledgeTopCallersPerDoc        = 3
	knowledgeUsagePruneMinInterval   = 5 * time.Minute
)

var knowledgeUsagePruneAt sync.Map

type preparedKnowledgeDocDelta struct {
	docKey      string
	sourceID    string
	sourceName  string
	docID       string
	title       string
	fileName    string
	callCount   int64
	searchHit   int64
	readCount   int64
}

func shouldPruneKnowledgeUsageEvents(ownerUserID int64, now time.Time) bool {
	if prev, ok := knowledgeUsagePruneAt.Load(ownerUserID); ok {
		if last, ok := prev.(time.Time); ok && now.Sub(last) < knowledgeUsagePruneMinInterval {
			return false
		}
	}
	knowledgeUsagePruneAt.Store(ownerUserID, now)
	return true
}

// RecordKnowledgeUsage atomically increments connector and document invocation
// counters and appends per-document caller events for ownerUserID.
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

	docMap := make(map[string]*preparedKnowledgeDocDelta, len(delta.Documents))
	var docHits int64
	for _, doc := range delta.Documents {
		docID := strings.TrimSpace(doc.DocumentID)
		if docID == "" {
			continue
		}
		docHits++
		sourceID := clipUsageField(doc.SourceID, 191)
		sourceName := clipUsageField(doc.SourceName, 255)
		title := clipUsageField(doc.Title, 255)
		fileName := clipUsageField(doc.FileName, 255)
		clippedDocID := clipUsageField(docID, 512)
		docKey := buildKnowledgeDocumentKey(sourceID, docID)

		if existing, ok := docMap[docKey]; ok {
			existing.callCount++
			existing.searchHit += doc.SearchHit
			existing.readCount += doc.Read
			if existing.sourceName == "" && sourceName != "" {
				existing.sourceName = sourceName
			}
			if existing.title == "" && title != "" {
				existing.title = title
			}
			if existing.fileName == "" && fileName != "" {
				existing.fileName = fileName
			}
			continue
		}
		docMap[docKey] = &preparedKnowledgeDocDelta{
			docKey:     docKey,
			sourceID:   sourceID,
			sourceName: sourceName,
			docID:      clippedDocID,
			title:      title,
			fileName:   fileName,
			callCount:  1,
			searchHit:  doc.SearchHit,
			readCount:  doc.Read,
		}
	}

	sortedDocs := make([]preparedKnowledgeDocDelta, 0, len(docMap))
	for _, item := range docMap {
		sortedDocs = append(sortedDocs, *item)
	}
	sort.Slice(sortedDocs, func(i, j int) bool {
		return sortedDocs[i].docKey < sortedDocs[j].docKey
	})

	readID := clipUsageField(delta.ReadID, 64)
	tool := clipUsageField(delta.Tool, 128)
	query := clipUsageField(delta.Query, 255)
	caller := normalizeUsageCaller(delta.Caller)

	err := r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

		events := make([]knowledgeUsageEventRow, 0, len(sortedDocs))
		for _, doc := range sortedDocs {
			docRow := knowledgeDocumentUsageRow{
				OwnerUserID:    ownerUserID,
				ConnectorID:    connID,
				DocumentKey:    doc.docKey,
				SourceID:       doc.sourceID,
				SourceName:     doc.sourceName,
				DocumentID:     doc.docID,
				Title:          doc.title,
				FileName:       doc.fileName,
				CallCount:      doc.callCount,
				SearchHitCount: doc.searchHit,
				ReadCount:      doc.readCount,
				CitedCount:     0,
				LastCalledAt:   &now,
				CreatedAt:      &now,
				UpdatedAt:      &now,
			}
			updates := map[string]any{
				"call_count":       gorm.Expr("call_count + ?", doc.callCount),
				"search_hit_count": gorm.Expr("search_hit_count + ?", doc.searchHit),
				"read_count":       gorm.Expr("read_count + ?", doc.readCount),
				"last_called_at":   &now,
				"updated_at":       &now,
			}
			if doc.sourceName != "" {
				updates["source_name"] = doc.sourceName
			}
			if doc.title != "" {
				updates["title"] = doc.title
			}
			if doc.fileName != "" {
				updates["file_name"] = doc.fileName
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "owner_user_id"}, {Name: "connector_id"}, {Name: "document_key"}},
				DoUpdates: clause.Assignments(updates),
			}).Create(&docRow).Error; err != nil {
				return err
			}

			action := domain.KnowledgeUsageActionSearchHit
			if doc.readCount > 0 {
				action = domain.KnowledgeUsageActionRead
			}
			events = append(events, knowledgeUsageEventRow{
				OwnerUserID:  ownerUserID,
				ReadID:       readID,
				ConnectorID:  connID,
				DocumentKey:  doc.docKey,
				SourceID:     doc.sourceID,
				SourceName:   doc.sourceName,
				DocumentID:   doc.docID,
				Title:        doc.title,
				FileName:     doc.fileName,
				Action:       action,
				Tool:         tool,
				Query:        query,
				Cited:        false,
				CallerKind:   caller.Kind,
				WorkflowID:   caller.WorkflowID,
				WorkflowName: caller.WorkflowName,
				RunID:        caller.RunID,
				NodeID:       caller.NodeID,
				NodeName:     caller.NodeName,
				AgentID:      caller.AgentID,
				AgentName:    caller.AgentName,
				SessionID:    caller.SessionID,
				SessionTitle: caller.SessionTitle,
				SpaceID:      caller.SpaceID,
				SpaceTitle:   caller.SpaceTitle,
				IssueID:      caller.IssueID,
				IssueTitle:   caller.IssueTitle,
				ProjectID:    caller.ProjectID,
				CalledAt:     now,
				CreatedAt:    &now,
			})
		}

		if len(events) > 0 {
			if err := tx.Create(&events).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(sortedDocs) > 0 && shouldPruneKnowledgeUsageEvents(ownerUserID, now) {
		_ = pruneKnowledgeUsageEventsTx(r.DB.WithContext(ctx), ownerUserID, now, knowledgeUsageEventRetentionDays, knowledgeUsageEventMaxPerUser)
	}
	return nil
}

func pruneKnowledgeUsageEventsTx(tx *gorm.DB, ownerUserID int64, now time.Time, retentionDays, maxRows int) error {
	if retentionDays > 0 {
		cutoff := now.AddDate(0, 0, -retentionDays)
		if err := tx.Where("owner_user_id = ? AND called_at < ?", ownerUserID, cutoff).
			Delete(&knowledgeUsageEventRow{}).Error; err != nil {
			return err
		}
	}
	if maxRows > 0 {
		var boundary struct {
			ID       int64
			CalledAt time.Time
		}
		err := tx.Model(&knowledgeUsageEventRow{}).
			Select("id, called_at").
			Where("owner_user_id = ?", ownerUserID).
			Order("called_at DESC, id DESC").
			Offset(maxRows).
			Limit(1).
			Take(&boundary).Error
		if err == nil && boundary.ID > 0 {
			if delErr := tx.Where(
				"owner_user_id = ? AND (called_at < ? OR (called_at = ? AND id <= ?))",
				ownerUserID, boundary.CalledAt, boundary.CalledAt, boundary.ID,
			).Delete(&knowledgeUsageEventRow{}).Error; delErr != nil {
				return delErr
			}
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	return nil
}

// MarkKnowledgeUsageCited idempotently marks matching document usage events as
// cited and increments knowledge_document_usage_counts.cited_count accordingly.
func (r *KnowledgeRepo) MarkKnowledgeUsageCited(ctx context.Context, ownerUserID int64, marks []domain.KnowledgeCitedDocMark) error {
	if r == nil || r.DB == nil || len(marks) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, mark := range marks {
			readID := clipUsageField(mark.ReadID, 64)
			connID := strings.TrimSpace(mark.ConnectorID)
			docID := strings.TrimSpace(mark.DocumentID)
			if readID == "" || connID == "" || docID == "" {
				continue
			}
			sourceID := clipUsageField(mark.SourceID, 191)
			docKey := buildKnowledgeDocumentKey(sourceID, docID)

			res := tx.Model(&knowledgeUsageEventRow{}).
				Where("owner_user_id = ? AND read_id = ? AND connector_id = ? AND document_key = ? AND cited = ?",
					ownerUserID, readID, connID, docKey, false).
				Update("cited", true)
			if res.Error != nil {
				return res.Error
			}
			matchedKey := docKey
			if res.RowsAffected == 0 && sourceID != "" {
				fallbackKey := buildKnowledgeDocumentKey("", docID)
				res = tx.Model(&knowledgeUsageEventRow{}).
					Where("owner_user_id = ? AND read_id = ? AND connector_id = ? AND document_key = ? AND cited = ?",
						ownerUserID, readID, connID, fallbackKey, false).
					Update("cited", true)
				if res.Error != nil {
					return res.Error
				}
				matchedKey = fallbackKey
			}
			if res.RowsAffected > 0 {
				if err := tx.Model(&knowledgeDocumentUsageRow{}).
					Where("owner_user_id = ? AND connector_id = ? AND document_key = ?",
						ownerUserID, connID, matchedKey).
					Updates(map[string]any{
						"cited_count": gorm.Expr("cited_count + ?", res.RowsAffected),
						"updated_at":  &now,
					}).Error; err != nil {
					return err
				}
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

// ListKnowledgeDocumentUsage returns per-document invocation counts (including
// cited counts and top callers) for ownerUserID, ordered by call_count DESC.
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
	docKeys := make([]string, 0, len(rows))
	docKeySet := make(map[string]struct{}, len(rows))
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
			CitedCount:     row.CitedCount,
			LastCalledAt:   row.LastCalledAt,
		})
		if _, ok := docKeySet[row.DocumentKey]; !ok {
			docKeySet[row.DocumentKey] = struct{}{}
			docKeys = append(docKeys, row.DocumentKey)
		}
	}

	if len(docKeys) > 0 {
		topMap := r.loadTopCallersByDoc(ownerUserID, connectorID, docKeys, knowledgeTopCallersPerDoc)
		for i, row := range rows {
			composite := row.ConnectorID + "\x00" + row.DocumentKey
			if callers := topMap[composite]; len(callers) > 0 {
				out[i].TopCallers = callers
			}
		}
	}
	return out
}

func (r *KnowledgeRepo) loadTopCallersByDoc(ownerUserID int64, connectorID string, docKeys []string, maxPerDoc int) map[string][]domain.KnowledgeUsageCallerSummary {
	q := r.DB.Where("owner_user_id = ? AND document_key IN ?", ownerUserID, docKeys)
	if id := strings.TrimSpace(connectorID); id != "" {
		q = q.Where("connector_id = ?", id)
	}
	var events []knowledgeUsageEventRow
	if err := q.Order("called_at DESC, id DESC").Find(&events).Error; err != nil || len(events) == 0 {
		return nil
	}

	type bucket struct {
		summary domain.KnowledgeUsageCallerSummary
	}
	perDoc := make(map[string]map[string]*bucket)
	for _, ev := range events {
		docComposite := ev.ConnectorID + "\x00" + ev.DocumentKey
		groupMap := perDoc[docComposite]
		if groupMap == nil {
			groupMap = make(map[string]*bucket)
			perDoc[docComposite] = groupMap
		}
		caller := rowToUsageCaller(ev)
		key, label, subLabel := callerSummaryIdentity(caller)
		b := groupMap[key]
		if b == nil {
			calledAt := ev.CalledAt
			b = &bucket{
				summary: domain.KnowledgeUsageCallerSummary{
					Kind:         caller.Kind,
					Label:        label,
					SubLabel:     subLabel,
					WorkflowID:   caller.WorkflowID,
					RunID:        caller.RunID,
					NodeID:       caller.NodeID,
					AgentID:      caller.AgentID,
					SessionID:    caller.SessionID,
					SpaceID:      caller.SpaceID,
					IssueID:      caller.IssueID,
					ProjectID:    caller.ProjectID,
					LastCalledAt: &calledAt,
				},
			}
			groupMap[key] = b
		}
		b.summary.CallCount++
		if ev.Cited {
			b.summary.CitedCount++
		}
	}

	result := make(map[string][]domain.KnowledgeUsageCallerSummary, len(perDoc))
	for docComposite, groupMap := range perDoc {
		list := make([]domain.KnowledgeUsageCallerSummary, 0, len(groupMap))
		for _, b := range groupMap {
			list = append(list, b.summary)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].CallCount != list[j].CallCount {
				return list[i].CallCount > list[j].CallCount
			}
			var ti, tj time.Time
			if list[i].LastCalledAt != nil {
				ti = *list[i].LastCalledAt
			}
			if list[j].LastCalledAt != nil {
				tj = *list[j].LastCalledAt
			}
			if !ti.Equal(tj) {
				return ti.After(tj)
			}
			return list[i].Label < list[j].Label
		})
		if maxPerDoc > 0 && len(list) > maxPerDoc {
			list = list[:maxPerDoc]
		}
		result[docComposite] = list
	}
	return result
}

// ListKnowledgeUsageEvents returns paginated document-level caller events for
// ownerUserID, ordered newest first.
func (r *KnowledgeRepo) ListKnowledgeUsageEvents(ownerUserID int64, filter domain.KnowledgeUsageEventFilter) domain.KnowledgeUsageEventPage {
	page := domain.KnowledgeUsageEventPage{Items: []domain.KnowledgeUsageEvent{}}
	if r == nil || r.DB == nil {
		return page
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	q := r.DB.Model(&knowledgeUsageEventRow{}).Where("owner_user_id = ?", ownerUserID)
	if connID := strings.TrimSpace(filter.ConnectorID); connID != "" {
		q = q.Where("connector_id = ?", connID)
	}
	if docID := strings.TrimSpace(filter.DocumentID); docID != "" {
		q = q.Where("document_id = ?", docID)
	}
	switch strings.ToLower(strings.TrimSpace(filter.CallerKind)) {
	case "":
	case "sop":
		q = q.Where("caller_kind = ?", domain.KnowledgeCallerSOP)
	case "role", "agent":
		q = q.Where("agent_id <> '' OR agent_name <> ''")
	case "chat":
		q = q.Where("caller_kind IN ?", []string{
			domain.KnowledgeCallerChat,
			domain.KnowledgeCallerSpace,
			domain.KnowledgeCallerClarify,
			domain.KnowledgeCallerScheduled,
			domain.KnowledgeCallerReport,
		})
	default:
		q = q.Where("caller_kind = ?", strings.ToLower(strings.TrimSpace(filter.CallerKind)))
	}
	if kw := strings.TrimSpace(filter.Query); kw != "" {
		like := "%" + escapeSQLLike(kw) + "%"
		q = q.Where(
			"title LIKE ? ESCAPE '!' OR file_name LIKE ? ESCAPE '!' OR document_id LIKE ? ESCAPE '!' OR source_name LIKE ? ESCAPE '!' OR query LIKE ? ESCAPE '!' OR workflow_name LIKE ? ESCAPE '!' OR agent_name LIKE ? ESCAPE '!' OR node_name LIKE ? ESCAPE '!' OR session_title LIKE ? ESCAPE '!' OR space_title LIKE ? ESCAPE '!' OR issue_title LIKE ? ESCAPE '!'",
			like, like, like, like, like, like, like, like, like, like, like,
		)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return page
	}
	page.Total = total
	if total == 0 {
		return page
	}

	var rows []knowledgeUsageEventRow
	if err := q.Order("called_at DESC, id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return page
	}
	items := make([]domain.KnowledgeUsageEvent, 0, len(rows))
	for _, row := range rows {
		items = append(items, domain.KnowledgeUsageEvent{
			ID:          row.ID,
			ReadID:      row.ReadID,
			ConnectorID: row.ConnectorID,
			SourceID:    row.SourceID,
			SourceName:  row.SourceName,
			DocumentID:  row.DocumentID,
			Title:       row.Title,
			FileName:    row.FileName,
			Action:      row.Action,
			Tool:        row.Tool,
			Query:       row.Query,
			Cited:       row.Cited,
			CalledAt:    row.CalledAt,
			Caller:      rowToUsageCaller(row),
		})
	}
	page.Items = items
	page.HasMore = int64(offset+len(items)) < total
	return page
}

// ResetKnowledgeUsage clears usage counters and caller events for ownerUserID
// (scoped to connectorID if non-empty).
func (r *KnowledgeRepo) ResetKnowledgeUsage(ownerUserID int64, connectorID string) error {
	if r == nil || r.DB == nil {
		return nil
	}
	connID := strings.TrimSpace(connectorID)
	return r.DB.Transaction(func(tx *gorm.DB) error {
		qConn := tx.Where("owner_user_id = ?", ownerUserID)
		qDoc := tx.Where("owner_user_id = ?", ownerUserID)
		qEv := tx.Where("owner_user_id = ?", ownerUserID)
		if connID != "" {
			qConn = qConn.Where("connector_id = ?", connID)
			qDoc = qDoc.Where("connector_id = ?", connID)
			qEv = qEv.Where("connector_id = ?", connID)
		}
		if err := qConn.Delete(&knowledgeConnectorUsageRow{}).Error; err != nil {
			return err
		}
		if err := qDoc.Delete(&knowledgeDocumentUsageRow{}).Error; err != nil {
			return err
		}
		return qEv.Delete(&knowledgeUsageEventRow{}).Error
	})
}

func normalizeUsageCaller(c domain.KnowledgeUsageCaller) domain.KnowledgeUsageCaller {
	kind := strings.ToLower(strings.TrimSpace(c.Kind))
	if kind == "" {
		switch {
		case strings.TrimSpace(c.RunID) != "" || strings.TrimSpace(c.WorkflowID) != "":
			kind = domain.KnowledgeCallerSOP
		case strings.TrimSpace(c.SpaceID) != "":
			kind = domain.KnowledgeCallerSpace
		case strings.TrimSpace(c.IssueID) != "":
			kind = domain.KnowledgeCallerClarify
		case strings.TrimSpace(c.SessionID) != "":
			kind = domain.KnowledgeCallerChat
		}
	}
	return domain.KnowledgeUsageCaller{
		Kind:         clipUsageField(kind, 32),
		WorkflowID:   clipUsageField(c.WorkflowID, 64),
		WorkflowName: clipUsageField(c.WorkflowName, 255),
		RunID:        clipUsageField(c.RunID, 64),
		NodeID:       clipUsageField(c.NodeID, 64),
		NodeName:     clipUsageField(c.NodeName, 255),
		AgentID:      clipUsageField(c.AgentID, 64),
		AgentName:    clipUsageField(c.AgentName, 255),
		SessionID:    clipUsageField(c.SessionID, 64),
		SessionTitle: clipUsageField(c.SessionTitle, 255),
		SpaceID:      clipUsageField(c.SpaceID, 64),
		SpaceTitle:   clipUsageField(c.SpaceTitle, 255),
		IssueID:      clipUsageField(c.IssueID, 64),
		IssueTitle:   clipUsageField(c.IssueTitle, 255),
		ProjectID:    clipUsageField(c.ProjectID, 64),
	}
}

func rowToUsageCaller(row knowledgeUsageEventRow) domain.KnowledgeUsageCaller {
	return domain.KnowledgeUsageCaller{
		Kind:         row.CallerKind,
		WorkflowID:   row.WorkflowID,
		WorkflowName: row.WorkflowName,
		RunID:        row.RunID,
		NodeID:       row.NodeID,
		NodeName:     row.NodeName,
		AgentID:      row.AgentID,
		AgentName:    row.AgentName,
		SessionID:    row.SessionID,
		SessionTitle: row.SessionTitle,
		SpaceID:      row.SpaceID,
		SpaceTitle:   row.SpaceTitle,
		IssueID:      row.IssueID,
		IssueTitle:   row.IssueTitle,
		ProjectID:    row.ProjectID,
	}
}

func callerSummaryIdentity(c domain.KnowledgeUsageCaller) (key, label, subLabel string) {
	firstNonEmptyStr := func(vals ...string) string {
		for _, v := range vals {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
		return ""
	}
	switch c.Kind {
	case domain.KnowledgeCallerSOP:
		wfKey := firstNonEmptyStr(c.WorkflowID, c.WorkflowName, c.RunID)
		agentKey := firstNonEmptyStr(c.AgentID, c.AgentName)
		nodeKey := firstNonEmptyStr(c.NodeID, c.NodeName)
		key = "sop|" + wfKey + "|" + agentKey + "|" + nodeKey
		label = firstNonEmptyStr(c.WorkflowName, c.WorkflowID, "SOP 运行")
		parts := make([]string, 0, 2)
		if a := firstNonEmptyStr(c.AgentName, c.AgentID); a != "" {
			parts = append(parts, a)
		}
		if n := firstNonEmptyStr(c.NodeName, c.NodeID); n != "" {
			parts = append(parts, n)
		}
		subLabel = strings.Join(parts, " · ")
	case domain.KnowledgeCallerSpace:
		key = "space|" + firstNonEmptyStr(c.SpaceID, c.SpaceTitle)
		label = firstNonEmptyStr(c.SpaceTitle, c.SpaceID, "协作空间")
		subLabel = "协作空间"
	case domain.KnowledgeCallerClarify:
		key = "clarify|" + firstNonEmptyStr(c.IssueID, c.SessionID, c.IssueTitle)
		label = firstNonEmptyStr(c.IssueTitle, c.SessionTitle, c.IssueID, "需求澄清")
		subLabel = "需求澄清"
	case domain.KnowledgeCallerScheduled:
		key = "scheduled|" + firstNonEmptyStr(c.SessionID, c.SessionTitle)
		label = firstNonEmptyStr(c.SessionTitle, "定时任务")
		subLabel = "定时任务"
	case domain.KnowledgeCallerReport:
		key = "report|" + firstNonEmptyStr(c.IssueID, c.SessionID, c.SessionTitle)
		label = firstNonEmptyStr(c.SessionTitle, c.IssueTitle, "分析报告")
		subLabel = "分析报告"
	default:
		key = "chat|" + firstNonEmptyStr(c.SessionID, c.SessionTitle)
		label = firstNonEmptyStr(c.SessionTitle, "个人对话")
		subLabel = "个人对话"
	}
	return key, label, subLabel
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

func escapeSQLLike(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '!' || r == '%' || r == '_' {
			b.WriteByte('!')
		}
		b.WriteRune(r)
	}
	return b.String()
}
