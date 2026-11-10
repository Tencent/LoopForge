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

// 内置知识引擎的领域实体。控制面在 KnowledgeRepo，内容面在 KnowledgeContentRepo；
// 内容行只用 SourceID 字符串指向控制面，不建跨组外键。

// KnowledgeBase 是一个知识库。切片只写 Layer="project" 的项目层库；
// ProjectID 为空留给以后的公共/平台库，不参与 (project_id, layer) 唯一约束。
type KnowledgeBase struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId,omitempty"`
	Name        string    `json:"name"`
	Layer       string    `json:"layer"`
	OwnerUserID int64     `json:"ownerUserId,omitempty"`
	CreatedAt   time.Time `json:"createdAt,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt,omitempty"`
}

// KnowledgeSource 是一个知识来源。Kind 为 hosted 或 git。托管库用
// HeadRevision 记当前修订号，Git 来源用 HeadCommit 记 commit。
type KnowledgeSource struct {
	ID           string     `json:"id"`
	BaseID       string     `json:"baseId"`
	Kind         string     `json:"kind"`
	Name         string     `json:"name"`
	GitURL       string     `json:"gitUrl,omitempty"`
	Branch       string     `json:"branch,omitempty"`
	PathPrefix   string     `json:"pathPrefix,omitempty"`
	HeadCommit   string     `json:"headCommit,omitempty"`
	HeadRevision int64      `json:"headRevision,omitempty"`
	Status       string     `json:"status,omitempty"`
	IndexStatus  string     `json:"indexStatus,omitempty"`
	DocCount     int        `json:"docCount,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
	LastSyncedAt *time.Time `json:"lastSyncedAt,omitempty"`
	LeaseOwner   string     `json:"leaseOwner,omitempty"`
	LeaseUntil   *time.Time `json:"leaseUntil,omitempty"`
}

// KnowledgeBinding 把项目绑定到知识库；切片里 Permission 只写 submit。
type KnowledgeBinding struct {
	ProjectID  string `json:"projectId"`
	BaseID     string `json:"baseId"`
	Permission string `json:"permission"`
}

// KnowledgeSyncJob 是一次同步的记录，状态与错误可在知识中心看见。
type KnowledgeSyncJob struct {
	ID           int64      `json:"id"`
	SourceID     string     `json:"sourceId"`
	Kind         string     `json:"kind"`
	Status       string     `json:"status"`
	FromCommit   string     `json:"fromCommit,omitempty"`
	ToCommit     string     `json:"toCommit,omitempty"`
	ChangedCount int        `json:"changedCount,omitempty"`
	DeletedCount int        `json:"deletedCount,omitempty"`
	Log          string     `json:"log,omitempty"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

// KnowledgeDocument 是含 frontmatter 的完整 Markdown 正文。出处版本对托管库
// 是修订号的十进制字符串，对 Git 来源是 commit；两者都放在 Commit 字段。
type KnowledgeDocument struct {
	SourceID  string    `json:"sourceId"`
	Path      string    `json:"path"`
	Title     string    `json:"title,omitempty"`
	Body      string    `json:"body"`
	LineCount int       `json:"lineCount,omitempty"`
	SHA256    string    `json:"sha256,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	Revision  int64     `json:"revision,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

// KnowledgeRevision 只给托管库追加，Git 同步不写修订历史。
type KnowledgeRevision struct {
	ID        string    `json:"id"`
	SourceID  string    `json:"sourceId"`
	Path      string    `json:"path"`
	Revision  int64     `json:"revision"`
	Body      string    `json:"body"`
	Author    string    `json:"author,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
}

// KnowledgeChunk 是检索用的索引块，可以从正文重建。
type KnowledgeChunk struct {
	ID        int64  `json:"id"`
	SourceID  string `json:"sourceId"`
	Path      string `json:"path"`
	Idx       int    `json:"idx"`
	Heading   string `json:"heading,omitempty"`
	Title     string `json:"title,omitempty"`
	StartLine int    `json:"startLine,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
	Commit    string `json:"commit,omitempty"`
	Revision  int64  `json:"revision,omitempty"`
	Text      string `json:"text"`
}

// KnowledgeChunkMatch 是关键词适配器的一次命中：索引块加相关度分数。
// 分数由适配器给出，调用方据此排序，不改索引块本身。
type KnowledgeChunkMatch struct {
	Chunk KnowledgeChunk `json:"chunk"`
	Score float64        `json:"score"`
}
