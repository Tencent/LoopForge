package domain

import (
	"net/url"
	"strconv"
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

const (
	KnowledgeProviderLightCloud = "lightcloud"
	KnowledgeProviderWeKnora    = "weknora"
)

type KnowledgeConnection struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Account         string `json:"account,omitempty"`
	Provider        string `json:"provider,omitempty"`
	URL             string `json:"url"`
	Token           string `json:"token,omitempty"`
	TokenConfigured bool   `json:"tokenConfigured"`
	Enabled         bool   `json:"enabled"`
}

// KnowledgeSettings is the platform-wide connection to the upstream knowledge
// base (lightcloud kb or WeKnora). Token is the upstream MCP bearer token or
// WeKnora X-API-Key; it never leaves the server and Public() strips it before
// any API response.
type KnowledgeSettings struct {
	Provider        string `json:"provider,omitempty"`
	Enabled         bool   `json:"enabled"`
	URL             string `json:"url"`
	Token           string `json:"token,omitempty"`
	TokenConfigured bool   `json:"tokenConfigured"`
	// Connections holds additional named knowledge base connections (e.g. multiple
	// WeKnora workspaces/instances) so admins can aggregate them without overwriting.
	Connections []KnowledgeConnection `json:"connections,omitempty"`
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

// EffectiveProvider returns the normalized provider name, defaulting to
// lightcloud for backward compatibility with existing stored settings.
func (c KnowledgeConnection) EffectiveProvider() string {
	switch strings.ToLower(strings.TrimSpace(c.Provider)) {
	case KnowledgeProviderWeKnora:
		return KnowledgeProviderWeKnora
	default:
		return KnowledgeProviderLightCloud
	}
}

// EffectiveProvider returns the normalized provider name, defaulting to
// lightcloud for backward compatibility with existing stored settings.
func (s KnowledgeSettings) EffectiveProvider() string {
	switch strings.ToLower(strings.TrimSpace(s.Provider)) {
	case KnowledgeProviderWeKnora:
		return KnowledgeProviderWeKnora
	default:
		return KnowledgeProviderLightCloud
	}
}

func (s KnowledgeSettings) Public() KnowledgeSettings {
	s.TokenConfigured = strings.TrimSpace(s.Token) != ""
	s.Token = ""
	s.URL = publicKnowledgeURL(s.URL, s.EffectiveProvider())
	// A connection without an explicit provider inherits the top-level one, so
	// the redaction must judge it by that inherited provider too.
	topProvider := s.EffectiveProvider()
	if len(s.Connections) > 0 {
		conns := make([]KnowledgeConnection, len(s.Connections))
		for i, c := range s.Connections {
			c.TokenConfigured = strings.TrimSpace(c.Token) != ""
			c.Token = ""
			provider := c.EffectiveProvider()
			if strings.TrimSpace(c.Provider) == "" {
				provider = topProvider
			}
			c.URL = publicKnowledgeURL(c.URL, provider)
			conns[i] = c
		}
		s.Connections = conns
	}
	return s
}

// publicKnowledgeURL removes credentials that can hide in a URL query or
// fragment before settings are echoed to a browser. Only WeKnora base URLs are
// touched: a saved WeKnora URL never carries a query (it is rejected on
// write), while other providers may legitimately use one.
func publicKnowledgeURL(raw, provider string) string {
	if provider != KnowledgeProviderWeKnora {
		return raw
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	if u, err := url.Parse(raw); err == nil {
		u.RawQuery, u.ForceQuery, u.Fragment = "", false, ""
		return u.String()
	}
	if cut := strings.IndexAny(raw, "?#"); cut >= 0 {
		return raw[:cut]
	}
	return raw
}

// ActiveConnections returns all enabled connections with valid URL and Token.
// When Connections is non-empty, it returns only enabled connections (even if
// empty, so disabling all connections makes Ready() == false) and never falls
// back to legacy top-level URL/Token fields.
func (s KnowledgeSettings) ActiveConnections() []KnowledgeConnection {
	if !s.Enabled {
		return nil
	}
	if len(s.Connections) > 0 {
		out := make([]KnowledgeConnection, 0, len(s.Connections))
		for _, c := range s.Connections {
			if c.Enabled && strings.TrimSpace(c.URL) != "" && strings.TrimSpace(c.Token) != "" {
				out = append(out, c)
			}
		}
		return out
	}
	if strings.TrimSpace(s.URL) != "" && strings.TrimSpace(s.Token) != "" {
		return []KnowledgeConnection{{
			ID:       "default",
			Name:     "默认连接",
			Provider: s.EffectiveProvider(),
			URL:      s.URL,
			Token:    s.Token,
			Enabled:  true,
		}}
	}
	return nil
}

// Ready reports whether agents and platform features may call the upstream.
func (s KnowledgeSettings) Ready() bool {
	return len(s.ActiveConnections()) > 0
}

// ActiveConnectionsFingerprint returns a deterministic summary of enabled
// connections and settings timestamp for cache invalidation.
func (s KnowledgeSettings) ActiveConnectionsFingerprint() string {
	conns := s.ActiveConnections()
	var b strings.Builder
	b.WriteString(strconv.FormatBool(s.Enabled))
	b.WriteByte('|')
	if !s.UpdatedAt.IsZero() {
		b.WriteString(strconv.FormatInt(s.UpdatedAt.UTC().UnixNano(), 10))
	}
	b.WriteByte('|')
	for i, c := range conns {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(c.ID)
		b.WriteByte(':')
		b.WriteString(c.EffectiveProvider())
		b.WriteByte(':')
		b.WriteString(strings.TrimSpace(c.URL))
	}
	return b.String()
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

// KnowledgeCapability enumerates the WeKnora REST management capabilities a
// connection may expose. Probes use sentinel ids so the set can be discovered
// without touching real knowledge bases or documents.
type KnowledgeCapability string

const (
	CapKBManage        KnowledgeCapability = "kb_manage"        // 库创建/改名/删除
	CapDocWrite        KnowledgeCapability = "doc_write"        // 文档上传/URL/手工创建/更新/删除
	CapChunkRead       KnowledgeCapability = "chunk_read"       // 分块列表读取
	CapPreview         KnowledgeCapability = "preview"          // 原文在线预览
	CapDownload        KnowledgeCapability = "download"         // 原文下载
	CapPreviewDownload KnowledgeCapability = "preview_download" // preview/download 聚合，兼容旧控制台
	CapReparse         KnowledgeCapability = "reparse"          // 文档重解析
)

// KnowledgeCapabilities is the full probe order exposed to consoles. The
// aggregate preview_download entry is kept for consoles written against the
// older contract; preview and download report independently so a permission
// difference between the two routes disables only the failing action.
var KnowledgeCapabilities = []KnowledgeCapability{
	CapKBManage, CapDocWrite, CapChunkRead, CapPreview, CapDownload, CapPreviewDownload, CapReparse,
}

// CapabilityState is the tri-state result of one capability probe. Unknown
// means the probe itself was inconclusive (timeout/5xx) and must never be
// treated as a hard failure of the connection.
type CapabilityState string

const (
	CapabilityAvailable   CapabilityState = "available"
	CapabilityUnavailable CapabilityState = "unavailable"
	CapabilityUnknown     CapabilityState = "unknown"
)

// CapabilityStatus carries the probe verdict and a credential-free reason.
type CapabilityStatus struct {
	Status CapabilityState `json:"status"`
	Reason string          `json:"reason,omitempty"`
}

// ConnectionCapabilities is the cached capability set of one connection.
type ConnectionCapabilities struct {
	CheckedAt    string                                   `json:"checkedAt"`
	Capabilities map[KnowledgeCapability]CapabilityStatus `json:"capabilities"`
}

// KnowledgeDocumentDetail is the management view of one upstream document.
type KnowledgeDocumentDetail struct {
	ID              string `json:"id"`
	KnowledgeBaseID string `json:"knowledgeBaseId"`
	Title           string `json:"title"`
	Type            string `json:"type,omitempty"`
	FileName        string `json:"fileName,omitempty"`
	FileType        string `json:"fileType,omitempty"`
	ParseStatus     string `json:"parseStatus,omitempty"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
	CreatedAt       string `json:"createdAt,omitempty"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

// KnowledgeChunk is one parsed passage of a document.
type KnowledgeChunk struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	ChunkIndex int    `json:"chunkIndex"`
	StartAt    int    `json:"startAt"`
	EndAt      int    `json:"endAt"`
}

// KnowledgeChunkList is a page of document chunks.
type KnowledgeChunkList struct {
	Chunks   []KnowledgeChunk `json:"chunks"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
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
