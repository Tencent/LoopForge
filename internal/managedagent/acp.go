package managedagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxACPText = 1 << 20

type acpMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error,omitempty"`
}

type acpEvent struct {
	message acpMessage
	err     error
}

type acpConnection struct {
	ctx      context.Context
	stop     context.CancelFunc
	client   *http.Client
	url      string
	token    string
	session  string
	id       string
	nextID   int
	messages <-chan acpEvent
}

func (w *WMA) connect(ctx context.Context, endpoint, token string) (*acpConnection, error) {
	ctx, stop := context.WithCancel(ctx)
	acp := &acpConnection{ctx: ctx, stop: stop, client: &w.client, url: endpoint, token: token, session: w.config.SessionID}
	res, err := acp.request(ctx, http.MethodGet, nil)
	if err != nil {
		stop()
		return nil, err
	}
	acp.id = res.Header.Get("Acp-Connection-Id")
	if acp.id == "" || !isEventStream(res) {
		res.Body.Close()
		stop()
		return nil, errors.New("WMA ACP did not return an SSE connection ID")
	}
	acp.messages = readEvents(ctx, res.Body)
	return acp, nil
}

func (a *acpConnection) request(ctx context.Context, method string, input any) (*http.Response, error) {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-Agent-Session-Id", a.session)
	req.Header.Set("Accept", "text/event-stream")
	if a.id != "" {
		req.Header.Set("Acp-Connection-Id", a.id)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("WMA ACP %s: %w", method, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, fmt.Errorf("WMA ACP %s: HTTP %d", method, res.StatusCode)
	}
	return res, nil
}

func isEventStream(res *http.Response) bool {
	contentType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	return contentType == "text/event-stream"
}

func (a *acpConnection) call(method string, params any, update func(acpMessage) error) (json.RawMessage, error) {
	a.nextID++
	id := strconv.Itoa(a.nextID)
	ctx, stop := context.WithCancel(a.ctx)
	defer stop()
	res, err := a.request(ctx, http.MethodPost, map[string]any{
		"jsonrpc": "2.0", "id": a.nextID, "method": method, "params": params,
	})
	if err != nil {
		return nil, err
	}
	var posted <-chan acpEvent
	if isEventStream(res) {
		posted = readEvents(ctx, res.Body)
	} else {
		res.Body.Close()
	}
	for a.messages != nil || posted != nil {
		var event acpEvent
		var open bool
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case event, open = <-a.messages:
			if !open {
				a.messages = nil
				continue
			}
		case event, open = <-posted:
			if !open {
				posted = nil
				continue
			}
		}
		if event.err != nil {
			return nil, event.err
		}
		msg := event.message
		if msg.Method != "" {
			if len(msg.ID) > 0 {
				if err := a.answerRequest(ctx, msg); err != nil {
					return nil, err
				}
			} else if update != nil {
				if err := update(msg); err != nil {
					return nil, err
				}
			}
			continue
		}
		if string(msg.ID) != id {
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("WMA ACP %s: JSON-RPC error %d", method, msg.Error.Code)
		}
		return msg.Result, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("WMA ACP stream ended before %s completed", method)
}

func (a *acpConnection) answerRequest(ctx context.Context, msg acpMessage) error {
	reply := map[string]any{"jsonrpc": "2.0", "id": msg.ID}
	if msg.Method == "session/request_permission" {
		reply["result"] = map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}
	} else {
		reply["error"] = map[string]any{"code": -32601, "message": "Method not supported"}
	}
	res, err := a.request(ctx, http.MethodPost, reply)
	if err == nil {
		res.Body.Close()
	}
	return err
}

func (a *acpConnection) prompt(text string, onText func(string)) (string, error) {
	var output strings.Builder
	result, err := a.call("session/prompt", map[string]any{
		"sessionId": a.session,
		"prompt":    []map[string]string{{"type": "text", "text": text}},
	}, func(msg acpMessage) error {
		if msg.Method != "session/update" {
			return nil
		}
		var event struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				Kind    string `json:"sessionUpdate"`
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if json.Unmarshal(msg.Params, &event) != nil || event.SessionID != a.session || event.Update.Kind != "agent_message_chunk" || event.Update.Content.Type != "text" {
			return nil
		}
		delta := event.Update.Content.Text
		if output.Len()+len(delta) > maxACPText {
			return errors.New("WMA ACP reply exceeds 1 MiB")
		}
		output.WriteString(delta)
		if onText != nil && delta != "" {
			onText(delta)
		}
		return nil
	})
	if err != nil {
		a.cleanupRequest(http.MethodPost, map[string]any{
			"jsonrpc": "2.0", "method": "session/cancel",
			"params": map[string]string{"sessionId": a.session},
		})
		return output.String(), err
	}
	var terminal struct {
		StopReason string `json:"stopReason"`
	}
	if json.Unmarshal(result, &terminal) != nil || terminal.StopReason == "" {
		return output.String(), errors.New("WMA ACP returned no stop reason")
	}
	if terminal.StopReason != "end_turn" {
		switch terminal.StopReason {
		case "max_tokens", "max_turn_requests", "refusal", "cancelled":
			return output.String(), fmt.Errorf("WMA ACP stopped: %s", terminal.StopReason)
		default:
			return output.String(), errors.New("WMA ACP returned an unsupported stop reason")
		}
	}
	return output.String(), nil
}

func (a *acpConnection) disconnect() {
	defer a.stop()
	a.cleanupRequest(http.MethodDelete, nil)
}

func (a *acpConnection) cleanupRequest(method string, body any) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if res, err := a.request(ctx, method, body); err == nil {
		res.Body.Close()
	}
}

// readEvents emits one message for each complete SSE event. Empty heartbeat
// lines and comments do not produce messages.
func readEvents(ctx context.Context, body io.ReadCloser) <-chan acpEvent {
	events := make(chan acpEvent, 16)
	go func() {
		defer close(events)
		defer body.Close()
		send := func(event acpEvent) bool {
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 4096), maxACPText)
		var data strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" && data.Len() > 0 {
				var msg acpMessage
				if json.Unmarshal([]byte(data.String()), &msg) != nil {
					send(acpEvent{err: errors.New("invalid WMA ACP event")})
					return
				}
				data.Reset()
				if !send(acpEvent{message: msg}) {
					return
				}
			} else if value, ok := strings.CutPrefix(line, "data:"); ok {
				if data.Len()+len(value)+1 > maxACPText {
					send(acpEvent{err: errors.New("WMA ACP event exceeds 1 MiB")})
					return
				}
				data.WriteString(strings.TrimPrefix(value, " "))
				data.WriteByte('\n')
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			send(acpEvent{err: fmt.Errorf("read WMA ACP stream: %w", err)})
		}
	}()
	return events
}
