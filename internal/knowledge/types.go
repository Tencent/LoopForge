package knowledge

import (
	"errors"
	"fmt"
	"strings"
)

const (
	ToolSearch      = "kb_search"
	ToolFetch       = "kb_fetch"
	ToolListSources = "kb_list_sources"
	ToolGetSource   = "kb_get_source"
	ToolWrite       = "kb_write_document"
)

// ReadOnlyTools is the only upstream surface exposed to agents.
var ReadOnlyTools = []string{ToolSearch, ToolFetch, ToolListSources, ToolGetSource}

func IsReadOnlyTool(name string) bool {
	for _, tool := range ReadOnlyTools {
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
	Source    string `json:"source"`
	Commit    string `json:"commit"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	LineCount int    `json:"line_count"`
	Lines     []Line `json:"lines"`
	Citation  string `json:"citation"`
}

type WriteResult struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Commit string `json:"commit"`
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
