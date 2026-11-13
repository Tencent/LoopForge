package runner

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"agentflow/internal/domain"
)

func newConversationDaemon() *Daemon {
	return &Daemon{conversationReplies: map[string]chan domain.RunnerToolResponse{}}
}

func pendingReplies(d *Daemon) int {
	d.conversationMu.Lock()
	defer d.conversationMu.Unlock()
	return len(d.conversationReplies)
}

// replyingWriter answers every agent_tool.request it sees through
// deliverConversationReply, standing in for the platform side of the socket.
func replyingWriter(t *testing.T, d *Daemon, response domain.RunnerToolResponse, sent *controlMessage) func(controlMessage) error {
	t.Helper()
	return func(message controlMessage) error {
		*sent = message
		go d.deliverConversationReply(message.RequestID, response)
		return nil
	}
}

func TestForwardConversationToolReturnsReplyBody(t *testing.T) {
	d := newConversationDaemon()
	var sent controlMessage
	write := replyingWriter(t, d, domain.RunnerToolResponse{Body: json.RawMessage(`{"ok":true}`)}, &sent)
	body, err := d.forwardConversationTool(context.Background(), context.Background(), write, "fixture-token", json.RawMessage(`{"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %s", body)
	}
	if sent.Type != "agent_tool.request" || sent.RequestID == "" || sent.ToolRequest == nil || sent.ToolRequest.Token != "fixture-token" || string(sent.ToolRequest.Body) != `{"method":"ping"}` {
		t.Fatalf("unexpected request: %+v", sent)
	}
	if pendingReplies(d) != 0 {
		t.Fatalf("reply channel not cleaned up: %d pending", pendingReplies(d))
	}
}

func TestForwardConversationToolReturnsReplyError(t *testing.T) {
	d := newConversationDaemon()
	var sent controlMessage
	write := replyingWriter(t, d, domain.RunnerToolResponse{Error: "tool denied"}, &sent)
	_, err := d.forwardConversationTool(context.Background(), context.Background(), write, "token", nil)
	if err == nil || err.Error() != "tool denied" {
		t.Fatalf("err = %v, want tool denied", err)
	}
}

func TestForwardConversationToolReturnsWriteError(t *testing.T) {
	d := newConversationDaemon()
	writeErr := errors.New("socket closed")
	_, err := d.forwardConversationTool(context.Background(), context.Background(), func(controlMessage) error { return writeErr }, "token", nil)
	if !errors.Is(err, writeErr) {
		t.Fatalf("err = %v, want %v", err, writeErr)
	}
	if pendingReplies(d) != 0 {
		t.Fatalf("reply channel not cleaned up: %d pending", pendingReplies(d))
	}
}

func TestForwardConversationToolStopsOnCallerOrConnectionCancel(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	noReply := func(controlMessage) error { return nil }
	for name, contexts := range map[string][2]context.Context{
		"caller":     {context.Background(), cancelled},
		"connection": {cancelled, context.Background()},
	} {
		t.Run(name, func(t *testing.T) {
			d := newConversationDaemon()
			_, err := d.forwardConversationTool(contexts[0], contexts[1], noReply, "token", nil)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			if pendingReplies(d) != 0 {
				t.Fatalf("reply channel not cleaned up: %d pending", pendingReplies(d))
			}
		})
	}
}

func TestDeliverConversationReplyIgnoresUnknownAndDuplicateReplies(t *testing.T) {
	d := newConversationDaemon()
	d.deliverConversationReply("missing", domain.RunnerToolResponse{Body: json.RawMessage(`1`)})

	reply := make(chan domain.RunnerToolResponse, 1)
	d.conversationReplies["tool-1"] = reply
	d.deliverConversationReply("tool-1", domain.RunnerToolResponse{Body: json.RawMessage(`"first"`)})
	d.deliverConversationReply("tool-1", domain.RunnerToolResponse{Body: json.RawMessage(`"second"`)})
	if got := <-reply; string(got.Body) != `"first"` {
		t.Fatalf("delivered %s, want first reply", got.Body)
	}
	select {
	case extra := <-reply:
		t.Fatalf("duplicate reply was queued: %s", extra.Body)
	default:
	}
}
