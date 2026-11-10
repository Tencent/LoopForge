package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	stdsync "sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"

	"github.com/gin-gonic/gin"
)

const knowledgeActivityLimit = 200

// knowledgeActivity is one proxied agent tool call, kept in memory so the
// 知识库 page can show which roles are actually using the knowledge base.
type knowledgeActivity struct {
	At        time.Time `json:"at"`
	Tool      string    `json:"tool"`
	ProjectID string    `json:"projectId,omitempty"`
	AgentID   string    `json:"agentId,omitempty"`
	UserID    int64     `json:"userId,omitempty"`
	Query     string    `json:"query,omitempty"`
	Hits      int       `json:"hits"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	LatencyMS int64     `json:"latencyMs"`
}

type knowledgeActivityLog struct {
	mu    stdsync.Mutex
	items []knowledgeActivity
}

func (l *knowledgeActivityLog) add(item knowledgeActivity) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, item)
	if over := len(l.items) - knowledgeActivityLimit; over > 0 {
		l.items = append([]knowledgeActivity(nil), l.items[over:]...)
	}
}

// recent returns newest first.
func (l *knowledgeActivityLog) recent() []knowledgeActivity {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]knowledgeActivity, 0, len(l.items))
	for i := len(l.items) - 1; i >= 0; i-- {
		out = append(out, l.items[i])
	}
	return out
}

// 路由: g.POST(knowledge.ProxyPath, h.proxyMCP)
//
// Stateless Streamable HTTP MCP for agents. Only the read-only upstream tools
// are listed or callable, and every call is narrowed to the project's sources.
func (h *knowledgeHandlers) proxyMCP(c *gin.Context) {
	raw, _ := bearerToken(c.Request)
	claims, err := knowledge.VerifyProxyToken(knowledge.SessionSecret(), raw, time.Now())
	if err != nil {
		c.Header("WWW-Authenticate", `Bearer realm="AgentFlow Knowledge"`)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid knowledge token"})
		return
	}
	if c.Request.Method != http.MethodPost {
		c.Header("Allow", "POST")
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "stateless Streamable HTTP accepts JSON-RPC POST requests"})
		return
	}
	var request agentToolRPCRequest
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		c.JSON(http.StatusBadRequest, agentToolRPCResponse{JSONRPC: "2.0", Error: &agentToolRPCError{Code: -32700, Message: "parse error", Data: err.Error()}})
		return
	}
	response := h.handleProxyRPC(c.Request.Context(), claims, request)
	if response == nil {
		c.Status(http.StatusAccepted)
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *knowledgeHandlers) handleProxyRPC(ctx context.Context, claims knowledge.ProxyClaims, request agentToolRPCRequest) *agentToolRPCResponse {
	if strings.HasPrefix(request.Method, "notifications/") {
		return nil
	}
	reply := func(result any) *agentToolRPCResponse {
		return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: result}
	}
	fail := func(code int, msg string) *agentToolRPCResponse {
		return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Error: &agentToolRPCError{Code: code, Message: msg}}
	}
	switch request.Method {
	case "initialize":
		return reply(map[string]any{
			"protocolVersion": agentToolProtocolVersion(request.Params),
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": domain.KnowledgeMCPServerID, "version": "1.0.0"},
			"instructions":    "团队知识库（只读）。先 kb_search 检索，再用 kb_fetch 读原文；引用时附上 citation。",
		})
	case "ping":
		return reply(map[string]any{})
	case "tools/list":
		tools, err := h.proxyTools(ctx)
		if err != nil {
			return fail(-32603, err.Error())
		}
		return reply(map[string]any{"tools": tools})
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return fail(-32602, err.Error())
		}
		name := strings.TrimSpace(params.Name)
		if i := strings.LastIndex(name, "__"); i >= 0 {
			name = name[i+2:]
		}
		if !knowledge.IsReadOnlyTool(name) {
			return fail(-32602, fmt.Sprintf("知识库工具 %s 不可用：Agent 只能只读检索，新知识请写入 knowledge.md 等待审核", params.Name))
		}
		started := time.Now()
		text, hits, err := h.callScoped(ctx, claims.ProjectID, name, params.Arguments)
		activity := knowledgeActivity{
			At: started.UTC(), Tool: name, ProjectID: claims.ProjectID, AgentID: claims.AgentID, UserID: claims.UserID,
			Query: truncateRunes(fmt.Sprint(valueOr(params.Arguments["query"], "")), 120), Hits: hits,
			OK: err == nil, LatencyMS: time.Since(started).Milliseconds(),
		}
		if err != nil {
			activity.Error = truncateRunes(err.Error(), 200)
		}
		h.activity.add(activity)
		log.Printf("knowledge-proxy tool=%s project=%s agent=%s ok=%t latency=%dms", name, claims.ProjectID, claims.AgentID, err == nil, activity.LatencyMS)
		if err != nil {
			return reply(mcpToolTextResult(err.Error(), true))
		}
		return reply(mcpToolTextResult(text, false))
	default:
		return fail(-32601, "method not found")
	}
}

func (h *knowledgeHandlers) proxyTools(ctx context.Context) ([]map[string]any, error) {
	upstream, err := h.svc.Tools(ctx)
	if err != nil {
		return nil, fmt.Errorf("知识库不可达：%w", err)
	}
	out := make([]map[string]any, 0, len(knowledge.ReadOnlyTools))
	for _, tool := range upstream {
		if !knowledge.IsReadOnlyTool(tool.Name) {
			continue
		}
		item := map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": json.RawMessage(defaultToolSchema(tool.InputSchema))}
		if len(tool.Annotations) > 0 && string(tool.Annotations) != "null" {
			item["annotations"] = tool.Annotations
		}
		out = append(out, item)
	}
	return out, nil
}

// callScoped applies the project's source scope to one read-only call and
// returns the upstream JSON text plus a hit count for activity reporting.
func (h *knowledgeHandlers) callScoped(ctx context.Context, projectID, tool string, args map[string]any) (string, int, error) {
	if args == nil {
		args = map[string]any{}
	}
	var value any
	var err error
	hits := 0
	str := func(k string) string { return fmt.Sprint(valueOr(args[k], "")) }
	integer := func(k string) int { n, _ := strconv.Atoi(str(k)); return n }
	switch tool {
	case knowledge.ToolSearch:
		var out knowledge.SearchOutput
		out, err = h.svc.Search(ctx, projectID, str("query"), stringList(args["sources"]))
		value = out.Result
		hits = len(out.Result.Results)
	case knowledge.ToolFetch:
		value, err = h.svc.DocumentVersion(ctx, projectID, str("source"), str("path"), str("commit"), integer("start_line"), integer("end_line"))
	case knowledge.ToolListSources:
		var list []knowledge.SourceView
		list, err = h.svc.Sources(ctx, projectID)
		kept := []knowledge.Source{}
		for _, src := range list {
			if src.Allowed {
				kept = append(kept, src.Source)
			}
		}
		value = map[string]any{"sources": kept}
		hits = len(kept)
	case knowledge.ToolGetSource:
		value, err = h.svc.SourceDetail(ctx, projectID, str("source"))
	default:
		return "", 0, fmt.Errorf("unknown knowledge tool")
	}
	if err != nil {
		return "", 0, err
	}
	raw, err := json.Marshal(value)
	return string(raw), hits, err
}

func stringList(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(list) == "" {
			return nil
		}
		return strings.Split(list, ",")
	}
	return nil
}

func valueOr(v, fallback any) any {
	if v == nil {
		return fallback
	}
	return v
}
