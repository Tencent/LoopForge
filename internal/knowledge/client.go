package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

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
		return nil, errLightCloudNotConfigured
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
		return fmt.Errorf("知识库 %s 返回无法解析", tool)
	}
	return nil
}

func (c *Client) GetSource(ctx context.Context, source string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.callJSON(ctx, ToolGetSource, map[string]any{"source": source}, &out)
	return out, err
}
