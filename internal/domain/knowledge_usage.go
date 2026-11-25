package domain

import "time"

// Knowledge usage caller kinds identify which surface triggered a knowledge
// tool invocation.
const (
	KnowledgeCallerSOP       = "sop"
	KnowledgeCallerChat      = "chat"
	KnowledgeCallerSpace     = "space"
	KnowledgeCallerClarify   = "clarify"
	KnowledgeCallerScheduled = "scheduled"
	KnowledgeCallerReport    = "report"
)

// Knowledge usage event actions distinguish search hits from full document reads.
const (
	KnowledgeUsageActionSearchHit = "search_hit"
	KnowledgeUsageActionRead      = "read"
)

// KnowledgeUsageCaller identifies who invoked a knowledge tool (SOP run/node/
// role or Chat/Space/Clarify/Scheduled assistant session).
type KnowledgeUsageCaller struct {
	Kind         string `json:"kind,omitempty"`
	WorkflowID   string `json:"workflowId,omitempty"`
	WorkflowName string `json:"workflowName,omitempty"`
	RunID        string `json:"runId,omitempty"`
	NodeID       string `json:"nodeId,omitempty"`
	NodeName     string `json:"nodeName,omitempty"`
	AgentID      string `json:"agentId,omitempty"`
	AgentName    string `json:"agentName,omitempty"`
	SessionID    string `json:"sessionId,omitempty"`
	SessionTitle string `json:"sessionTitle,omitempty"`
	SpaceID      string `json:"spaceId,omitempty"`
	SpaceTitle   string `json:"spaceTitle,omitempty"`
	IssueID      string `json:"issueId,omitempty"`
	IssueTitle   string `json:"issueTitle,omitempty"`
	ProjectID    string `json:"projectId,omitempty"`
}

// KnowledgeUsageCallerSummary summarizes one caller group that accessed a
// document (used in the per-document topCallers column).
type KnowledgeUsageCallerSummary struct {
	Kind         string     `json:"kind"`
	Label        string     `json:"label"`
	SubLabel     string     `json:"subLabel,omitempty"`
	CallCount    int64      `json:"callCount"`
	CitedCount   int64      `json:"citedCount"`
	WorkflowID   string     `json:"workflowId,omitempty"`
	RunID        string     `json:"runId,omitempty"`
	NodeID       string     `json:"nodeId,omitempty"`
	AgentID      string     `json:"agentId,omitempty"`
	SessionID    string     `json:"sessionId,omitempty"`
	SpaceID      string     `json:"spaceId,omitempty"`
	IssueID      string     `json:"issueId,omitempty"`
	ProjectID    string     `json:"projectId,omitempty"`
	LastCalledAt *time.Time `json:"lastCalledAt,omitempty"`
}

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
// knowledge connector has been hit in search results, read directly, or cited.
type KnowledgeDocumentUsage struct {
	ConnectorID    string                        `json:"connectorId"`
	ConnectorName  string                        `json:"connectorName,omitempty"`
	SourceID       string                        `json:"sourceId,omitempty"`
	SourceName     string                        `json:"sourceName,omitempty"`
	DocumentID     string                        `json:"documentId"`
	Title          string                        `json:"title,omitempty"`
	FileName       string                        `json:"fileName,omitempty"`
	CallCount      int64                         `json:"callCount"`
	SearchHitCount int64                         `json:"searchHitCount"`
	ReadCount      int64                         `json:"readCount"`
	CitedCount     int64                         `json:"citedCount"`
	TopCallers     []KnowledgeUsageCallerSummary `json:"topCallers,omitempty"`
	LastCalledAt   *time.Time                    `json:"lastCalledAt,omitempty"`
}

// KnowledgeUsageSummary is the combined usage view for a user's knowledge
// connectors and their most-called documents.
type KnowledgeUsageSummary struct {
	TotalCalls         int64                     `json:"totalCalls"`
	TotalDocumentCalls int64                     `json:"totalDocumentCalls"`
	TotalCitedCount    int64                     `json:"totalCitedCount"`
	Connectors         []KnowledgeConnectorUsage `json:"connectors"`
	Documents          []KnowledgeDocumentUsage  `json:"documents"`
}

// KnowledgeUsageEvent captures one document access by one caller during a
// model tool call.
type KnowledgeUsageEvent struct {
	ID            int64                `json:"id"`
	ReadID        string               `json:"readId,omitempty"`
	ConnectorID   string               `json:"connectorId"`
	ConnectorName string               `json:"connectorName,omitempty"`
	ConnectorType string               `json:"connectorType,omitempty"`
	SourceID      string               `json:"sourceId,omitempty"`
	SourceName    string               `json:"sourceName,omitempty"`
	DocumentID    string               `json:"documentId"`
	Title         string               `json:"title,omitempty"`
	FileName      string               `json:"fileName,omitempty"`
	Action        string               `json:"action"`
	Tool          string               `json:"tool,omitempty"`
	Query         string               `json:"query,omitempty"`
	Cited         bool                 `json:"cited"`
	CalledAt      time.Time            `json:"calledAt"`
	Caller        KnowledgeUsageCaller `json:"caller"`
}

// KnowledgeUsageEventFilter scopes queries on knowledge_usage_events.
type KnowledgeUsageEventFilter struct {
	ConnectorID string
	DocumentID  string
	CallerKind  string
	Query       string
	Limit       int
	Offset      int
}

// KnowledgeUsageEventPage is a paginated list of document usage events.
type KnowledgeUsageEventPage struct {
	Items   []KnowledgeUsageEvent `json:"items"`
	Total   int64                 `json:"total"`
	HasMore bool                  `json:"hasMore"`
}

// KnowledgeCitedDocMark identifies a document hit/read in a specific tool call
// that was later cited in the final assistant reply or SOP node output.
type KnowledgeCitedDocMark struct {
	ReadID      string
	ConnectorID string
	SourceID    string
	DocumentID  string
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
	ReadID      string
	ConnectorID string
	Tool        string
	Query       string
	Caller      KnowledgeUsageCaller
	Search      int64
	Read        int64
	Error       int64
	CalledAt    time.Time
	Documents   []KnowledgeDocumentUsageDelta
}
