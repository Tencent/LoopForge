package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type agentToolCallCancel struct {
	cancel context.CancelFunc
}

// agentToolRPCIDKey names a JSON-RPC request id. Numbers and strings are
// different ids, so the key keeps the type.
func agentToolRPCIDKey(id any) string {
	switch v := id.(type) {
	case nil:
		return ""
	case string:
		return "s:" + v
	case json.Number:
		return "n:" + v.String()
	case float64, int, int64:
		return "n:" + strings.TrimSuffix(fmt.Sprint(v), ".0")
	default:
		return ""
	}
}

// cancelledRequestID reads requestId from notifications/cancelled params.
func cancelledRequestID(params json.RawMessage) string {
	var payload struct {
		RequestID any `json:"requestId"`
	}
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return ""
	}
	return agentToolRPCIDKey(payload.RequestID)
}

// trackToolCall makes a tools/call cancellable by notifications/cancelled
// with the same id. done releases the entry and the context.
func (s *AgentToolSession) trackToolCall(ctx context.Context, id any) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	key := agentToolRPCIDKey(id)
	if key == "" {
		return ctx, cancel
	}
	entry := &agentToolCallCancel{cancel: cancel}
	s.mu.Lock()
	if s.toolCalls == nil {
		s.toolCalls = map[string]*agentToolCallCancel{}
	}
	s.toolCalls[key] = entry
	s.mu.Unlock()
	return ctx, func() {
		s.mu.Lock()
		if s.toolCalls[key] == entry {
			delete(s.toolCalls, key)
		}
		s.mu.Unlock()
		cancel()
	}
}

func (s *AgentToolSession) cancelToolCall(key string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	entry := s.toolCalls[key]
	delete(s.toolCalls, key)
	s.mu.Unlock()
	if entry != nil {
		entry.cancel()
	}
}
