// Package botinteraction implements the official enterprise WeChat AI Bot
// WebSocket protocol. No inbound HTTP callback endpoint is exposed.
package botinteraction

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type ReplyError struct{ Code int }

func (e *ReplyError) Error() string {
	return "企业微信拒绝了这次回复，请检查接收会话和 Bot 授权"
}

type Frame struct {
	Cmd     string `json:"cmd,omitempty"`
	Headers struct {
		RequestID string `json:"req_id"`
	} `json:"headers"`
	Body json.RawMessage `json:"body,omitempty"`
	Code int             `json:"errcode,omitempty"`
}
type Incoming struct {
	MessageID string `json:"msgid"`
	BotID     string `json:"aibotid"`
	ChatID    string `json:"chatid"`
	ChatType  string `json:"chattype"`
	From      struct {
		UserID string `json:"userid"`
	} `json:"from"`
	Type string `json:"msgtype"`
	Text struct {
		Content string `json:"content"`
	} `json:"text"`
	Voice struct {
		Content string `json:"content"`
	} `json:"voice"`
	Quote struct {
		Text struct {
			Content string `json:"content"`
		} `json:"text"`
	} `json:"quote"`
	Event struct {
		Type              string `json:"eventtype"`
		Key               string `json:"event_key"`
		TaskID            string `json:"task_id"`
		TemplateCardEvent struct {
			Key    string `json:"event_key"`
			TaskID string `json:"task_id"`
		} `json:"template_card_event"`
	} `json:"event"`
}
type socket struct {
	streams sync.Map
	conn    *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan int
	done    chan struct{}
	cancel  context.CancelFunc
}

func dial(ctx context.Context, url, botID, secret string, onFrame func(*socket, Frame)) (*socket, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		return nil, errors.New("企业微信长连接建立失败")
	}
	socketCtx, cancel := context.WithCancel(ctx)
	s := &socket{conn: conn, pending: map[string]chan int{}, done: make(chan struct{}), cancel: cancel}
	conn.SetReadLimit(1 << 20)
	go func() {
		defer close(s.done)
		defer cancel()
		defer conn.Close()
		for {
			var f Frame
			if err := conn.ReadJSON(&f); err != nil {
				return
			}
			s.mu.Lock()
			ack := s.pending[f.Headers.RequestID]
			s.mu.Unlock()
			if f.Cmd != "" && f.Cmd != "aibot_msg_callback" {
				var body map[string]json.RawMessage
				_ = json.Unmarshal(f.Body, &body)
				keys := []string{}
				for key := range body {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				var event map[string]json.RawMessage
				_ = json.Unmarshal(body["event"], &event)
				eventKeys := []string{}
				for key := range event {
					eventKeys = append(eventKeys, key)
				}
				sort.Strings(eventKeys)
				var cardEvent map[string]json.RawMessage
				_ = json.Unmarshal(event["template_card_event"], &cardEvent)
				cardKeys := []string{}
				for key := range cardEvent {
					cardKeys = append(cardKeys, key)
				}
				sort.Strings(cardKeys)
				log.Printf("wecom frame cmd=%s body_keys=%v event_keys=%v card_keys=%v", f.Cmd, keys, eventKeys, cardKeys)
			}
			if f.Cmd == "aibot_msg_callback" || f.Cmd == "aibot_event_callback" {
				onFrame(s, f)
			} else if ack != nil {
				select {
				case ack <- f.Code:
				default:
				}
			}
		}
	}()
	go func() { <-socketCtx.Done(); conn.Close() }()
	if err := s.request(ctx, "aibot_subscribe", "", map[string]any{"bot_id": botID, "secret": secret}); err != nil {
		s.close()
		return nil, errors.New("企业微信长连接认证失败")
	}
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-socketCtx.Done():
				return
			case <-ticker.C:
				if err := s.request(socketCtx, "ping", "", nil); err != nil {
					s.close()
					return
				}
			}
		}
	}()
	return s, nil
}
func (s *socket) close() { s.cancel(); s.conn.Close() }
func (s *socket) request(ctx context.Context, cmd, id string, body any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if id == "" {
		id = randomID()
	}
	ack := make(chan int, 1)
	s.mu.Lock()
	if _, exists := s.pending[id]; exists {
		s.mu.Unlock()
		return errors.New("相同回复仍在等待回执")
	}
	s.pending[id] = ack
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()
	payload := map[string]any{"cmd": cmd, "headers": map[string]string{"req_id": id}}
	if body != nil {
		payload["body"] = body
	}
	s.writeMu.Lock()
	s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := s.conn.WriteJSON(payload)
	s.writeMu.Unlock()
	if err != nil {
		return errors.New("企业微信回复连接中断")
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case code := <-ack:
		if code != 0 {
			return &ReplyError{Code: code}
		}
		return nil
	case <-timer.C:
		return errors.New("企业微信回复回执超时")
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errors.New("企业微信连接已关闭")
	}
}
func (s *socket) send(ctx context.Context, chatID, text string) error {
	return s.request(ctx, "aibot_send_msg", "", map[string]any{"chatid": chatID, "msgtype": "markdown", "markdown": map[string]string{"content": text}})
}
