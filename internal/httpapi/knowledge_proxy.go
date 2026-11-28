package httpapi

import (
	"context"
	"encoding/json"
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

// knowledgeActivity records recent proxied agent tool calls in memory.
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
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, item)
	if over := len(l.items) - knowledgeActivityLimit; over > 0 {
		l.items = append([]knowledgeActivity(nil), l.items[over:]...)
	}
}

// 路由: Any(knowledge.ProxyPath, api.know.proxyMCP)
//
// Stateless Streamable HTTP MCP for SOP/Runner agents. Verifies the signed
// afkb1. proxy token, checks running node ownership & attempt liveness, and
// delegates read-only execution strictly to the token owner's Connector.
func (h *knowledgeHandlers) proxyMCP(c *gin.Context) {
	raw, _ := bearerToken(c.Request)
	claims, err := knowledge.VerifyProxyToken(raw, time.Now())
	if err != nil {
		c.Header("WWW-Authenticate", `Bearer realm="AgentFlow Knowledge"`)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid knowledge token"})
		return
	}
	if strings.TrimSpace(claims.RunID) == "" || strings.TrimSpace(claims.NodeID) == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "节点执行身份已失效"})
		return
	}
	run, ok := h.repo.RunState(claims.RunID)
	nr := run.Nodes[claims.NodeID]
	if !ok || nr == nil || nr.Status != "running" || nr.Attempt != claims.Attempt || run.ExecutionContext.ProjectID != claims.ProjectID || run.ExecutionContext.ResponsibleUserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "节点执行身份已失效"})
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
	exec := h.executor()
	switch request.Method {
	case "initialize":
		connName, connType := knowledgeServerMeta(h.deps, claims.UserID, claims.Ref.ConnectorID)
		instructions := "用户为当前任务绑定的只读知识连接器（" + connName + "）。请先检索与读取原文再下结论，采用事实时在引用处紧跟输出直接支撑该句事实的 [[knowledge:<id>]] 标记（勿放入反引号，不漏标不多标）。"
		if domain.IsNativeRESTConnectorType(connType) {
			instructions = "个人知识连接器（" + connName + " · " + connType + "，只读）。先 list_knowledge_bases 或 search_knowledge 检索，再用 read_document 读原文；采用结论时在引用处紧跟输出直接支撑该句事实的 [[knowledge:<id>]] 标记（勿放入反引号，不漏标不多标）。"
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
		if claims.Ref == nil {
			return fail(-32602, "missing knowledge connector reference")
		}
		conn, tools, err := exec.ReferenceTools(ctx, claims.UserID, *claims.Ref)
		if err != nil {
			log.Printf("knowledge-proxy-tools connector=%s user=%d run=%s node=%s ok=false err=%q",
				claims.Ref.ConnectorID, claims.UserID, claims.RunID, claims.NodeID, err.Error())
			return fail(-32603, err.Error())
		}
		if len(tools) == 0 || knowledge.DebugEnabled() {
			log.Printf("knowledge-proxy-tools connector=%s type=%s user=%d run=%s node=%s ok=true tools=%d",
				conn.ID, conn.Type, claims.UserID, claims.RunID, claims.NodeID, len(tools))
		}
		out := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			item := map[string]any{
				"name":        tool.Name,
				"description": knowledgeToolDescription(conn.Name, conn.Type, conn.ID, tool.Name, tool.Description),
				"inputSchema": json.RawMessage(defaultToolSchema(tool.InputSchema)),
			}
			if len(tool.Annotations) > 0 && string(tool.Annotations) != "null" {
				item["annotations"] = tool.Annotations
			}
			out = append(out, item)
		}
		return reply(map[string]any{"tools": out})
	case "tools/call":
		if claims.Ref == nil {
			return fail(-32602, "missing knowledge connector reference")
		}
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return fail(-32602, err.Error())
		}
		name := strings.TrimSpace(params.Name)
		if exec.ToolMayWrite(ctx, claims.UserID, *claims.Ref, name) {
			return reply(mcpToolTextResult("SOP 无人值守运行未开放只读工具外的写入操作（"+name+"），请走知识沉淀审核队列", true))
		}
		started := time.Now()
		var priorReads []domain.KnowledgeRead
		if claims.RunID != "" && claims.NodeID != "" && h.engine != nil {
			priorReads = h.engine.NodeKnowledgeReads(claims.RunID, claims.NodeID, claims.Attempt)
		}
		caller := h.buildSOPUsageCaller(claims)
		read, text, err := exec.ExecuteReferenceToolWithHistory(knowledge.WithUsageCaller(ctx, caller), claims.UserID, *claims.Ref, name, params.Arguments, domain.KnowledgeCallOriginModelTool, priorReads)
		read.Query = knowledge.KnowledgeCallQuerySummary(params.Arguments)
		read.Attempt = claims.Attempt

		hits := len(read.Evidences)
		if hits == 0 {
			hits = len(read.Targets)
		}
		activity := knowledgeActivity{
			RunID: claims.RunID, NodeID: claims.NodeID, ServerID: claims.Ref.ConnectorID,
			Origin: domain.KnowledgeCallOriginModelTool, At: started.UTC(), Tool: name,
			ProjectID: claims.ProjectID, AgentID: claims.AgentID, UserID: claims.UserID,
			Query: read.Query, Hits: hits, OK: err == nil, LatencyMS: time.Since(started).Milliseconds(),
		}
		if err != nil {
			activity.Error = truncateRunes(err.Error(), 200)
		}
		h.activity.add(activity)
		if claims.RunID != "" && claims.NodeID != "" && h.engine != nil {
			h.engine.RecordKnowledgeRead(claims.RunID, claims.NodeID, read)
			attrs := map[string]any{
				"knowledgeReadId":            read.ID,
				"origin":                     domain.KnowledgeCallOriginModelTool,
				"loopforge.knowledge.origin": domain.KnowledgeCallOriginModelTool,
				"attempt":                    claims.Attempt,
				"ok":                         err == nil,
				"hits":                       hits,
				"latencyMs":                  activity.LatencyMS,
			}
			if read.Query != "" {
				attrs["query"] = read.Query
			}
			if len(read.Targets) > 0 {
				attrs["targets"] = read.Targets
			}
			h.engine.IngestOTLPTraces([]domain.TelemetryEvent{{
				RunID: claims.RunID, NodeID: claims.NodeID, Category: "tool", Name: "knowledge.call",
				Phase: "finish", Message: claims.Ref.ConnectorID + " · " + name + " · " + read.Status, Attributes: attrs,
			}})
		}
		log.Printf("knowledge-proxy origin=%s tool=%s connector=%s user=%d ok=%t latency=%dms", domain.KnowledgeCallOriginModelTool, name, claims.Ref.ConnectorID, claims.UserID, err == nil, activity.LatencyMS)
		if err != nil {
			return reply(mcpToolTextResult(err.Error(), true))
		}
		return reply(mcpToolTextResult(text, false))
	default:
		return fail(-32601, "method not found")
	}
}

func (h *knowledgeHandlers) buildSOPUsageCaller(claims knowledge.ProxyClaims) domain.KnowledgeUsageCaller {
	caller := domain.KnowledgeUsageCaller{
		Kind:      domain.KnowledgeCallerSOP,
		RunID:     strings.TrimSpace(claims.RunID),
		NodeID:    strings.TrimSpace(claims.NodeID),
		AgentID:   strings.TrimSpace(claims.AgentID),
		ProjectID: strings.TrimSpace(claims.ProjectID),
	}
	if h == nil || h.repo == nil {
		return caller
	}
	if caller.RunID != "" {
		if run, ok := h.repo.RunState(caller.RunID); ok {
			caller.WorkflowID = strings.TrimSpace(run.WorkflowID)
			caller.WorkflowName = strings.TrimSpace(run.WorkflowSnapshot.Name)
			if caller.ProjectID == "" {
				caller.ProjectID = strings.TrimSpace(run.ExecutionContext.ProjectID)
			}
			for _, node := range run.WorkflowSnapshot.Nodes {
				if node.ID == caller.NodeID {
					caller.NodeName = strings.TrimSpace(node.Name)
					if caller.AgentID == "" {
						caller.AgentID = strings.TrimSpace(node.AgentID)
					}
					break
				}
			}
		}
	}
	if caller.AgentID != "" {
		if agent, ok := h.repo.Agent(caller.AgentID); ok {
			caller.AgentName = strings.TrimSpace(agent.Name)
		}
	}
	return caller
}
