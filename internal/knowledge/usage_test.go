package knowledge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
)

type memoryUsageStore struct {
	mu     sync.Mutex
	deltas []struct {
		owner int64
		delta domain.KnowledgeUsageDelta
	}
}

func (m *memoryUsageStore) RecordKnowledgeUsage(_ context.Context, ownerUserID int64, delta domain.KnowledgeUsageDelta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deltas = append(m.deltas, struct {
		owner int64
		delta domain.KnowledgeUsageDelta
	}{owner: ownerUserID, delta: delta})
	return nil
}

func (m *memoryUsageStore) ListKnowledgeConnectorUsage(ownerUserID int64, connectorID string) []domain.KnowledgeConnectorUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	byConn := map[string]*domain.KnowledgeConnectorUsage{}
	for _, item := range m.deltas {
		if item.owner != ownerUserID {
			continue
		}
		if connectorID != "" && item.delta.ConnectorID != connectorID {
			continue
		}
		u, ok := byConn[item.delta.ConnectorID]
		if !ok {
			u = &domain.KnowledgeConnectorUsage{ConnectorID: item.delta.ConnectorID}
			byConn[item.delta.ConnectorID] = u
		}
		u.CallCount++
		u.SearchCount += item.delta.Search
		u.ReadCount += item.delta.Read
		u.DocumentHitCount += int64(len(item.delta.Documents))
		u.ErrorCount += item.delta.Error
		t := item.delta.CalledAt
		u.LastCalledAt = &t
	}
	var out []domain.KnowledgeConnectorUsage
	for _, u := range byConn {
		out = append(out, *u)
	}
	return out
}

func (m *memoryUsageStore) ListKnowledgeDocumentUsage(ownerUserID int64, connectorID string, _ int) []domain.KnowledgeDocumentUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	byDoc := map[string]*domain.KnowledgeDocumentUsage{}
	for _, item := range m.deltas {
		if item.owner != ownerUserID {
			continue
		}
		if connectorID != "" && item.delta.ConnectorID != connectorID {
			continue
		}
		for _, doc := range item.delta.Documents {
			key := item.delta.ConnectorID + "|" + doc.SourceID + "|" + doc.DocumentID
			u, ok := byDoc[key]
			if !ok {
				u = &domain.KnowledgeDocumentUsage{
					ConnectorID: item.delta.ConnectorID,
					SourceID:    doc.SourceID,
					SourceName:  doc.SourceName,
					DocumentID:  doc.DocumentID,
					Title:       doc.Title,
					FileName:    doc.FileName,
				}
				byDoc[key] = u
			}
			u.CallCount++
			u.SearchHitCount += doc.SearchHit
			u.ReadCount += doc.Read
		}
	}
	var out []domain.KnowledgeDocumentUsage
	for _, u := range byDoc {
		out = append(out, *u)
	}
	return out
}

func (m *memoryUsageStore) ResetKnowledgeUsage(ownerUserID int64, connectorID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	filtered := m.deltas[:0]
	for _, item := range m.deltas {
		if item.owner == ownerUserID && (connectorID == "" || item.delta.ConnectorID == connectorID) {
			continue
		}
		filtered = append(filtered, item)
	}
	m.deltas = filtered
	return nil
}

func TestUsageCounterIgnoresCatalogProbeAndDeduplicatesDocuments(t *testing.T) {
	store := &memoryUsageStore{}
	counter := NewUsageCounter(store)
	fixedNow := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	counter.now = func() time.Time { return fixedNow }

	ctx := context.Background()

	// 1. Background catalog_probe must NOT be counted.
	if err := counter.RecordExecution(ctx, 7, domain.KnowledgeRead{
		Origin:   domain.KnowledgeCallOriginCatalogProbe,
		ServerID: "conn-1",
		Tool:     "list_documents",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if len(store.deltas) != 0 {
		t.Fatalf("expected catalog_probe to be ignored, got %d deltas", len(store.deltas))
	}

	// 2. Search returning 2 chunks of the same doc counts that doc once.
	if err := counter.RecordExecution(ctx, 7, domain.KnowledgeRead{
		Origin:   domain.KnowledgeCallOriginModelTool,
		ServerID: "conn-1",
		Tool:     "search_knowledge",
		Evidences: []domain.KnowledgeEvidence{
			{SourceID: "kb1", SourceName: "规范库", DocumentID: "doc-1", Title: "架构文档"},
			{SourceID: "kb1", SourceName: "规范库", DocumentID: "doc-1", Title: "架构文档"},
			{SourceID: "kb1", SourceName: "规范库", DocumentID: "doc-2", FileName: "faq.md"},
		},
	}, nil); err != nil {
		t.Fatal(err)
	}

	// 3. Direct read without evidences falls back to Targets.
	if err := counter.RecordExecution(ctx, 7, domain.KnowledgeRead{
		Origin:   domain.KnowledgeCallOriginModelTool,
		ServerID: "conn-1",
		Tool:     "read_document",
		ReadFull: true,
		Targets:  []string{"doc-3"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	// 4. Failed call increments error count without document hits.
	if err := counter.RecordExecution(ctx, 7, domain.KnowledgeRead{
		Origin:   domain.KnowledgeCallOriginModelTool,
		ServerID: "conn-1",
		Tool:     "read_document",
		ReadFull: true,
	}, errors.New("timeout")); err != nil {
		t.Fatal(err)
	}

	summary := counter.Summary([]domain.KnowledgeConnector{
		{ID: "conn-1", Name: "我的知识库", Type: domain.ConnectorTypeWeKnora},
	}, 7, "", 20)

	if summary.TotalCalls != 3 || summary.TotalDocumentCalls != 3 {
		t.Fatalf("unexpected summary totals: %+v", summary)
	}
	if len(summary.Connectors) != 1 {
		t.Fatalf("expected 1 connector summary, got %+v", summary.Connectors)
	}
	c := summary.Connectors[0]
	if c.ConnectorName != "我的知识库" || c.ConnectorType != domain.ConnectorTypeWeKnora || c.CallCount != 3 || c.SearchCount != 1 || c.ReadCount != 1 || c.ErrorCount != 1 || c.DocumentHitCount != 3 {
		t.Fatalf("unexpected connector usage: %+v", c)
	}
	if len(summary.Documents) != 3 {
		t.Fatalf("expected 3 deduplicated document usages, got %+v", summary.Documents)
	}
}
