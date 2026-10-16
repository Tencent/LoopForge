package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"agentflow/internal/application"
	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
	"agentflow/internal/mcpserver"
	"agentflow/internal/runtime"
)

const assistantMCPResultLimit = 12000

type assistantMCPCall struct {
	Server    string         `json:"server"`
	Tool      string         `json:"tool,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Path      map[string]any `json:"path,omitempty"`
	Query     map[string]any `json:"query,omitempty"`
	Body      any            `json:"body,omitempty"`
}

func assistantOutboundMCPServers(items []domain.MCPServerDefinition, projectID string, userID int64) []domain.MCPServerDefinition {
	out := []domain.MCPServerDefinition{}
	for _, item := range visibleMCPServersForUser(items, userID) {
		if !assistantMCPInProject(item, projectID) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func assistantMCPInProject(item domain.MCPServerDefinition, projectID string) bool {
	if strings.TrimSpace(projectID) == "" {
		return false
	}
	if item.Scope == "personal" {
		return true
	}
	if item.ProjectID != "" {
		return item.ProjectID == projectID
	}
	return item.Scope != "project"
}

type ginH = map[string]any

var assistantPublicOperations = func() map[string]application.Operation {
	out := map[string]application.Operation{}
	for _, op := range application.PublicOperations() {
		out[op.Name] = op
	}
	return out
}()

func assistantPlatformServer(server string) bool {
	switch strings.ToLower(strings.TrimSpace(server)) {
	case "", "agentflow", "agentflow-platform", "platform":
		return true
	default:
		return false
	}
}

func (h *assistantHandlers) dispatchAssistantMCPCall(ctx context.Context, user domain.User, projectID string, call assistantMCPCall) (string, error) {
	if h != nil && h.repo != nil {
		if _, ok := h.lookupAssistantMCP(projectID, user.ID, call.Server); ok {
			return h.callAssistantMCP(user, projectID, call)
		}
	}
	tool := strings.TrimSpace(call.Tool)
	if tool == "" || tool == "tools/list" {
		if assistantPlatformServer(call.Server) {
			return h.callAssistantPlatform(ctx, user, projectID, call)
		}
		return h.callAssistantMCP(user, projectID, call)
	}
	if _, ok := assistantPublicOperations[tool]; ok && assistantPlatformServer(call.Server) {
		return h.callAssistantPlatform(ctx, user, projectID, call)
	}
	return h.callAssistantMCP(user, projectID, call)
}

func (h *assistantHandlers) callAssistantPlatform(ctx context.Context, user domain.User, projectID string, call assistantMCPCall) (string, error) {
	if h == nil || h.api == nil {
		return "", fmt.Errorf("未连接数据源")
	}
	tool := strings.TrimSpace(call.Tool)
	if tool == "" || tool == "tools/list" {
		names := make([]string, 0, len(assistantPublicOperations))
		reads := make([]string, 0)
		writes := make([]string, 0)
		for name, op := range assistantPublicOperations {
			names = append(names, name)
			if op.ReadOnly {
				reads = append(reads, name)
			} else {
				writes = append(writes, name)
			}
		}
		sort.Strings(names)
		sort.Strings(reads)
		sort.Strings(writes)
		raw, _ := json.Marshal(ginH{"server": "agentflow", "name": "AgentFlow", "tools": names, "readOnly": reads, "write": writes})
		return string(raw), nil
	}
	op, ok := assistantPublicOperations[tool]
	if !ok {
		return "", fmt.Errorf("未知平台工具 %q", tool)
	}
	mode := assistantExecModeFrom(ctx)
	if !op.ReadOnly {
		if !mode.AutoWrite {
			return "", fmt.Errorf("%s 是写入工具，请放入 drafts 供用户确认后再执行", op.Name)
		}
		if mode.ConfirmDangerous && assistantRiskyToolName(op.Name) {
			return "", fmt.Errorf("%s 是危险写入工具（删除/发布/派发/启动类），当前审批模式下需要用户确认：请把它连同完整参数放入 drafts 供用户确认后执行", op.Name)
		}
		if mode.DeferSOP && assistantDeferredSOPTool(op.Name) {
			return "", fmt.Errorf("%s 由平台定时任务在提示词执行结束后进行，本轮已跳过", op.Name)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, requestUserContextKey{}, domainUserToAuth(user))
	input := assistantToolInput(call)
	if mode.AutoWrite && op.Name == "issues_save" {
		input.Body = h.mergeIssueSaveBody(input.Body, input.Path)
	}
	if strings.HasSuffix(op.Name, "_list") && strings.TrimSpace(projectID) != "" {
		if input.Query == nil {
			input.Query = map[string]string{}
		}
		input.Query["projectId"] = projectID
	}
	if op.Name == "issues_list" {
		if input.Query == nil {
			input.Query = map[string]string{}
		}
		if strings.TrimSpace(input.Query["limit"]) == "" {
			input.Query["limit"] = strconv.Itoa(issueListDefaultLimit)
		}
	}
	result, err := mcpserver.LocalInvoker{Handler: h.api}.Invoke(ctx, op, input)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(result.Body))
	if result.Status < 200 || result.Status >= 300 {
		if text == "" {
			text = fmt.Sprintf("HTTP %d", result.Status)
		}
		return "", fmt.Errorf("%s", truncateRunes(text, 500))
	}
	if err := assistantRejectForeignProject(projectID, result.Body); err != nil {
		return "", err
	}
	return redactAssistantSecrets(truncateRunes(text, assistantMCPResultLimit), user), nil
}

func assistantMCPCallArguments(call assistantMCPCall) map[string]any {
	args := map[string]any{}
	for key, value := range call.Arguments {
		args[key] = value
	}
	if call.Path != nil {
		if _, ok := args["path"]; !ok {
			args["path"] = call.Path
		}
	}
	if call.Query != nil {
		if _, ok := args["query"]; !ok {
			args["query"] = call.Query
		}
	}
	if call.Body != nil {
		if _, ok := args["body"]; !ok {
			args["body"] = call.Body
		}
	}
	return args
}

func assistantToolInput(call assistantMCPCall) mcpserver.ToolInput {
	input := mcpserver.ToolInput{Query: map[string]string{}, Path: map[string]string{}}
	call.Arguments = assistantMCPCallArguments(call)
	if len(call.Arguments) == 0 {
		return input
	}
	if path, ok := call.Arguments["path"].(map[string]any); ok {
		for key, value := range path {
			if s := strings.TrimSpace(fmt.Sprint(value)); s != "" && s != "<nil>" {
				input.Path[key] = s
			}
		}
	}
	if query, ok := call.Arguments["query"].(map[string]any); ok {
		for key, value := range query {
			if s := strings.TrimSpace(fmt.Sprint(value)); s != "" && s != "<nil>" {
				input.Query[key] = s
			}
		}
	}
	if body, ok := call.Arguments["body"]; ok {
		input.Body = body
	}
	if len(input.Path) == 0 {
		if id, ok := call.Arguments["id"].(string); ok {
			if id = strings.TrimSpace(id); id != "" {
				input.Path["id"] = id
			}
		}
	}
	return input
}

func assistantRejectForeignProject(projectID string, body []byte) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || len(body) == 0 {
		return nil
	}
	var payload any
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	value, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	if issue, ok := value["issue"].(map[string]any); ok {
		if err := assistantRejectForeignProjectValue(projectID, issue); err != nil {
			return err
		}
	}
	return assistantRejectForeignProjectValue(projectID, value)
}

func assistantRejectForeignProjectValue(projectID string, value map[string]any) error {
	pid, _ := value["projectId"].(string)
	if strings.TrimSpace(pid) != "" && pid != projectID {
		return fmt.Errorf("该资源不属于当前项目")
	}
	return nil
}

func (h *assistantHandlers) callAssistantMCP(user domain.User, projectID string, call assistantMCPCall) (string, error) {
	if h == nil || h.repo == nil {
		return "", fmt.Errorf("未连接数据源")
	}
	if strings.TrimSpace(projectID) == "" {
		return "", fmt.Errorf("请先在顶栏选择项目后再调用项目 MCP")
	}
	server, ok := h.lookupAssistantMCP(projectID, user.ID, call.Server)
	if !ok {
		return "", fmt.Errorf("当前项目没有名为 %q 的 MCP，或你无权使用", strings.TrimSpace(call.Server))
	}
	transport := strings.ToLower(strings.TrimSpace(server.Transport))
	if transport == "" && strings.TrimSpace(server.URL) != "" {
		transport = "http"
	}
	if transport != "http" && transport != "sse" {
		return "", fmt.Errorf("%s 是 %s MCP，助手只代调当前项目的 HTTP MCP（如 TAPD/工蜂），不能启动 stdio 命令", server.Name, transport)
	}
	bound, err := bindAssistantMCP(server, user)
	if err != nil {
		return "", err
	}
	target, err := url.Parse(strings.TrimSpace(bound.URL))
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return "", fmt.Errorf("MCP %s 的 URL 无效", server.Name)
	}
	client := mcpclient.New(bound.URL, bound.Headers).WithTimeout(assistantChatTimeout)
	tool := strings.TrimSpace(call.Tool)
	if tool == "" || tool == "tools/list" {
		tools, err := client.ListTools()
		if err != nil {
			return "", fmt.Errorf("列出 %s 工具失败: %w", server.Name, err)
		}
		raw, _ := json.Marshal(ginH{"server": server.ID, "name": server.Name, "tools": tools})
		return redactAssistantSecrets(string(raw), user), nil
	}
	text, err := client.CallTool(tool, call.Arguments)
	if err != nil {
		return "", err
	}
	return redactAssistantSecrets(truncateRunes(text, assistantMCPResultLimit), user), nil
}

func (h *assistantHandlers) lookupAssistantMCP(projectID string, userID int64, ref string) (domain.MCPServerDefinition, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return domain.MCPServerDefinition{}, false
	}
	for _, item := range assistantOutboundMCPServers(h.repo.MCPServers(), projectID, userID) {
		if item.ID == ref || strings.EqualFold(item.Name, ref) || strings.EqualFold(item.MarketID, ref) {
			return item, true
		}
	}
	return domain.MCPServerDefinition{}, false
}

func bindAssistantMCP(server domain.MCPServerDefinition, user domain.User) (domain.MCPServerDefinition, error) {
	bundle := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{server}}
	var githubErr error
	bundle, githubErr = runtime.BindGitHubMCPHeaders(bundle, user.GitToken, true)
	if githubErr != nil {
		return domain.MCPServerDefinition{}, githubErr
	}
	server = bundle.MCPServers[0]
	if strings.TrimSpace(server.AuthType) == runtime.MCPAuthTaihuPAT {
		bound, err := runtime.BindPersonalMCPHeaders(bundle, user.TaihuPAT)
		if err != nil {
			return domain.MCPServerDefinition{}, err
		}
		server = bound.MCPServers[0]
	} else {
		server = runtime.BindPersonalMCPHeadersOptional(bundle, user.TaihuPAT).MCPServers[0]
	}
	headers, err := runtime.ResolveMCPHeaders(server)
	if err != nil {
		return domain.MCPServerDefinition{}, err
	}
	server.Headers = headers
	return server, nil
}

func redactAssistantSecrets(text string, user domain.User) string {
	for _, secret := range []string{user.CodeBuddyAPIKey, user.CodexAPIKey, user.ClaudeCodeAPIKey, user.TaihuPAT, user.GitToken} {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}
