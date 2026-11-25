package domain

import "time"

// KnowledgeConnectorUsage aggregates how many times a user's knowledge
// connector has been invoked by agents/models.
type KnowledgeConnectorUsage struct {
	ConnectorID      string     `json:"connectorId"`
	ConnectorName    string     `json:"connectorName,omitempty"`
	ConnectorType    string     `json:"connectorType,omitempty"`
	CallCount        int64      `json:"callCount"`
	SearchCount      int64      `json:"searchCount"`
	ReadCount        int64      `json:"readCount"`
	DocumentHitCount int64      `json:"documentHitCount"`
	ErrorCount       int64      `json:"errorCount"`
	LastCalledAt     *time.Time `json:"lastCalledAt,omitempty"`
}

// KnowledgeDocumentUsage tracks how many times an individual document inside a
// knowledge connector has been hit in search results or read directly.
type KnowledgeDocumentUsage struct {
	ConnectorID    string     `json:"connectorId"`
	ConnectorName  string     `json:"connectorName,omitempty"`
	SourceID       string     `json:"sourceId,omitempty"`
	SourceName     string     `json:"sourceName,omitempty"`
	DocumentID     string     `json:"documentId"`
	Title          string     `json:"title,omitempty"`
	FileName       string     `json:"fileName,omitempty"`
	CallCount      int64      `json:"callCount"`
	SearchHitCount int64      `json:"searchHitCount"`
	ReadCount      int64      `json:"readCount"`
	LastCalledAt   *time.Time `json:"lastCalledAt,omitempty"`
}

// KnowledgeUsageSummary is the combined usage view for a user's knowledge
// connectors and their most-called documents.
type KnowledgeUsageSummary struct {
	TotalCalls         int64                     `json:"totalCalls"`
	TotalDocumentCalls int64                     `json:"totalDocumentCalls"`
	Connectors         []KnowledgeConnectorUsage `json:"connectors"`
	Documents          []KnowledgeDocumentUsage  `json:"documents"`
}

// KnowledgeDocumentUsageDelta describes one document's increment from a single
// knowledge tool execution.
type KnowledgeDocumentUsageDelta struct {
	SourceID   string
	SourceName string
	DocumentID string
	Title      string
	FileName   string
	SearchHit  int64
	Read       int64
}

// KnowledgeUsageDelta describes the connector and document increments produced
// by a single knowledge tool execution.
type KnowledgeUsageDelta struct {
	ConnectorID string
	Search      int64
	Read        int64
	Error       int64
	CalledAt    time.Time
	Documents   []KnowledgeDocumentUsageDelta
}

