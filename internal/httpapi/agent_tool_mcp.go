package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/mcpserver"

	"github.com/gin-gonic/gin"
)

type agentToolGateway struct {
	*deps
}

type agentToolRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type agentToolRPCResponse struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      any                `json:"id"`
	Result  any                `json:"result,omitempty"`
	Error   *agentToolRPCError `json:"error,omitempty"`
}

type agentToolRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *Server) handleAgentToolMCP(c *gin.Context) {
	(&agentToolGateway{deps: s.deps}).ServeHTTP(c)
}

func (h *agentToolGateway) ServeHTTP(c *gin.Context) {
	token := strings.TrimSpace(c.Param("token"))
	session, ok := agentToolSessions.get(token)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid agent tool session"})
		return
	}
	if c.Request.Method == http.MethodDelete {
		session.Close()
		c.Status(http.StatusNoContent)
		return
	}
	if c.Request.Method != http.MethodPost {
		c.Header("Allow", "POST, DELETE")
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "stateless Streamable HTTP accepts JSON-RPC POST requests"})
		return
	}
	var request agentToolRPCRequest
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 4<<20))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		c.JSON(http.StatusBadRequest, agentToolRPCResponse{JSONRPC: "2.0", Error: &agentToolRPCError{Code: -32700, Message: "parse error", Data: err.Error()}})
		return
	}
	response := h.handleRPC(c.Request.Context(), session, request)
	if response == nil {
		c.Status(http.StatusAccepted)
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *agentToolGateway) handleRPC(ctx context.Context, session *AgentToolSession, request agentToolRPCRequest) *agentToolRPCResponse {
	if strings.HasPrefix(request.Method, "notifications/") {
		return nil
	}
	switch request.Method {
	case "initialize":
		return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{
			"protocolVersion": agentToolProtocolVersion(request.Params),
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": agentToolSessionMCPName, "version": "1.0.0"},
			"instructions":    agentToolInstructions(session.Mode),
		}}
	case "ping":
		return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{}}
	case "tools/list":
		return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"tools": h.toolList(ctx, session)}}
	case "tools/call":
		result, err := h.callTool(ctx, session, request.Params)
		if err != nil {
			return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Error: &agentToolRPCError{Code: -32602, Message: err.Error()}}
		}
		return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: result}
	default:
		return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Error: &agentToolRPCError{Code: -32601, Message: "method not found", Data: request.Method}}
	}
}

func agentToolProtocolVersion(params json.RawMessage) string {
	var payload struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &payload)
	switch payload.ProtocolVersion {
	case "2024-11-05", "2025-03-26", "2025-06-18":
		return payload.ProtocolVersion
	default:
		return "2025-06-18"
	}
}

func agentToolInstructions(mode AgentToolMode) string {
	switch mode {
	case AgentToolReport:
		return "只能基于提供的资料汇总本次 SOP，完成后调用 submit_notification_report。普通文本不算提交。"
	case AgentToolClarify:
		return "只读查询 LoopForge 及当前项目已安装的外部 MCP。本轮结束时调用 submit_clarification。"
	case AgentToolAssistant:
		return "只读查询平台和外部 MCP。写入放进 submit_assistant_reply 的草稿，用户确认后才保存。普通问答和只读查询直接用自然语言回复，无需调用提交工具。"
	case AgentToolScheduled:
		return "使用当前项目和个人已安装的 MCP 以及 agentflow__* 直接读写，写入立即生效。完成后用自然语言汇报。"
	default:
		return "AgentFlow session MCP"
	}
}

func (h *agentToolGateway) toolList(ctx context.Context, session *AgentToolSession) []map[string]any {
	catalog := h.buildCatalog(ctx, session)
	out := make([]map[string]any, 0, len(catalog))
	for _, tool := range catalog {
		item := map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": json.RawMessage(defaultToolSchema(tool.InputSchema)),
		}
		if len(tool.OutputSchema) > 0 && string(tool.OutputSchema) != "null" {
			item["outputSchema"] = json.RawMessage(tool.OutputSchema)
		}
		if len(tool.Annotations) > 0 && string(tool.Annotations) != "null" {
			item["annotations"] = json.RawMessage(tool.Annotations)
		}
		out = append(out, item)
	}
	return out
}

func (h *agentToolGateway) callTool(ctx context.Context, session *AgentToolSession, raw json.RawMessage) (map[string]any, error) {
	if session.finished() {
		return nil, errAgentToolTerminalCommitted
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	name := stripAgentToolServerPrefix(params.Name)
	logAgentToolEvent("call-start", "mode="+string(session.Mode), "tool="+name)
	if name == submitClarificationTool || name == submitAssistantReplyTool || name == submitNotificationReportTool {
		return h.callTerminal(session, name, params.Arguments)
	}
	if session.terminalToolsOnly() {
		return nil, errAgentToolForbidden
	}
	if name == "workbench_tool_schema" && session.Mode == AgentToolAssistant {
		var input struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params.Arguments, &input); err != nil {
			return nil, err
		}
		op, ok := assistantPublicOperations[strings.TrimPrefix(input.Name, "agentflow__")]
		if !ok {
			return nil, errAgentToolUnknown
		}
		body, _ := json.Marshal(map[string]any{"name": op.Name, "description": op.Description, "inputSchema": mcpserver.ToolInputSchema(op)})
		return map[string]any{"content": []map[string]any{{"type": "text", "text": string(body)}}}, nil
	}
	if name == "workbench_attachment_read" {
		return readWorkbenchAttachmentTool(session, params.Arguments)
	}
	catalog := h.buildCatalog(ctx, session)
	tool, ok := lookupAgentTool(catalog, name)
	if !ok {
		kind, upstream, parsed := parseNamespacedAgentTool(name)
		if !parsed || !agentToolNameAllowed(session.Mode, kind, upstream) {
			return nil, fmt.Errorf("%w: %s", errAgentToolUnknown, name)
		}
		tool = agentToolDescriptor{Name: name, Kind: kind, UpstreamName: upstream, ServerID: h.serverIDForKind(session, kind)}
	}
	args := map[string]any{}
	if len(params.Arguments) > 0 && string(params.Arguments) != "null" {
		dec := json.NewDecoder(bytes.NewReader(params.Arguments))
		dec.UseNumber()
		if err := dec.Decode(&args); err != nil {
			return nil, err
		}
	}
	text, err := h.dispatch(ctx, session, tool, args)
	if err != nil {
		logAgentToolEvent("call-error", "mode="+string(session.Mode), "tool="+tool.Name, "err="+truncateRunes(err.Error(), 160))
		return mcpToolTextResult(err.Error(), true), nil
	}
	logAgentToolEvent("call", "mode="+string(session.Mode), "tool="+tool.Name, "ok=1")
	return mcpToolTextResult(text, false), nil
}

func (h *agentToolGateway) callTerminal(session *AgentToolSession, name string, raw json.RawMessage) (map[string]any, error) {
	switch {
	case name == submitNotificationReportTool && session.Mode == AgentToolReport:
		report, err := parseSubmitNotificationReportArgs(raw)
		if err != nil {
			return mcpToolTextResult(err.Error(), true), nil
		}
		if err := session.commitFinal(AgentTerminalResult{Kind: AgentTerminalReport, Report: &report}); err != nil {
			return nil, err
		}
		return mcpToolTextResult("报告提交成功，本轮结束。", false), nil
	case name == submitClarificationTool && session.Mode == AgentToolClarify:
		result, err := parseSubmitClarificationArgs(raw)
		if err != nil {
			logAgentToolEvent("terminal-invalid", "mode="+string(session.Mode), "tool="+name, "err="+truncateRunes(err.Error(), 160))
			return mcpToolTextResult(err.Error(), true), nil
		}
		if err := session.commitFinal(AgentTerminalResult{Kind: AgentTerminalClarification, Clarification: &result}); err != nil {
			return nil, err
		}
		return mcpToolTextResult("提交成功，本轮结束。", false), nil
	case name == submitAssistantReplyTool && session.Mode == AgentToolAssistant:
		reply, err := parseSubmitAssistantReplyArgs(raw, session.ProjectID)
		if err != nil {
			logAgentToolEvent("terminal-invalid", "mode="+string(session.Mode), "tool="+name, "err="+truncateRunes(err.Error(), 160))
			return nil, err
		}
		if session.ExecMode.Consultation && len(reply.Drafts) > 0 {
			return mcpToolTextResult("角色咨询只能回答和建议返工，不能提交写入草稿；请向 PM 提出建议。", true), nil
		}
		if err := session.commitFinal(AgentTerminalResult{Kind: AgentTerminalAssistant, Assistant: &reply}); err != nil {
			return nil, err
		}
		return mcpToolTextResult("提交成功，本轮结束。", false), nil
	default:
		return nil, errAgentToolForbidden
	}
}

func (h *agentToolGateway) dispatch(ctx context.Context, session *AgentToolSession, tool agentToolDescriptor, args map[string]any) (string, error) {
	if tool.Kind == "workspace" {
		return h.callMainBotWorkspace(ctx, session, tool.Name, args)
	}
	if tool.Kind == "agentflow" {
		if session.Mode == AgentToolClarify && !agentToolNameAllowed(AgentToolClarify, "agentflow", tool.UpstreamName) {
			return "", errAgentToolForbidden
		}
		if session.Mode == AgentToolAssistant {
			// 审批模式决定写工具放行范围；目录里能看到的才允许调用。
			// 拦截时给出引导性错误，让模型改走 drafts（CLI resume 的会话
			// 可能还带着旧模式的系统提示词，不能指望它自己记得）。
			if !assistantPlatformToolAllowed(session.ExecMode, tool.UpstreamName) {
				return "", fmt.Errorf("%s 不可在当前审批模式下直接调用：请把该操作连同完整参数放入 drafts（每轮 1 个，删除类需先自查引用并携带目标 id）供用户确认后执行", tool.UpstreamName)
			}
			ctx = withAssistantExecMode(ctx, session.ExecMode)
		} else if !agentToolNameAllowed(session.Mode, "agentflow", tool.UpstreamName) {
			return "", errAgentToolForbidden
		}
		if session.Mode == AgentToolScheduled {
			ctx = withAssistantExecMode(ctx, session.ExecMode)
		}
		call := assistantMCPCall{Server: "agentflow", Tool: tool.UpstreamName, Arguments: args}
		// A native CLI tool call is a fresh HTTP request, so it does not inherit
		// the generation's lease context. Reattach only the authenticated
		// session's own Run lease; a different task must acquire its own lease.
		if session.WorkspaceToken != "" && session.RunID != "" && strings.HasPrefix(tool.UpstreamName, "runs_") && assistantToolInput(call).Path["id"] == session.RunID {
			ctx = engine.WithConversationWorkspaceToken(ctx, session.WorkspaceToken, session.RunID)
		}
		asst := h.assistant()
		text, err := asst.callAssistantPlatform(ctx, session.User, session.ProjectID, call)
		if err != nil {
			return "", err
		}
		return redactAgentToolResult(text, session.User), nil
	}
	call := assistantMCPCall{Server: tool.ServerID, Tool: tool.UpstreamName, Arguments: stringifyTAPDIDs(args)}
	if call.Server == "" {
		call.Server = h.serverIDForKind(session, tool.Kind)
	}
	kind := tool.Kind
	if kind == "" {
		if server, ok := h.lookupServer(session, call.Server); ok {
			kind = classifyClarifyMCP(server)
		}
	}
	if kind == "tapd" {
		call = wrapClarifyTAPDCall(call)
		inner := strings.TrimSpace(fmt.Sprint(assistantMCPCallArguments(call)["tool_name"]))
		if call.Tool == "proxy_execute_tool" && inner != "" && !tapdInnerToolAllowed(session.Mode, inner) {
			return "", fmt.Errorf("%w: %s", errAgentToolForbidden, inner)
		}
	}
	if !agentToolNameAllowed(session.Mode, kind, tool.UpstreamName) && !(kind == "tapd" && tool.UpstreamName == "proxy_execute_tool") {
		return "", errAgentToolForbidden
	}
	if session.Mode != AgentToolScheduled && clarifyMCPCallWrite(kind, call) {
		name := strings.TrimSpace(call.Tool)
		if kind == "tapd" {
			if inner := strings.TrimSpace(fmt.Sprint(assistantMCPCallArguments(call)["tool_name"])); inner != "" {
				name = inner
			}
		}
		return "", fmt.Errorf("只允许只读 MCP，拒绝 %s", name)
	}
	asst := h.assistant()
	if session.Mode == AgentToolClarify {
		stream := &streamHandlers{deps: h.deps}
		text, err := stream.dispatchClarifyMCPCall(asst, session.User, session.ProjectID, call, nil)
		if err != nil {
			return "", err
		}
		return redactAgentToolResult(text, session.User), nil
	}
	text, err := asst.callAssistantMCP(session.User, session.ProjectID, call)
	if err != nil {
		return "", err
	}
	return redactAgentToolResult(text, session.User), nil
}

func (h *agentToolGateway) serverIDForKind(session *AgentToolSession, kind string) string {
	for _, server := range session.MCPServers {
		if classifyClarifyMCP(server) == kind {
			return server.ID
		}
	}
	return ""
}

func (h *agentToolGateway) lookupServer(session *AgentToolSession, ref string) (domain.MCPServerDefinition, bool) {
	ref = strings.TrimSpace(ref)
	for _, server := range session.MCPServers {
		if server.ID == ref || strings.EqualFold(server.Name, ref) || strings.EqualFold(server.MarketID, ref) {
			return server, true
		}
	}
	return domain.MCPServerDefinition{}, false
}

func (h *agentToolGateway) assistant() *assistantHandlers {
	return &assistantHandlers{deps: h.deps, api: h.api}
}

func (h *agentToolGateway) clarifyMCPUser(issue domain.Issue, user domain.User) domain.User {
	return (&streamHandlers{deps: h.deps}).clarifyMCPUser(issue, user)
}
