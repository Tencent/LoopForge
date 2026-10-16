// Package knowledge integrates the upstream lightcloud knowledge base (kb)
// through its Streamable HTTP MCP. The platform holds the upstream token;
// agents only reach the read-only subset through the platform proxy.
package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
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

var ErrNotConfigured = errors.New("知识库未接入：请在「知识库 → 接入配置」填写地址和 Token 并启用")

type Source struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	IndexStatus string `json:"index_status,omitempty"`
	DocCount    int    `json:"doc_count"`
}

type SearchHit struct {
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

// Client calls the upstream kb MCP with the platform's credentials.
type Client struct {
	mcp *mcpclient.Client
}

var (
	clientsMu sync.Mutex
	clients   = map[string]*mcpclient.Client{}
)

// NewClient returns a client for the stored settings. Clients are cached per
// URL+token so the MCP initialize handshake is not repeated on every call.
func NewClient(settings domain.KnowledgeSettings) (*Client, error) {
	url, token := strings.TrimSpace(settings.URL), strings.TrimSpace(settings.Token)
	if url == "" || token == "" {
		return nil, ErrNotConfigured
	}
	key := url + "\x00" + token
	clientsMu.Lock()
	defer clientsMu.Unlock()
	c, ok := clients[key]
	if !ok {
		c = mcpclient.New(url, map[string]string{"Authorization": "Bearer " + token}).WithTimeout(30 * time.Second)
		clients[key] = c
	}
	return &Client{mcp: c}, nil
}

// Call forwards one tool call and returns the upstream text payload (JSON).
func (c *Client) Call(ctx context.Context, tool string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	return c.mcp.CallToolContext(ctx, tool, args)
}

func (c *Client) Tools(ctx context.Context) ([]mcpclient.Tool, error) {
	return c.mcp.ListToolsContext(ctx)
}

func (c *Client) ListSources(ctx context.Context) ([]Source, error) {
	var out struct {
		Sources []Source `json:"sources"`
	}
	if err := c.callJSON(ctx, ToolListSources, nil, &out); err != nil {
		return nil, err
	}
	if out.Sources == nil {
		out.Sources = []Source{}
	}
	return out.Sources, nil
}

func (c *Client) Search(ctx context.Context, query string, sources []string) (SearchResult, error) {
	args := map[string]any{"query": query}
	if len(sources) > 0 {
		args["sources"] = sources
	}
	var out SearchResult
	if err := c.callJSON(ctx, ToolSearch, args, &out); err != nil {
		return SearchResult{}, err
	}
	if out.Results == nil {
		out.Results = []SearchHit{}
	}
	return out, nil
}

func (c *Client) Fetch(ctx context.Context, source, path string, start, end int) (Document, error) {
	args := map[string]any{"source": source, "path": path}
	if start > 0 {
		args["start_line"] = start
	}
	if end > 0 {
		args["end_line"] = end
	}
	var out Document
	err := c.callJSON(ctx, ToolFetch, args, &out)
	return out, err
}

func (c *Client) WriteDocument(ctx context.Context, source, path, content string) (WriteResult, error) {
	var out WriteResult
	err := c.callJSON(ctx, ToolWrite, map[string]any{"source": source, "path": path, "content": content}, &out)
	return out, err
}

func (c *Client) callJSON(ctx context.Context, tool string, args map[string]any, out any) error {
	text, err := c.Call(ctx, tool, args)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("知识库 %s 返回无法解析：%s", tool, clip(text, 200))
	}
	return nil
}

// ScopeSources intersects the caller's requested sources with the project's
// allowed list, mirroring the upstream token scope rules: an empty allowed
// list is unrestricted, an empty request expands to the allowed list, and any
// explicit source outside the list is rejected.
func ScopeSources(allowed, requested []string) ([]string, error) {
	requested = cleanList(requested)
	allowed = cleanList(allowed)
	if len(allowed) == 0 {
		return requested, nil
	}
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
	if len(allowed) == 0 {
		return true
	}
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
