package domain

import (
	"strings"
	"time"
)

// TODO(knowledge-legacy-compat): 历史平台级知识库与沉淀审核队列领域模型，仅为向后兼容
// 旧版 `/api/knowledge/{settings,check,sources,search,document,candidates,submissions}`
// 接口而保留；新接入一律使用用户级连接器模型（见 knowledge.go 中的 KnowledgeConnector）。
// 待所有旧调用方、上游 E2E 用例（如 tokenOwner 探针）及历史数据迁移完成后整体移除本文件。

// KnowledgeArtifactBasename is the conventional deliverable name a run writes
// when it has knowledge worth distilling back into the legacy shared KB.
const KnowledgeArtifactBasename = "knowledge.md"

// KnowledgeSettings is the legacy platform-wide connection to an upstream
// knowledge base (lightcloud kb). Token never leaves the server; Public()
// strips it before any API response.
//
// Deprecated: TODO(knowledge-legacy-compat) replaced by per-user KnowledgeConnector.
type KnowledgeSettings struct {
	Enabled         bool                `json:"enabled"`
	URL             string              `json:"url"`
	Token           string              `json:"token,omitempty"`
	TokenConfigured bool                `json:"tokenConfigured"`
	WriteEnabled    bool                `json:"writeEnabled"`
	DefaultSources  []string            `json:"defaultSources,omitempty"`
	ProjectSources  map[string][]string `json:"projectSources,omitempty"`
	UpdatedBy       string              `json:"updatedBy,omitempty"`
	UpdatedAt       time.Time           `json:"updatedAt,omitempty"`
}

func (s KnowledgeSettings) Public() KnowledgeSettings {
	s.TokenConfigured = strings.TrimSpace(s.Token) != ""
	s.Token = ""
	return s
}

// Ready reports whether the legacy platform-wide KB upstream is configured and enabled.
func (s KnowledgeSettings) Ready() bool {
	return s.Enabled && strings.TrimSpace(s.URL) != "" && strings.TrimSpace(s.Token) != ""
}

// SourcesForProject returns the legacy sources a project may read.
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

// KnowledgeSubmission is one legacy distilled document waiting for human review
// before being written back to an upstream knowledge source.
//
// Deprecated: TODO(knowledge-legacy-compat) kept for legacy review-queue API callers.
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
// document and can be turned into a legacy submission.
//
// Deprecated: TODO(knowledge-legacy-compat) kept for GET /api/knowledge/candidates compatibility.
type KnowledgeCandidate struct {
	Artifact     Artifact `json:"artifact"`
	ProjectID    string   `json:"projectId,omitempty"`
	IssueID      string   `json:"issueId,omitempty"`
	WorkflowID   string   `json:"workflowId,omitempty"`
	RunStatus    string   `json:"runStatus,omitempty"`
	SubmissionID string   `json:"submissionId,omitempty"`
}
