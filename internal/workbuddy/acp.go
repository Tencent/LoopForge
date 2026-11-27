package workbuddy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Turn struct {
	Text         string
	Model        string
	Usage        Usage
	gateReceipts gateReceiptSet
}

type Usage struct {
	Input      int64  `json:"inputTokens"`
	Output     int64  `json:"outputTokens"`
	CacheRead  int64  `json:"cachedReadTokens"`
	CacheWrite int64  `json:"cachedWriteTokens"`
	Model      string `json:"model"`
}

type acpConnection struct {
	life         context.Context
	endpoint     *url.URL
	sessionID    string
	token        string
	connectionID string
	lastEventID  string
	updates      <-chan acpFrame
	sequence     int
	reconnected  bool
	gate         *GateAttempt
}

type acpFrame struct {
	data []byte
	id   string
	err  error
}

type acpMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// RunTurn runs one prompt. A disconnected event stream may reattach to the
// existing session, while the prompt itself is submitted only once.
func (c *Client) RunTurn(ctx context.Context, session Session, token, model, prompt string, onText func(string), gate *GateAttempt) (Turn, error) {
	endpoint, err := c.serviceURL(session.Endpoint, true)
	if err != nil {
		return Turn{}, err
	}
	if endpoint.Path == "" || endpoint.Path == "/" {
		endpoint.Path = "/v1/agents/acp"
	}
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	connection := &acpConnection{life: life, endpoint: endpoint, sessionID: session.ID, token: token}
	if err := connection.open(ctx); err != nil {
		return Turn{}, err
	}
	defer connection.end(http.MethodDelete, nil)
	initialized, err := connection.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}},
	}, nil)
	if err != nil {
		return Turn{}, err
	}
	var handshake struct {
		Capabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	if json.Unmarshal(initialized, &handshake) != nil || !handshake.Capabilities.LoadSession {
		return Turn{}, errors.New("WorkBuddy ACP 接入点未启用会话加载")
	}
	loaded, err := connection.rpc(ctx, "session/load", map[string]any{
		"sessionId": session.ID, "cwd": "/workspace", "mcpServers": []any{},
	}, nil)
	if err != nil {
		return Turn{}, err
	}
	var sessionState struct {
		Models struct {
			Current string `json:"currentModelId"`
		} `json:"models"`
	}
	_ = json.Unmarshal(loaded, &sessionState)
	activeModel := sessionState.Models.Current
	if model != "" && model != activeModel {
		result, err := connection.rpc(ctx, "session/set_model", map[string]string{
			"sessionId": session.ID, "modelId": model,
		}, nil)
		if err != nil {
			return Turn{}, fmt.Errorf("WorkBuddy 无法切换到模型 %q：%w", model, err)
		}
		if len(result) == 0 {
			return Turn{}, fmt.Errorf("WorkBuddy 未确认切换到模型 %q", model)
		}
		activeModel = model
	}
	if err := ctx.Err(); err != nil {
		return Turn{}, err
	}
	connection.gate = gate
	const outputLimit = 64 << 10
	var output strings.Builder
	var usage Usage
	var gateReceipts gateReceiptSet
	reply, err := connection.rpc(ctx, "session/prompt", map[string]any{
		"sessionId": session.ID, "prompt": []any{map[string]string{"type": "text", "text": prompt}},
	}, func(raw json.RawMessage) {
		var event struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				Kind      string          `json:"sessionUpdate"`
				Content   json.RawMessage `json:"content"`
				RawOutput json.RawMessage `json:"rawOutput"`
				Usage
			} `json:"update"`
		}
		if json.Unmarshal(raw, &event) != nil || event.SessionID != session.ID {
			return
		}
		switch event.Update.Kind {
		case "agent_message_chunk":
			var content struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(event.Update.Content, &content) != nil {
				return
			}
			chunk := content.Text
			if left := outputLimit - output.Len(); left > 0 && chunk != "" {
				chunk = chunk[:min(len(chunk), left)]
				output.WriteString(chunk)
				if onText != nil {
					onText(strings.ToValidUTF8(chunk, ""))
				}
			}
		case "usage_update":
			usage = event.Update.Usage
		case "tool_call", "tool_call_update":
			gateReceipts.collect(string(event.Update.RawOutput))
			gateReceipts.collect(string(event.Update.Content))
		}
	})
	turn := Turn{Text: strings.ToValidUTF8(output.String(), ""), Model: activeModel, Usage: usage, gateReceipts: gateReceipts}
	if err != nil {
		connection.end(http.MethodPost, map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": session.ID}})
		return turn, err
	}
	var result struct {
		StopReason string `json:"stopReason"`
		Usage      *Usage `json:"usage"`
		Meta       struct {
			Refusal json.RawMessage `json:"codebuddy.ai/errorMessage"`
		} `json:"_meta"`
	}
	if json.Unmarshal(reply, &result) != nil {
		return turn, errors.New("WorkBuddy ACP 返回了无效的执行结果")
	}
	if result.Usage != nil {
		turn.Usage = *result.Usage
	}
	if turn.Usage.Model == "" {
		turn.Usage.Model = activeModel
	}
	if result.StopReason != "end_turn" {
		return turn, turnStopError(result.StopReason, result.Meta.Refusal)
	}
	return turn, nil
}

func turnStopError(reason string, raw json.RawMessage) error {
	if reason == "refusal" && len(raw) != 0 {
		var message string
		if json.Unmarshal(raw, &message) == nil {
			raw = json.RawMessage(message)
		}
		var failure struct {
			Message string `json:"message"`
			Data    struct {
				Details   string `json:"details"`
				RequestID string `json:"requestId"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &failure) == nil {
			detail := strings.TrimSpace(failure.Data.Details)
			if detail == "" {
				detail = strings.TrimSpace(failure.Message)
			}
			if detail != "" {
				if requestID := strings.TrimSpace(failure.Data.RequestID); requestID != "" {
					return fmt.Errorf("WorkBuddy 拒绝执行：%s（请求 %s）", detail, requestID)
				}
				return fmt.Errorf("WorkBuddy 拒绝执行：%s", detail)
			}
		}
	}
	return fmt.Errorf("WorkBuddy 未正常完成本轮执行（%q）", reason)
}

func (c *acpConnection) open(ctx context.Context) error {
	stream, cancel := context.WithCancel(c.life)
	stop := context.AfterFunc(ctx, cancel)
	resp, err := c.send(stream, http.MethodGet, "", c.lastEventID, nil)
	stop()
	if err != nil {
		cancel()
		return err
	}
	id := resp.Header.Get("Acp-Connection-Id")
	if resp.StatusCode != http.StatusOK || id == "" || !isSSE(resp) {
		resp.Body.Close()
		cancel()
		return fmt.Errorf("WorkBuddy ACP 建连失败（HTTP %d）", resp.StatusCode)
	}
	c.connectionID = id
	frames := make(chan acpFrame, 8)
	c.updates = frames
	go func() {
		defer cancel()
		defer close(frames)
		defer resp.Body.Close()
		readSSE(stream, resp.Body, frames)
	}()
	return nil
}

func (c *acpConnection) rpc(ctx context.Context, method string, params any, update func(json.RawMessage)) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.sequence++
	wantID := strconv.Itoa(c.sequence)
	connectionID := c.connectionID
	request := map[string]any{"jsonrpc": "2.0", "id": c.sequence, "method": method, "params": params}
	frames := make(chan acpFrame, 8)
	go c.post(connectionID, request, frames)
	var posted <-chan acpFrame = frames
	for {
		var frame acpFrame
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case next, ok := <-posted:
			if !ok {
				posted = nil
				continue
			}
			frame = next
		case next, ok := <-c.updates:
			if !ok {
				if c.reconnected {
					return nil, errors.New("WorkBuddy ACP 事件流再次中断")
				}
				c.reconnected = true
				if err := c.open(ctx); err != nil {
					return nil, err
				}
				continue
			}
			if next.id != "" {
				if next.id == c.lastEventID {
					continue
				}
				c.lastEventID = next.id
			}
			frame = next
		}
		if frame.err != nil {
			return nil, frame.err
		}
		var message acpMessage
		if err := json.Unmarshal(frame.data, &message); err != nil {
			return nil, errors.New("WorkBuddy ACP 返回了无效事件")
		}
		switch {
		case message.Method == "session/update":
			if update != nil {
				update(message.Params)
			}
		case len(message.ID) != 0 && message.Method != "":
			if err := c.reply(ctx, message); err != nil {
				return nil, err
			}
		case string(message.ID) == wantID || string(message.ID) == strconv.Quote(wantID):
			if len(message.Error) > 0 && string(message.Error) != "null" {
				return nil, fmt.Errorf("WorkBuddy ACP %s 执行失败", method)
			}
			return message.Result, nil
		}
	}
}

func (c *acpConnection) post(connectionID string, request any, frames chan<- acpFrame) {
	defer close(frames)
	resp, err := c.send(c.life, http.MethodPost, connectionID, "", request)
	if err != nil {
		sendFrame(c.life, frames, acpFrame{err: err})
		return
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusAccepted:
	case resp.StatusCode == http.StatusOK && isSSE(resp):
		readSSE(c.life, resp.Body, frames)
	default:
		sendFrame(c.life, frames, acpFrame{err: fmt.Errorf("WorkBuddy ACP 请求失败（HTTP %d）", resp.StatusCode)})
	}
}

func (c *acpConnection) reply(ctx context.Context, message acpMessage) error {
	request := map[string]any{"jsonrpc": "2.0", "id": message.ID}
	if message.Method == "session/request_permission" {
		request["result"] = map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
	} else if message.Method == gateQuestionMethod && c.gate.UsesACPQuestion() {
		request["result"] = c.gate.answerACPQuestion(c.sessionID, message.Params)
	} else {
		request["error"] = map[string]any{"code": -32601, "message": "Unsupported client request"}
	}
	resp, err := c.send(ctx, http.MethodPost, c.connectionID, "", request)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("WorkBuddy ACP 回应失败（HTTP %d）", resp.StatusCode)
	}
	return nil
}

func (c *acpConnection) end(method string, request any) {
	ctx, cancel := context.WithTimeout(c.life, 4*time.Second)
	defer cancel()
	if resp, err := c.send(ctx, method, c.connectionID, "", request); err == nil {
		resp.Body.Close()
	}
}

func (c *acpConnection) send(ctx context.Context, method, connectionID, cursor string, payload any) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint.String(), body)
	if err != nil {
		return nil, errors.New("无法构造 WorkBuddy ACP 请求")
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("X-Agent-Session-Id", c.sessionID)
	request.Header.Set("Accept", "text/event-stream")
	if connectionID != "" {
		request.Header.Set("Acp-Connection-Id", connectionID)
	}
	if cursor != "" {
		request.Header.Set("Last-Event-ID", cursor)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("WorkBuddy ACP 连接中断")
	}
	return resp, nil
}

func isSSE(response *http.Response) bool {
	return strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream")
}

func readSSE(ctx context.Context, body io.Reader, frames chan<- acpFrame) {
	const frameLimit = 1 << 20
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), frameLimit)
	var data strings.Builder
	var id string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if data.Len() > 0 && !sendFrame(ctx, frames, acpFrame{data: []byte(strings.TrimSuffix(data.String(), "\n")), id: id}) {
				return
			}
			data.Reset()
			id = ""
		case strings.HasPrefix(line, "id:"):
			id = strings.TrimPrefix(line[3:], " ")
		case strings.HasPrefix(line, "data:"):
			if data.Len()+len(line) > frameLimit {
				sendFrame(ctx, frames, acpFrame{err: errors.New("WorkBuddy ACP 事件超过大小上限")})
				return
			}
			data.WriteString(strings.TrimPrefix(line[5:], " "))
			data.WriteByte('\n')
		}
	}
}

func sendFrame(ctx context.Context, frames chan<- acpFrame, frame acpFrame) bool {
	select {
	case frames <- frame:
		return true
	case <-ctx.Done():
		return false
	}
}
