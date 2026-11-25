package knowledge

import (
	"context"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// UsageStore persists and queries knowledge base and document invocation counts.
type UsageStore interface {
	RecordKnowledgeUsage(ctx context.Context, ownerUserID int64, delta domain.KnowledgeUsageDelta) error
	ListKnowledgeConnectorUsage(ownerUserID int64, connectorID string) []domain.KnowledgeConnectorUsage
	ListKnowledgeDocumentUsage(ownerUserID int64, connectorID string, limit int) []domain.KnowledgeDocumentUsage
	ResetKnowledgeUsage(ownerUserID int64, connectorID string) error
}

// UsageCounter records and summarizes knowledge base and document invocations.
type UsageCounter struct {
	store UsageStore
	now   func() time.Time
}

// NewUsageCounter constructs a UsageCounter backed by store (nil-safe).
func NewUsageCounter(store UsageStore) *UsageCounter {
	return &UsageCounter{
		store: store,
		now:   time.Now,
	}
}

// RecordExecution increments connector and document counters for a single
// runtime knowledge tool execution. Background catalog probes are ignored.
func (c *UsageCounter) RecordExecution(ctx context.Context, ownerUserID int64, read domain.KnowledgeRead, callErr error) error {
	if c == nil || c.store == nil {
		return nil
	}
	if read.Origin == domain.KnowledgeCallOriginCatalogProbe {
		return nil
	}
	connID := strings.TrimSpace(read.ServerID)
	if connID == "" {
		return nil
	}

	now := time.Now().UTC()
	if c.now != nil {
		now = c.now().UTC()
	}

	delta := domain.KnowledgeUsageDelta{
		ConnectorID: connID,
		CalledAt:    now,
	}
	if callErr != nil {
		delta.Error = 1
	} else if read.ReadFull {
		delta.Read = 1
	} else {
		delta.Search = 1
	}

	if callErr == nil {
		delta.Documents = collectDocumentDeltas(read)
	}
	return c.store.RecordKnowledgeUsage(ctx, ownerUserID, delta)
}

// Summary returns aggregated connector and document invocation counts for
// ownerUserID, enriched with connector metadata.
func (c *UsageCounter) Summary(connectors []domain.KnowledgeConnector, ownerUserID int64, connectorID string, docLimit int) domain.KnowledgeUsageSummary {
	summary := domain.KnowledgeUsageSummary{
		Connectors: []domain.KnowledgeConnectorUsage{},
		Documents:  []domain.KnowledgeDocumentUsage{},
	}
	if c == nil || c.store == nil {
		return summary
	}

	connMeta := make(map[string]domain.KnowledgeConnector, len(connectors))
	for _, conn := range connectors {
		connMeta[conn.ID] = conn
	}

	connUsage := c.store.ListKnowledgeConnectorUsage(ownerUserID, connectorID)
	for i := range connUsage {
		if meta, ok := connMeta[connUsage[i].ConnectorID]; ok {
			connUsage[i].ConnectorName = meta.Name
			connUsage[i].ConnectorType = meta.Type
		}
		summary.TotalCalls += connUsage[i].CallCount
		summary.TotalDocumentCalls += connUsage[i].DocumentHitCount
	}
	if connUsage != nil {
		summary.Connectors = connUsage
	}

	docUsage := c.store.ListKnowledgeDocumentUsage(ownerUserID, connectorID, docLimit)
	for i := range docUsage {
		if meta, ok := connMeta[docUsage[i].ConnectorID]; ok {
			docUsage[i].ConnectorName = meta.Name
		}
	}
	if docUsage != nil {
		summary.Documents = docUsage
	}
	return summary
}

// Reset clears recorded usage counts for ownerUserID (optionally scoped to
// connectorID).
func (c *UsageCounter) Reset(ownerUserID int64, connectorID string) error {
	if c == nil || c.store == nil {
		return nil
	}
	return c.store.ResetKnowledgeUsage(ownerUserID, connectorID)
}

func collectDocumentDeltas(read domain.KnowledgeRead) []domain.KnowledgeDocumentUsageDelta {
	seen := make(map[string]int)
	var out []domain.KnowledgeDocumentUsageDelta

	addDoc := func(sourceID, sourceName, docID, title, fileName string) {
		docID = strings.TrimSpace(docID)
		if docID == "" {
			return
		}
		sourceID = strings.TrimSpace(sourceID)
		key := sourceID + "\x00" + docID
		if idx, exists := seen[key]; exists {
			if out[idx].SourceName == "" && strings.TrimSpace(sourceName) != "" {
				out[idx].SourceName = strings.TrimSpace(sourceName)
			}
			if out[idx].Title == "" && strings.TrimSpace(title) != "" {
				out[idx].Title = strings.TrimSpace(title)
			}
			if out[idx].FileName == "" && strings.TrimSpace(fileName) != "" {
				out[idx].FileName = strings.TrimSpace(fileName)
			}
			return
		}
		seen[key] = len(out)
		d := domain.KnowledgeDocumentUsageDelta{
			SourceID:   sourceID,
			SourceName: strings.TrimSpace(sourceName),
			DocumentID: docID,
			Title:      strings.TrimSpace(title),
			FileName:   strings.TrimSpace(fileName),
		}
		if read.ReadFull {
			d.Read = 1
		} else {
			d.SearchHit = 1
		}
		out = append(out, d)
	}

	for _, ev := range read.Evidences {
		addDoc(ev.SourceID, ev.SourceName, ev.DocumentID, ev.Title, ev.FileName)
	}
	if len(out) == 0 && read.ReadFull {
		for _, target := range read.Targets {
			addDoc("", "", target, "", "")
		}
	}
	return out
}
