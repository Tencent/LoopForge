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
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/knowledge"

	"github.com/gin-gonic/gin"
)

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
		caller := knowledge.Caller{
			ProjectID: claims.ProjectID, AgentID: claims.AgentID, UserID: claims.UserID, RunID: claims.RunID,
		}
		text, _, err := h.callScoped(ctx, caller, name, params.Arguments)
		log.Printf("knowledge-proxy tool=%s project=%s agent=%s run=%s ok=%t latency=%dms", name, claims.ProjectID, claims.AgentID, claims.RunID, err == nil, time.Since(started).Milliseconds())
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
func (h *knowledgeHandlers) callScoped(ctx context.Context, caller knowledge.Caller, tool string, args map[string]any) (string, int, error) {
	return callKnowledgeTool(ctx, h.svc, caller, tool, args)
}

// callKnowledgeTool is the single implementation of the read-only knowledge
// tool contract. The SOP proxy and Main Bot's in-process tools both call it,
// so project narrowing, JSON shapes and activity records cannot drift apart.
func callKnowledgeTool(ctx context.Context, svc *knowledge.Service, caller knowledge.Caller, tool string, args map[string]any) (text string, hits int, err error) {
	started := time.Now()
	defer func() {
		if svc == nil {
			return
		}
		activity := domain.KnowledgeActivity{
			At: started.UTC(), Tool: tool, ProjectID: caller.ProjectID, AgentID: caller.AgentID,
			UserID: caller.UserID, RunID: caller.RunID, SessionID: caller.SessionID,
			Query: truncateRunes(fmt.Sprint(valueOr(args["query"], "")), 120), Hits: hits,
			OK: err == nil, LatencyMS: time.Since(started).Milliseconds(),
		}
		if err != nil {
			activity.Error = truncateRunes(err.Error(), 200)
		}
		recordKnowledgeActivity(svc, activity)
	}()
	if svc == nil {
		return "", 0, knowledge.ErrNotConfigured
	}
	if args == nil {
		args = map[string]any{}
	}
	var value any
	str := func(k string) string { return fmt.Sprint(valueOr(args[k], "")) }
	integer := func(k string) int { n, _ := strconv.Atoi(str(k)); return n }
	switch tool {
	case knowledge.ToolSearch:
		var out knowledge.SearchOutput
		out, err = svc.Search(ctx, caller.ProjectID, str("query"), stringList(args["sources"]))
		hits = len(out.Result.Results)
		if err == nil && hits == 0 {
			// 空结果直接给 Agent 一句中文说明，避免模型看到空数组后用
			// 英文自行编造“No results / Try shorter keywords”之类的提示。
			value = map[string]any{
				"query": out.Result.Query, "mode": out.Result.Mode,
				"results": []knowledge.SearchHit{}, "message": "没有命中。换更短或更具体的关键词再试。",
			}
		} else {
			value = out.Result
		}
	case knowledge.ToolFetch:
		value, err = svc.DocumentVersion(ctx, caller.ProjectID, str("source"), str("path"), str("commit"), integer("start_line"), integer("end_line"))
	case knowledge.ToolListSources:
		var list []knowledge.SourceView
		list, err = svc.Sources(ctx, caller.ProjectID)
		kept := []knowledge.Source{}
		for _, src := range list {
			if src.Allowed {
				kept = append(kept, src.Source)
			}
		}
		value = map[string]any{"sources": kept}
		hits = len(kept)
	case knowledge.ToolGetSource:
		value, err = svc.SourceDetail(ctx, caller.ProjectID, str("source"))
	default:
		return "", 0, fmt.Errorf("unknown knowledge tool")
	}
	if err != nil {
		return "", 0, err
	}
	raw, err := json.Marshal(value)
	return string(raw), hits, err
}

// recordKnowledgeActivity logs a failed write and never changes the tool
// result: the 知识中心 panel is diagnostic, not part of the tool contract.
func recordKnowledgeActivity(svc *knowledge.Service, activity domain.KnowledgeActivity) {
	if err := svc.RecordActivity(activity); err != nil {
		log.Printf("knowledge-activity write failed: tool=%s project=%s err=%v", activity.Tool, activity.ProjectID, err)
	}
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
