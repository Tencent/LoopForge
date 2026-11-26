package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agentflow/internal/runtime"
)

// assistantInteractiveInput keeps stdin open until the translated result.
// The writer may block on a long frame without blocking stdout processing.
type assistantInteractiveInput struct {
	input, writer *os.File
	frames        chan []byte
	closed        chan struct{}
	closeOnce     sync.Once
}

func newAssistantInteractiveInput() (*assistantInteractiveInput, error) {
	input, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	a := &assistantInteractiveInput{input: input, writer: writer, frames: make(chan []byte, 1), closed: make(chan struct{})}
	go a.writeFrames()
	return a, nil
}

func (a *assistantInteractiveInput) writeFrames() {
	defer a.closeWriter()
	for {
		select {
		case <-a.closed:
			return
		case data := <-a.frames:
			if _, err := a.writer.Write(data); err != nil {
				return
			}
		}
	}
}

func (a *assistantInteractiveInput) closeWriter() {
	a.closeOnce.Do(func() {
		close(a.closed)
		_ = a.writer.Close()
	})
}

func (a *assistantInteractiveInput) close() {
	a.closeWriter()
	_ = a.input.Close()
}

func (a *assistantInteractiveInput) enqueue(data []byte) {
	select {
	case a.frames <- data:
	case <-a.closed:
	}
}

// assistantACP uses the existing interactive conversation transport for CLIs
// whose one-shot print mode takes its prompt only on the command line.
type assistantACP struct {
	*assistantInteractiveInput
	cwd      string
	prompt   string
	resume   string
	session  string
	text     strings.Builder
	stream   runtime.ClaudeStream
	routes   runtime.CursorRouteFilter
	finished chan struct{}
}

func newAssistantACP(cwd, prompt, resume string) (*assistantACP, error) {
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	input, err := newAssistantInteractiveInput()
	if err != nil {
		return nil, err
	}
	a := &assistantACP{assistantInteractiveInput: input, cwd: cwd, prompt: prompt, resume: resume, finished: make(chan struct{})}
	a.request(1, "initialize", map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "LoopForge", "version": "1"},
	})
	return a, nil
}

func (a *assistantACP) Read(p []byte) (int, error) { return a.input.Read(p) }

func (a *assistantACP) Close() error {
	a.closeWriter()
	return a.input.Close()
}

func (a *assistantACP) send(frame map[string]any) {
	frame["jsonrpc"] = "2.0"
	data, _ := json.Marshal(frame)
	a.enqueue(append(data, '\n'))
}

func (a *assistantACP) request(id int, method string, params map[string]any) {
	a.send(map[string]any{"id": id, "method": method, "params": params})
}

func (a *assistantACP) result(text string, failed bool) []string {
	a.closeWriter()
	out := a.stream.Result(text, failed)
	close(a.finished)
	return out
}

func (a *assistantACP) answer(text string) []string {
	a.text.WriteString(text)
	return a.stream.Delta("acp-answer", "text", text)
}

var errAssistantACPFinished = errors.New("ACP turn finished")

// ACP processes can stay alive after stdin closes. Give the CLI time to flush
// its journal, then stop it through the same execution context as user stops.
func (a *assistantACP) execute(ctx context.Context, run func(context.Context) (string, error)) (string, error) {
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		select {
		case <-runCtx.Done():
			return
		case <-a.finished:
		}
		timer := time.NewTimer(runtime.ConversationTerminalResultDrain)
		defer timer.Stop()
		select {
		case <-runCtx.Done():
		case <-timer.C:
			cancel(errAssistantACPFinished)
		}
	}()
	out, err := run(runCtx)
	if context.Cause(runCtx) == errAssistantACPFinished {
		return out, nil
	}
	return out, err
}

func (a *assistantACP) translate(line string) []string {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Message string `json:"message"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(line), &message) != nil || a.stream.Finished() {
		return nil
	}
	if message.Method != "" {
		if len(message.ID) > 0 {
			if message.Method == "session/request_permission" {
				a.send(map[string]any{"id": message.ID, "result": map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}})
			} else {
				a.send(map[string]any{"id": message.ID, "error": map[string]any{"code": -32601, "message": "Unsupported ACP client request"}})
			}
			return nil
		}
		if message.Method == "session/update" && a.session != "" {
			return a.update(message.Params)
		}
		return nil
	}
	if message.Error != nil {
		text := message.Error.Data.Message
		if text == "" {
			text = message.Error.Message
		}
		if text == "" {
			text = "ACP 请求失败"
		}
		lower := strings.ToLower(text)
		missing := strings.Contains(lower, "unknown session") || strings.Contains(lower, "session") &&
			(strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist"))
		if string(message.ID) == "2" && a.resume != "" && (message.Error.Code == -32002 || missing) {
			text = "No conversation found with session ID: " + a.resume
		}
		return a.result(text, true)
	}
	switch string(message.ID) {
	case "1":
		var result struct {
			ProtocolVersion   int `json:"protocolVersion"`
			AgentCapabilities struct {
				LoadSession bool `json:"loadSession"`
			} `json:"agentCapabilities"`
		}
		if json.Unmarshal(message.Result, &result) != nil || result.ProtocolVersion != 1 {
			return a.result("CLI 返回了不支持的 ACP 协议版本", true)
		}
		params := map[string]any{"cwd": a.cwd, "mcpServers": []any{}}
		if a.resume == "" {
			a.request(2, "session/new", params)
		} else {
			if !result.AgentCapabilities.LoadSession {
				return a.result("此 CLI 版本不支持通过 ACP 继续会话", true)
			}
			params["sessionId"] = a.resume
			a.request(2, "session/load", params)
		}
	case "2":
		var result struct {
			SessionID string `json:"sessionId"`
			Models    struct {
				CurrentModelID string `json:"currentModelId"`
			} `json:"models"`
		}
		if json.Unmarshal(message.Result, &result) != nil {
			return a.result("ACP 返回了无效的会话信息", true)
		}
		a.session = result.SessionID
		if a.session == "" {
			a.session = a.resume
		}
		if !agentCLISessionValid(a.session) {
			return a.result("ACP 返回了无效的会话编号", true)
		}
		a.request(3, "session/prompt", map[string]any{
			"sessionId": a.session, "prompt": []map[string]string{{"type": "text", "text": a.prompt}},
		})
		return a.stream.Init(a.session, result.Models.CurrentModelID)
	case "3":
		var result struct {
			StopReason string `json:"stopReason"`
		}
		if json.Unmarshal(message.Result, &result) != nil || result.StopReason != "end_turn" {
			return a.result("ACP 会话未完成："+result.StopReason, true)
		}
		out := a.answer(a.routes.Finish())
		return append(out, a.result(a.text.String(), false)...)
	}
	return nil
}

func (a *assistantACP) update(data json.RawMessage) []string {
	var notice struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind       string          `json:"sessionUpdate"`
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Status     string          `json:"status"`
			RawInput   json.RawMessage `json:"rawInput"`
			RawOutput  json.RawMessage `json:"rawOutput"`
			Content    struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(data, &notice) != nil || notice.SessionID != a.session {
		return nil
	}
	u := notice.Update
	switch u.Kind {
	case "agent_message_chunk":
		if u.Content.Type == "text" {
			return a.answer(a.routes.Feed(u.Content.Text))
		}
	case "agent_thought_chunk":
		return a.stream.Delta("acp-thinking", "thinking", u.Content.Text)
	case "tool_call":
		return a.stream.ToolUse(u.ToolCallID, u.Title, u.RawInput)
	case "tool_call_update":
		if u.Status == "completed" || u.Status == "failed" {
			return a.stream.ToolResult(u.ToolCallID, u.Status == "failed", string(u.RawOutput))
		}
	}
	return nil
}
