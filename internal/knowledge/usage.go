package knowledge

import (
	"context"
	"strings"
	"time"

	"agentflow/internal/domain"
)

type usageCallerContextKey struct{}

// WithUsageCaller attaches caller identity (SOP/node/role or Chat/Space/Clarify
// session) to ctx so Executor and UsageCounter can persist who accessed each
// knowledge document.
func WithUsageCaller(ctx context.Context, caller domain.KnowledgeUsageCaller) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, usageCallerContextKey{}, caller)
}

// UsageCallerFromContext extracts the KnowledgeUsageCaller attached via
// WithUsageCaller (returns zero value when unset).
func UsageCallerFromContext(ctx context.Context) domain.KnowledgeUsageCaller {
	if ctx == nil {
		return domain.KnowledgeUsageCaller{}
	}
	if caller, ok := ctx.Value(usageCallerContextKey{}).(domain.KnowledgeUsageCaller); ok {
		return caller
	}
	return domain.KnowledgeUsageCaller{}
}

// UsageStore persists and queries knowledge base and document invocation counts
// as well as per-document caller events.
type UsageStore interface {
	RecordKnowledgeUsage(ctx context.Context, ownerUserID int64, delta domain.KnowledgeUsageDelta) error
	MarkKnowledgeUsageCited(ctx context.Context, ownerUserID int64, marks []domain.KnowledgeCitedDocMark) error
	ListKnowledgeConnectorUsage(ownerUserID int64, connectorID string) []domain.KnowledgeConnectorUsage
	ListKnowledgeDocumentUsage(ownerUserID int64, connectorID string, limit int) []domain.KnowledgeDocumentUsage
	ListKnowledgeUsageEvents(ownerUserID int64, filter domain.KnowledgeUsageEventFilter) domain.KnowledgeUsageEventPage
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

// RecordExecution increments connector and document counters and records
// per-document caller events for a single runtime knowledge tool execution.
// Background catalog probes are ignored.
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
		ReadID:      strings.TrimSpace(read.ID),
		ConnectorID: connID,
		Tool:        strings.TrimSpace(read.Tool),
		Query:       strings.TrimSpace(read.Query),
		Caller:      UsageCallerFromContext(ctx),
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

// MarkCited writes back final-answer citation status for documents that were
// actually cited via [[knowledge:...]] markers in assistant replies or SOP node
// outputs.
func (c *UsageCounter) MarkCited(ctx context.Context, ownerUserID int64, reads []domain.KnowledgeRead) error {
	if c == nil || c.store == nil || len(reads) == 0 {
		return nil
	}
	marks := collectCitedDocMarks(reads)
	if len(marks) == 0 {
		return nil
	}
	return c.store.MarkKnowledgeUsageCited(ctx, ownerUserID, marks)
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
		summary.TotalCitedCount += docUsage[i].CitedCount
	}
	if docUsage != nil {
		summary.Documents = docUsage
	}
	return summary
}

// Events returns paginated document-level caller events enriched with
// connector metadata.
func (c *UsageCounter) Events(connectors []domain.KnowledgeConnector, ownerUserID int64, filter domain.KnowledgeUsageEventFilter) domain.KnowledgeUsageEventPage {
	page := domain.KnowledgeUsageEventPage{Items: []domain.KnowledgeUsageEvent{}}
	if c == nil || c.store == nil {
		return page
	}
	connMeta := make(map[string]domain.KnowledgeConnector, len(connectors))
	for _, conn := range connectors {
		connMeta[conn.ID] = conn
	}
	page = c.store.ListKnowledgeUsageEvents(ownerUserID, filter)
	if page.Items == nil {
		page.Items = []domain.KnowledgeUsageEvent{}
	}
	for i := range page.Items {
		if meta, ok := connMeta[page.Items[i].ConnectorID]; ok {
			page.Items[i].ConnectorName = meta.Name
			page.Items[i].ConnectorType = meta.Type
		}
	}
	return page
}

// Reset clears recorded usage counts and events for ownerUserID (optionally
// scoped to connectorID).
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

func collectCitedDocMarks(reads []domain.KnowledgeRead) []domain.KnowledgeCitedDocMark {
	seen := make(map[string]struct{})
	var out []domain.KnowledgeCitedDocMark

	add := func(readID, connID, sourceID, docID string) {
		readID = strings.TrimSpace(readID)
		connID = strings.TrimSpace(connID)
		docID = strings.TrimSpace(docID)
		if readID == "" || connID == "" || docID == "" {
			return
		}
		sourceID = strings.TrimSpace(sourceID)
		key := readID + "\x00" + connID + "\x00" + sourceID + "\x00" + docID
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, domain.KnowledgeCitedDocMark{
			ReadID:      readID,
			ConnectorID: connID,
			SourceID:    sourceID,
			DocumentID:  docID,
		})
	}

	for _, read := range reads {
		if read.Origin == domain.KnowledgeCallOriginCatalogProbe {
			continue
		}
		connID := strings.TrimSpace(read.ServerID)
		if connID == "" || strings.TrimSpace(read.ID) == "" {
			continue
		}
		addedForRead := false
		for _, ev := range read.Evidences {
			if !ev.Cited {
				continue
			}
			add(read.ID, connID, ev.SourceID, ev.DocumentID)
			addedForRead = true
		}
		if !addedForRead && read.Cited {
			if len(read.Evidences) > 0 {
				for _, ev := range read.Evidences {
					add(read.ID, connID, ev.SourceID, ev.DocumentID)
				}
			} else if read.ReadFull {
				for _, target := range read.Targets {
					add(read.ID, connID, "", target)
				}
			}
		}
	}
	return out
}
