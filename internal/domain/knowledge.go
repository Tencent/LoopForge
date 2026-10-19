package domain

import (
	"strings"
	"time"
)

// KnowledgeMCPServerID is the reserved id of the read-only knowledge base MCP
// that the engine materializes for roles with AgentDefinition.KnowledgeBase.
// Like PlatformMCPServerID it is never a row in the mcp_servers table.
const KnowledgeMCPServerID = "agentflow-knowledge"

// KnowledgeUsageRuleID is the built-in rule injected alongside the knowledge
// MCP so every opted-in role searches before answering and cites sources.
const KnowledgeUsageRuleID = "agentflow-knowledge-usage"

// KnowledgeArtifactBasename is the conventional deliverable name a run writes
// when it has knowledge worth distilling back into the knowledge base.
const KnowledgeArtifactBasename = "knowledge.md"

// KnowledgeSettings is the platform-wide connection to the upstream knowledge
// base (lightcloud kb). Token is the upstream MCP bearer token; it never
// leaves the server and Public() strips it before any API response.
type KnowledgeSettings struct {
	Enabled         bool   `json:"enabled"`
	URL             string `json:"url"`
	Token           string `json:"token,omitempty"`
	TokenConfigured bool   `json:"tokenConfigured"`
	// WriteEnabled gates publishing reviewed submissions upstream. Agents never
	// receive the write tool regardless of this flag.
	WriteEnabled bool `json:"writeEnabled"`
	// DefaultSources applies to projects without their own entry; empty means
	// every source the upstream token can see.
	DefaultSources []string            `json:"defaultSources,omitempty"`
	ProjectSources map[string][]string `json:"projectSources,omitempty"`
	UpdatedBy      string              `json:"updatedBy,omitempty"`
	UpdatedAt      time.Time           `json:"updatedAt,omitempty"`
}

func (s KnowledgeSettings) Public() KnowledgeSettings {
	s.TokenConfigured = strings.TrimSpace(s.Token) != ""
	s.Token = ""
	return s
}

// Ready reports whether agents and platform features may call the upstream.
func (s KnowledgeSettings) Ready() bool {
	return s.Enabled && strings.TrimSpace(s.URL) != "" && strings.TrimSpace(s.Token) != ""
}

// SourcesForProject returns the sources a project may read. A project entry
// (even an empty one) overrides DefaultSources; empty means unrestricted.
func (s KnowledgeSettings) SourcesForProject(projectID string) []string {
	if list, ok := s.ProjectSources[strings.TrimSpace(projectID)]; ok {
		return list
	}
	return s.DefaultSources
}

const (
	KnowledgeSubmissionPending   = "pending"
	KnowledgeSubmissionPublished = "published"
	KnowledgeSubmissionRejected  = "rejected"
)

// KnowledgeSubmission is one distilled document waiting for human review
// before it is written back to an upstream knowledge source.
type KnowledgeSubmission struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId,omitempty"`
	RunID       string    `json:"runId,omitempty"`
	NodeID      string    `json:"nodeId,omitempty"`
	ArtifactID  string    `json:"artifactId,omitempty"`
	IssueID     string    `json:"issueId,omitempty"`
	WorkflowID  string    `json:"workflowId,omitempty"`
	Title       string    `json:"title"`
	Source      string    `json:"source"`
	Path        string    `json:"path"`
	Content     string    `json:"content,omitempty"`
	Status      string    `json:"status"`
	Commit      string    `json:"commit,omitempty"`
	Error       string    `json:"error,omitempty"`
	ReviewNote  string    `json:"reviewNote,omitempty"`
	CreatedBy   string    `json:"createdBy,omitempty"`
	ReviewedBy  string    `json:"reviewedBy,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	PublishedAt time.Time `json:"publishedAt,omitempty"`
}

// KnowledgeCandidate is a run artifact that looks like a distilled knowledge
// document and can be turned into a submission.
type KnowledgeCandidate struct {
	Artifact     Artifact `json:"artifact"`
	ProjectID    string   `json:"projectId,omitempty"`
	IssueID      string   `json:"issueId,omitempty"`
	WorkflowID   string   `json:"workflowId,omitempty"`
	RunStatus    string   `json:"runStatus,omitempty"`
	SubmissionID string   `json:"submissionId,omitempty"`
}
