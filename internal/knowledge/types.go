package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	ToolSearch      = "kb_search"
	ToolFetch       = "kb_fetch"
	ToolGrep        = "kb_grep"
	ToolListSources = "kb_list_sources"
	ToolGetSource   = "kb_get_source"
	ToolReportIssue = "kb_report_issue"
	ToolWrite       = "kb_write_document"
)

// ReadOnlyTools 是只读检索/读取工具。Agent 还可使用 ToolReportIssue 记录
// 文档问题；它写反馈但不改正文，因此不放进只读列表。
var ReadOnlyTools = []string{ToolSearch, ToolFetch, ToolGrep, ToolListSources, ToolGetSource}

// AgentTools 是暴露给 SOP 代理和 Main Bot 的全部知识库工具。
var AgentTools = []string{ToolSearch, ToolFetch, ToolGrep, ToolListSources, ToolGetSource, ToolReportIssue}

func IsReadOnlyTool(name string) bool {
	for _, tool := range ReadOnlyTools {
		if tool == name {
			return true
		}
	}
	return false
}

func IsAgentTool(name string) bool {
	for _, tool := range AgentTools {
		if tool == name {
			return true
		}
	}
	return false
}

var ErrNotConfigured = errors.New("内置知识引擎不可用")

// BaseOrigin identifies the knowledge layer without changing versioned citations.
type BaseOrigin struct {
	BaseID   string `json:"base_id,omitempty"`
	BaseName string `json:"base_name,omitempty"`
	Layer    string `json:"layer,omitempty"`
}

type Source struct {
	BaseOrigin
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	IndexStatus string `json:"index_status,omitempty"`
	DocCount    int    `json:"doc_count"`
}

type SearchHit struct {
	BaseOrigin
	Source    string  `json:"source"`
	Commit    string  `json:"commit"`
	Path      string  `json:"path"`
	Title     string  `json:"title,omitempty"`
	Heading   string  `json:"heading,omitempty"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Snippet   string  `json:"snippet"`
	Score     float64 `json:"score"`
	Citation  string  `json:"citation"`
	URL       string  `json:"url,omitempty"`
	// DocStatus 是索引时从 frontmatter 解析的生命周期状态。空值表示没有
	// 标注；检索后处理据此降权废弃、移除归档。
	DocStatus string `json:"doc_status,omitempty"`
}

type SearchResult struct {
	Query         string      `json:"query"`
	Mode          string      `json:"mode"`
	Degraded      bool        `json:"degraded"`
	DegradeReason string      `json:"degrade_reason,omitempty"`
	Results       []SearchHit `json:"results"`
}

type Line struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

type Document struct {
	BaseOrigin
	Source         string `json:"source"`
	Commit         string `json:"commit"`
	Path           string `json:"path"`
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	LineCount      int    `json:"line_count"`
	Lines          []Line `json:"lines"`
	Citation       string `json:"citation"`
	URL            string `json:"url,omitempty"`
	Freshness      string `json:"freshness,omitempty"`
	CurrentVersion string `json:"current_version,omitempty"`
}

type WriteResult struct {
	Source     string `json:"source"`
	Path       string `json:"path"`
	Commit     string `json:"commit"`
	Branch     string `json:"branch,omitempty"`
	MRURL      string `json:"mrUrl,omitempty"`
	MRProvider string `json:"mrProvider,omitempty"`
	MRStatus   string `json:"mrStatus,omitempty"`
	Message    string `json:"message,omitempty"`
}

// WritebackRequest 是审核条目发布到 Git 来源时需要的最小上下文。引擎只
// 用它记录写回、生成分支名和合并请求文案，不接收任何凭据字段。
type WritebackRequest struct {
	SubmissionID string
	ProjectID    string
	Source       string
	Path         string
	Content      string
	Title        string
}

// GitWritebacker 是内置引擎对 Git 来源的写回能力。Service 通过类型断言
// 使用它，保持 Upstream 五个只读方法不变。
type GitWritebacker interface {
	WriteGitDocument(ctx context.Context, req WritebackRequest) (WriteResult, error)
}

func ScopeSources(allowed, requested []string) ([]string, error) {
	requested = cleanList(requested)
	allowed = cleanList(allowed)

	if len(requested) == 0 {
		return allowed, nil
	}
	for _, s := range requested {
		if !SourceAllowed(allowed, s) {
			return nil, fmt.Errorf("知识库来源 %q 不在当前项目的授权范围", s)
		}
	}
	return requested, nil
}

func SourceAllowed(allowed []string, source string) bool {
	allowed = cleanList(allowed)

	source = strings.TrimSpace(source)
	for _, s := range allowed {
		if s == source {
			return true
		}
	}
	return false
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
