package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	RunID     string    `json:"runId,omitempty"`
	NodeID    string    `json:"nodeId,omitempty"`
	ServerID  string    `json:"serverId,omitempty"`
	Origin    string    `json:"origin,omitempty"`
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
	settings := h.repo.KnowledgeSettings()

	raw, _ := bearerToken(c.Request)
	claims, err := knowledge.VerifyProxyToken(settings, raw, time.Now())
	if err != nil {
		c.Header("WWW-Authenticate", `Bearer realm="AgentFlow Knowledge"`)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid knowledge token"})
		return
	}
	if (claims.Ref == nil || claims.Ref.ServerID == domain.KnowledgeMCPServerID) && !settings.Ready() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": knowledge.ErrNotConfigured.Error()})
		return
	}
	if claims.Ref != nil || (claims.RunID != "" && claims.NodeID != "") {
		run, ok := h.repo.RunState(claims.RunID)
		nr := run.Nodes[claims.NodeID]
		if !ok || nr == nil || nr.Status != "running" || nr.Attempt != claims.Attempt || run.ExecutionContext.ProjectID != claims.ProjectID || run.ExecutionContext.ResponsibleUserID != claims.UserID {
			c.JSON(403, gin.H{"error": "节点执行身份已失效"})
			return
		}
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
	response := h.handleProxyRPC(c.Request.Context(), settings, claims, request)
	if response == nil {
		c.Status(http.StatusAccepted)
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *knowledgeHandlers) handleProxyRPC(ctx context.Context, settings domain.KnowledgeSettings, claims knowledge.ProxyClaims, request agentToolRPCRequest) *agentToolRPCResponse {
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
		instructions := "团队知识库（只读）。先 kb_search 检索，再用 kb_fetch 读原文；采用结论时请在引用处紧跟输出工具返回中的 [[knowledge:<id>]] 标记（勿放入反引号）。"
		if knowledge.HasWeKnoraConnection(settings) {
			instructions = "团队知识库（WeKnora，只读）。先 list_knowledge_bases 或 search_knowledge 检索，再用 read_document 读原文；采用结论时请在引用处紧跟输出工具返回中的 [[knowledge:<id>]] 标记（勿放入反引号）。"
		}
		if claims.Ref != nil && claims.Ref.ServerID != domain.KnowledgeMCPServerID {
			serverName, _ := knowledgeServerMeta(h.deps, claims.Ref.ServerID)
			instructions = "用户为当前 SOP 节点绑定的只读知识服务（" + serverName + "）。请优先使用本服务的检索与文档读取工具核实团队规范、架构、接口与排障资料，采用结论时请在引用处紧跟输出工具返回中的 [[knowledge:<id>]] 标记（勿放入反引号）。"
		}
		return reply(map[string]any{
			"protocolVersion": agentToolProtocolVersion(request.Params),
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": domain.KnowledgeMCPServerID, "version": "1.0.0"},
			"instructions":    instructions,
		})
	case "ping":
		return reply(map[string]any{})
	case "tools/list":
		if claims.Ref != nil {
			tools, _, err := referenceTools(ctx, h.deps, claims.ProjectID, claims.UserID, *claims.Ref)
			if err != nil {
				return fail(-32603, err.Error())
			}
			serverName, serverDesc := knowledgeServerMeta(h.deps, claims.Ref.ServerID)
			out := make([]map[string]any, 0, len(tools))
			for _, tool := range tools {
				item := map[string]any{
					"name":        tool.Name,
					"description": knowledgeToolDescription(serverName, serverDesc, claims.Ref.ServerID, tool.Name, tool.Description),
					"inputSchema": json.RawMessage(defaultToolSchema(tool.InputSchema)),
				}
				if len(tool.Annotations) > 0 && string(tool.Annotations) != "null" {
					item["annotations"] = tool.Annotations
				}
				out = append(out, item)
			}
			return reply(map[string]any{"tools": out})
		}
		tools, err := h.proxyTools(ctx, settings)
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
		if i := strings.LastIndex(name, "__"); claims.Ref == nil && i >= 0 {
			name = name[i+2:]
		}
		if claims.Ref == nil {
			hasWeKnora := knowledge.HasWeKnoraConnection(settings)
			allowedTool := knowledge.IsReadOnlyTool(name) || (hasWeKnora && knowledge.IsWeKnoraReadOnlyTool(name))
			if !allowedTool {
				return fail(-32602, fmt.Sprintf("知识库工具 %s 不可用：Agent 只能只读检索，新知识请写入 knowledge.md 等待审核", params.Name))
			}
		}
		started := time.Now()
		var execRes knowledge.ExecutionResult
		var err error
		serverID := domain.KnowledgeMCPServerID
		if claims.Ref != nil {
			serverID = claims.Ref.ServerID
			execRes, err = callKnowledgeReferenceWithOrigin(ctx, h.deps, claims.ProjectID, claims.UserID, *claims.Ref, name, params.Arguments, domain.KnowledgeCallOriginModelTool)
		} else {
			execRes, err = h.svc.ExecuteTool(ctx, claims.ProjectID, name, params.Arguments, knowledge.ExecuteOptions{})
		}
		text := execRes.Text
		hits := execRes.Hits
		query := knowledgeCallQuerySummary(params.Arguments)
		activity := knowledgeActivity{
			RunID: claims.RunID, NodeID: claims.NodeID, ServerID: serverID, Origin: domain.KnowledgeCallOriginModelTool, At: started.UTC(), Tool: name, ProjectID: claims.ProjectID, AgentID: claims.AgentID, UserID: claims.UserID,
			Query: query, Hits: hits,
			OK: err == nil, LatencyMS: time.Since(started).Milliseconds(),
		}
		if err != nil {
			activity.Error = truncateRunes(err.Error(), 200)
		}
		h.activity.add(activity)
		if claims.RunID != "" && claims.NodeID != "" && h.engine != nil {
			read, result := knowledgeReadResultWithMeta(serverID, name, text, hits, execRes.Status, execRes.Warning, domain.KnowledgeCallOriginModelTool, err)
			text = result
			read.Query = query
			read.Attempt = claims.Attempt
			h.engine.RecordKnowledgeRead(claims.RunID, claims.NodeID, read)
			state := read.Status
			attrs := map[string]any{
				"knowledgeReadId":            read.ID,
				"origin":                     domain.KnowledgeCallOriginModelTool,
				"loopforge.knowledge.origin": domain.KnowledgeCallOriginModelTool,
				"attempt":                    claims.Attempt,
				"ok":                         err == nil,
				"hits":                       hits,
				"latencyMs":                  activity.LatencyMS,
			}
			if query != "" {
				attrs["query"] = query
			}
			if len(read.Targets) > 0 {
				attrs["targets"] = read.Targets
			}
			h.engine.IngestOTLPTraces([]domain.TelemetryEvent{{RunID: claims.RunID, NodeID: claims.NodeID, Category: "tool", Name: "knowledge.call", Phase: "finish", Message: serverID + " · " + name + " · " + state, Attributes: attrs}})
		}
		log.Printf("knowledge-proxy origin=%s tool=%s project=%s agent=%s ok=%t latency=%dms", domain.KnowledgeCallOriginModelTool, name, claims.ProjectID, claims.AgentID, err == nil, activity.LatencyMS)
		if err != nil {
			return reply(mcpToolTextResult(err.Error(), true))
		}
		return reply(mcpToolTextResult(text, false))
	default:
		return fail(-32601, "method not found")
	}
}

func (h *knowledgeHandlers) proxyTools(ctx context.Context, settings domain.KnowledgeSettings) ([]map[string]any, error) {
	tools, err := knowledge.ReadOnlyToolsForSettings(ctx, settings)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
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
func (h *knowledgeHandlers) callScoped(ctx context.Context, _ domain.KnowledgeSettings, projectID, tool string, args map[string]any) (string, int, error) {
	res, err := h.svc.ExecuteTool(ctx, projectID, tool, args, knowledge.ExecuteOptions{})
	if err != nil {
		return "", 0, err
	}
	return res.Text, res.Hits, nil
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

func intArg(v any, fallback int) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n)
		}
	}
	return fallback
}
