package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/mcpserver"

	"github.com/gin-gonic/gin"
)

type agentToolGateway struct {
	*deps
}

type agentToolTokenKey struct{}

func withAgentToolToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, agentToolTokenKey{}, token)
}

// Resolve the capability again under the mode fence. A request may have
// obtained its session pointer immediately before a mode rotation revoked it.
func validAgentToolToken(ctx context.Context, session *AgentToolSession) bool {
	token, _ := ctx.Value(agentToolTokenKey{}).(string)
	return (token == "" || token == session.Token) && !session.closed.Load() && !session.expired(time.Now())
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
		agentToolSessions.delete(token)
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
	response := h.handleRPC(withAgentToolToken(c.Request.Context(), token), session, request)
	if response == nil {
		c.Status(http.StatusAccepted)
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *agentToolGateway) handleRPC(ctx context.Context, session *AgentToolSession, request agentToolRPCRequest) *agentToolRPCResponse {
	if request.Method != "tools/call" {
		session.toolsMu.RLock()
		defer session.toolsMu.RUnlock()
	}
	if request.Method != "tools/call" && !validAgentToolToken(ctx, session) {
		return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Error: &agentToolRPCError{Code: -32602, Message: errAgentToolInvalidSession.Error()}}
	}
	if strings.HasPrefix(request.Method, "notifications/") {
		return nil
	}
	switch request.Method {
	case "initialize":
		return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{
			"protocolVersion": agentToolProtocolVersion(request.Params),
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": agentToolSessionMCPName, "version": "1.0.0"},
			"instructions":    agentToolSessionInstructions(session),
		}}
	case "ping":
		return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{}}
	case "tools/list":
		return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{"tools": h.toolList(ctx, session)}}
	case "agentflow/nativeApproval":
		result := h.approveNativeTool(ctx, session, request.Params)
		return &agentToolRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: result}
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
		return "LoopForge session MCP"
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
	session.toolsMu.RLock()
	defer session.toolsMu.RUnlock()
	if session.finished() {
		return nil, errAgentToolTerminalCommitted
	}
	if !validAgentToolToken(ctx, session) {
		return nil, errAgentToolInvalidSession
	}
	if session.ctx != nil {
		// Tool calls arrive on independent HTTP requests. Stopping the model
		// turn must also stop any in-flight work using its gateway.
		callCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(session.ctx, cancel)
		defer stop()
		defer cancel()
		ctx = callCtx
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	name := stripAgentToolServerPrefix(params.Name)
	if session.requestedMode() != "" {
		return mcpToolTextResult(assistantHandoffNotice, true), nil
	}
	if session.RuntimeMode == domain.AssistantModeWorkspace {
		if _, ok := lookupAgentTool(h.buildCatalog(ctx, session), name); !ok {
			return nil, fmt.Errorf("%w: %s", errAgentToolForbidden, name)
		}
	}
	// Some CLI/provider combinations encode function arguments as a JSON
	// string before forwarding them to MCP. Accept one encoded object, while
	// retaining the same token, mode, catalog and write checks.
	if rawArgs := bytes.TrimSpace(params.Arguments); len(rawArgs) > 0 && rawArgs[0] == '"' {
		var encoded string
		if err := json.Unmarshal(rawArgs, &encoded); err != nil {
			return nil, err
		}
		object := bytes.TrimSpace([]byte(encoded))
		if len(object) == 0 || object[0] != '{' || !json.Valid(object) {
			return nil, fmt.Errorf("tool arguments must be a JSON object")
		}
		params.Arguments = object
	}
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
	if name == "workbench_publish_artifact" {
		return h.publishWorkbenchArtifact(session, params.Arguments)
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
	if tool.Kind == "browser" {
		result, err := h.callMainBotBrowser(ctx, session, tool.Name, args)
		logAgentToolEvent("call", "mode="+string(session.Mode), "tool="+tool.Name, fmt.Sprintf("ok=%t", err == nil && result["isError"] != true))
		return result, err
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
			return mcpToolTextResult("草稿参数校验失败，本轮继续；请按以下错误修正后重新提交，不代表工具不可用："+err.Error(), true), nil
		}
		if h.deps != nil && h.repo != nil {
			if err := h.assistant().vetProposalDrafts(session, &reply); err != nil {
				logAgentToolEvent("terminal-invalid", "mode="+string(session.Mode), "tool="+name, "err="+truncateRunes(err.Error(), 160))
				return mcpToolTextResult("派发建议未提交，本轮继续："+err.Error(), true), nil
			}
		}
		if len(reply.Drafts) == 0 {
			// This tool ends the model process. Accepting a progress message here
			// used to kill sibling read calls before a requested SOP was prepared.
			return mcpToolTextResult("未提交草稿，本轮继续。submit_assistant_reply 仅用于提交待确认写入草稿；进度说明请用普通文本并继续执行工具查询。请完成用户请求或说明具体阻塞原因后，以自然语言结束；不要提交占位回复。", true), nil
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
	if session.Mode == AgentToolAssistant && session.ExecMode.ReviewWrites {
		ctx = withAssistantApprovalReviewer(ctx, session.approvalReviewer)
	}
	if tool.Kind == "workspace" {
		return h.callMainBotWorkspace(ctx, session, tool.Name, args)
	}
	// 轻量 GitHub 工具（agent_tool_github.go）：用请求人自己的 Git Token 直连
	// GitHub，范围限定在当前项目已绑定的仓库。与 workspace 工具一样不走 drafts，
	// 「帮我批准」时写操作在 callMainBotGitHub 里逐项过审查。
	if tool.Kind == "github" {
		return h.callMainBotGitHub(ctx, session, tool.Name, args)
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
		if session.canOpenConversationWorkspace() {
			ctx = withConversationSession(ctx, session.ChatSessionID)
		}
		ctx = context.WithValue(ctx, assistantControlScopeKey{}, session.ControlScope)
		ctx = context.WithValue(ctx, assistantAssetObserverKey{}, func(asset domain.AssistantAssetRef) {
			session.mu.Lock()
			defer session.mu.Unlock()
			for i, old := range session.assets {
				if old.Kind == asset.Kind && old.ID == asset.ID {
					session.assets[i] = asset
					return
				}
			}
			session.assets = append(session.assets, asset)
			if asset.Kind == "run" {
				session.runIDs = append(session.runIDs, asset.ID)
			}
		})
		// A native CLI tool call is a fresh HTTP request, so it does not inherit
		// the generation's lease context. Reattach only the authenticated
		// session's own Run lease; a different task must acquire its own lease.
		if session.WorkspaceToken != "" && session.RunID != "" && strings.HasPrefix(tool.UpstreamName, "runs_") && assistantToolInput(call).Path["id"] == session.RunID {
			ctx = engine.WithConversationWorkspaceToken(ctx, session.WorkspaceToken, session.RunID)
		}
		if session.workspaceTurn != nil && strings.HasPrefix(tool.UpstreamName, "runs_") {
			releaseTurn := session.workspaceTurn.lockOperation()
			defer releaseTurn()
			ctx = session.workspaceTurn.withLease(ctx, assistantToolInput(call).Path["id"])
		}
		asst := h.assistant()
		text, err := asst.callAssistantPlatform(ctx, session.User, session.ProjectID, call)
		if err != nil {
			return "", err
		}
		return assistantPlatformResultForModel(tool.UpstreamName, redactAgentToolResult(text, session.User)), nil
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
	if session.Mode == AgentToolAssistant && mcpCallMayWrite(kind, call) {
		// A write to an external MCP follows the conversation's approval
		// mode like a platform write. It has no draft to confirm, so the
		// confirm mode cannot run it.
		name := mcpCallName(kind, call)
		if !session.ExecMode.AutoWrite {
			return "", fmt.Errorf("%s 会写入外部系统，当前审批模式需要逐项确认，而外部 MCP 写入没有待确认草稿。请让用户把审批模式切到「自动」或「辅助」后再试，或告诉用户要做的改动由其手动完成", name)
		}
		if session.ExecMode.ReviewWrites {
			review := reviewAssistantAction(ctx, name, mcpServerName(h, session, call.Server)+" MCP 写入外部系统", map[string]any{"server": call.Server, "tool": call.Tool, "arguments": call.Arguments})
			if !review.allows() {
				return "", assistantReviewRefusal(name, review)
			}
		}
	} else if session.Mode != AgentToolScheduled && clarifyMCPCallWrite(kind, call) {
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

func mcpServerName(h *agentToolGateway, session *AgentToolSession, ref string) string {
	if item, ok := h.lookupServer(session, ref); ok && strings.TrimSpace(item.Name) != "" {
		return item.Name
	}
	return ref
}

// mcpCallName is the tool a call runs; for TAPD it is the inner tool.
func mcpCallName(kind string, call assistantMCPCall) string {
	if kind == "tapd" {
		if inner := strings.TrimSpace(fmt.Sprint(assistantMCPCallArguments(call)["tool_name"])); inner != "" && inner != "<nil>" {
			return inner
		}
	}
	return strings.TrimSpace(call.Tool)
}

// mcpCallMayWrite treats a call as a write unless its name reads as a read.
// The approval review is the safety net, so an unknown name such as
// merge_merge_request is reviewed instead of passing as a read.
func mcpCallMayWrite(kind string, call assistantMCPCall) bool {
	name := strings.ToLower(mcpCallName(kind, call))
	if name == "" || clarifyMCPWriteName(name) {
		return name != ""
	}
	for _, word := range []string{"add", "merge", "upload", "transition", "close", "reopen", "approve", "assign", "set", "remove", "publish", "send", "post", "submit", "comment"} {
		for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
			if part == word {
				return true
			}
		}
	}
	for _, word := range []string{"get", "list", "search", "query", "count", "lookup", "read", "fetch", "describe", "show", "info", "detail", "tree", "blame", "compare", "schema", "reference", "find", "view"} {
		if strings.Contains(name, word) {
			return false
		}
	}
	return true
}
