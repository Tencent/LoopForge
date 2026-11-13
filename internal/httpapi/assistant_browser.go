package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"agentflow/internal/application"
	"agentflow/internal/domain"
	"agentflow/internal/mcpserver"
)

const mainBotBrowserPrefix = "workbench_browser_"

// errMainBotBrowserNoWorkspace keeps the ordinary conversation case
// actionable: the browser runs on an execution machine, which only exists once
// the conversation has a workspace.
var errMainBotBrowserNoWorkspace = errors.New("本对话还没有工作区，浏览器没有可运行的执行机：先调用 workbench_workspace_open 为要改的仓库开一个临时工作区，再用 workbench_browser_navigate 打开页面")

// mainBotBrowserRun is the run whose workspace hosts the browser: the task run
// of a task conversation, or the temporary workspace Main Bot opened in an
// ordinary conversation.
func mainBotBrowserRun(s *AgentToolSession) (string, error) {
	if len(mainBotBrowserTools(s)) == 0 {
		return "", errAgentToolForbidden
	}
	runID := strings.TrimSpace(s.workspaceRun())
	if runID == "" {
		return "", errMainBotBrowserNoWorkspace
	}
	return runID, nil
}

func mainBotBrowserTools(s *AgentToolSession) []agentToolDescriptor {
	if len(mainBotWorkspaceTools(s)) == 0 {
		return nil
	}
	descriptors := []struct{ name, description, schema string }{
		{"navigate", "在当前会话/任务的 Agent 浏览器里打开网址（不是网址时按关键词搜索），或执行 back/forward/reload。浏览器未启动时自动启动，和文件工作区跑在同一台执行机上；普通对话需先用 workbench_workspace_open 打开工作区。返回加载后的页面快照。", `{"type":"object","properties":{"url":{"type":"string"},"action":{"type":"string","enum":["back","forward","reload"]}},"additionalProperties":false}`},
		{"snapshot", "读取当前页面：URL、标题、标签页、可见文本，以及带 ref（如 e12）的可交互元素。点击、输入前先获取快照。", `{"type":"object","properties":{},"additionalProperties":false}`},
		{"click", "用真实鼠标点击快照中的元素。返回点击后的新快照（ref 会刷新）。", `{"type":"object","properties":{"ref":{"type":"string"}},"required":["ref"],"additionalProperties":false}`},
		{"type", "清空并在快照中的输入元素里输入文本；submit=true 时随后按回车。返回新快照。", `{"type":"object","properties":{"ref":{"type":"string"},"text":{"type":"string"},"submit":{"type":"boolean"}},"required":["ref","text"],"additionalProperties":false}`},
		{"press", "在当前焦点按键：Enter、Tab、Escape、Backspace、Delete、Space、ArrowUp/Down/Left/Right、PageUp、PageDown、Home、End。返回新快照。", `{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false}`},
		{"screenshot", "截取当前标签页画面（图像），用于确认布局、样式或快照无法表达的内容。截图会自动附在本轮回复里给用户看（显示在正文下方，每轮保留最近 4 张）。用户还能在对话侧栏「浏览器」页签实时查看和操作这个浏览器：回复里写出页面地址（file:///workspace/… 或 http://localhost:端口/…），用户点击即在该页签打开；工作区文件和 http://localhost:端口 的开发服务器都由平台转发、在用户自己的浏览器里原生渲染（可在新窗口打开；开发服务器暂不支持热更新，改动后让用户刷新）。", `{"type":"object","properties":{},"additionalProperties":false}`},
		{"tabs", "管理标签页：list 列出，switch 切换到 tabId，new 新建，close 关闭 tabId（缺省当前页）。", `{"type":"object","properties":{"action":{"type":"string","enum":["list","switch","new","close"]},"tabId":{"type":"string"}},"required":["action"],"additionalProperties":false}`},
	}
	out := make([]agentToolDescriptor, 0, len(descriptors))
	for _, d := range descriptors {
		out = append(out, agentToolDescriptor{Name: mainBotBrowserPrefix + d.name, Description: d.description, InputSchema: json.RawMessage(d.schema), Kind: "browser"})
	}
	return out
}

func mainBotBrowserRequest(name string, args map[string]any) (domain.BrowserRequest, error) {
	text := func(key string) string { v, _ := args[key].(string); return strings.TrimSpace(v) }
	req := domain.BrowserRequest{Ensure: true}
	switch strings.TrimPrefix(name, mainBotBrowserPrefix) {
	case "navigate":
		req.WithSnapshot = true
		switch action := text("action"); action {
		case "back", "forward", "reload":
			req.Operation = action
		case "":
			if req.URL = text("url"); req.URL == "" {
				return req, errors.New("url 或 action 必须提供一个")
			}
			req.Operation = "navigate"
		default:
			return req, fmt.Errorf("不支持的 action %q", action)
		}
	case "snapshot", "screenshot":
		req.Operation = strings.TrimPrefix(name, mainBotBrowserPrefix)
	case "click":
		req.Operation, req.Ref = "click", text("ref")
	case "type":
		value, ok := args["text"].(string)
		if !ok {
			return req, errors.New("text 必须是字符串")
		}
		submit, _ := args["submit"].(bool)
		req.Operation, req.Ref, req.Text, req.Submit = "type", text("ref"), value, submit
	case "press":
		req.Operation, req.Key = "press", text("key")
	case "tabs":
		switch text("action") {
		case "list":
			req.Operation = "status"
		case "switch":
			if req.TargetID = text("tabId"); req.TargetID == "" {
				return req, errors.New("switch 需要 tabId")
			}
			req.Operation, req.WithSnapshot = "activate", true
		case "new":
			req.Operation = "newTab"
		case "close":
			req.Operation, req.TargetID = "closeTab", text("tabId")
		default:
			return req, errors.New("action 必须是 list、switch、new 或 close")
		}
	default:
		return req, errAgentToolUnknown
	}
	return req, nil
}

func (h *agentToolGateway) callMainBotBrowser(ctx context.Context, s *AgentToolSession, name string, args map[string]any) (map[string]any, error) {
	releaseTurn := s.workspaceTurn.lockOperation()
	defer releaseTurn()
	runID, runErr := mainBotBrowserRun(s)
	if runErr != nil {
		if errors.Is(runErr, errMainBotBrowserNoWorkspace) {
			return mcpToolTextResult(runErr.Error(), true), nil
		}
		return nil, runErr
	}
	if run, ok := h.repo.RunState(runID); !ok || run.ExecutionContext.ProjectID != s.ProjectID || s.User.ID != s.UserID {
		return nil, errAgentToolForbidden
	}
	req, err := mainBotBrowserRequest(name, args)
	if err != nil {
		return mcpToolTextResult(err.Error(), true), nil
	}
	ctx, _, release, err := h.mainBotWorkspaceLease(ctx, s, runID)
	if err != nil {
		return mcpToolTextResult(err.Error(), true), nil
	}
	defer release()
	ctx = context.WithValue(ctx, requestUserContextKey{}, domainUserToAuth(s.User))
	ctx = context.WithValue(ctx, mainBotWorkspaceKey{}, runID)
	result, err := (mcpserver.LocalInvoker{Handler: h.api}).Invoke(ctx, application.Operation{Method: "POST", Path: "/api/runs/{id}/browser"}, mcpserver.ToolInput{Path: map[string]string{"id": runID}, Body: req})
	if err != nil {
		return mcpToolTextResult(err.Error(), true), nil
	}
	var state domain.BrowserState
	actionErr := ""
	if result.Status == 409 {
		var conflict struct {
			Error string              `json:"error"`
			State domain.BrowserState `json:"state"`
		}
		_ = json.Unmarshal(result.Body, &conflict)
		state, actionErr = conflict.State, conflict.Error
	} else if result.Status < 200 || result.Status >= 300 {
		return mcpToolTextResult(strings.TrimSpace(string(result.Body)), true), nil
	} else if err := json.Unmarshal(result.Body, &state); err != nil {
		return nil, err
	}
	if !state.Available {
		reason := state.Reason
		if reason == "" {
			reason = "Agent 浏览器不可用"
		}
		return mcpToolTextResult(reason, true), nil
	}
	summary := redactAgentToolResult(formatBrowserState(state, actionErr), s.User)
	if req.Operation == "screenshot" && state.Frame != "" && actionErr == "" {
		// The image block only reaches the model; keep a copy for the reply so
		// the user sees the same frame the answer talks about.
		if data, err := base64.StdEncoding.DecodeString(state.Frame); err == nil && len(data) > 0 && len(data) <= 8<<20 {
			if mimeType := http.DetectContentType(data); strings.HasPrefix(mimeType, "image/") {
				s.addReplyScreenshot(mimeType, data)
				summary += "\n\n这张截图会自动附在本轮回复里（正文下方），用户能直接看到。"
			}
		}
		return map[string]any{"content": []map[string]any{
			{"type": "text", "text": summary},
			{"type": "image", "mimeType": "image/jpeg", "data": state.Frame},
		}}, nil
	}
	return mcpToolTextResult(summary, actionErr != ""), nil
}

// formatBrowserState renders the page for a language model: tabs, then the
// interactive elements it can address by ref, then the visible text.
func formatBrowserState(state domain.BrowserState, actionErr string) string {
	var b strings.Builder
	if actionErr != "" {
		b.WriteString("操作失败：" + actionErr + "\n\n")
	}
	b.WriteString("URL: " + state.URL + "\n标题: " + state.Title + "\n")
	if len(state.Tabs) > 1 {
		b.WriteString("\n标签页：\n")
		for _, tab := range state.Tabs {
			mark := "  "
			if tab.Active {
				mark = "* "
			}
			fmt.Fprintf(&b, "%s%s %q %s\n", mark, tab.ID, tab.Title, tab.URL)
		}
	}
	snap := state.Snapshot
	if snap == nil {
		return b.String()
	}
	if len(snap.Elements) > 0 {
		b.WriteString("\n可交互元素：\n")
		for _, el := range snap.Elements {
			fmt.Fprintf(&b, "[%s] %s %q", el.Ref, el.Role, el.Name)
			if el.Value != "" {
				fmt.Fprintf(&b, " value=%q", el.Value)
			}
			if el.Checked {
				b.WriteString(" checked")
			}
			if el.Disabled {
				b.WriteString(" disabled")
			}
			b.WriteByte('\n')
		}
	}
	b.WriteString("\n页面文本：\n" + snap.Text + "\n")
	if snap.Truncated {
		b.WriteString("…（页面文本已截断，可滚动或点击后重新获取快照）\n")
	}
	return b.String()
}
