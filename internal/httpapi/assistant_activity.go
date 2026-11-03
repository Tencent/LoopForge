package httpapi

import (
	"agentflow/internal/domain"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Coalesce token deltas while retaining the order of thinking, tools and status.
// Bound transcript metadata independently of the live generation event window.
type assistantActivityLog struct {
	mu        sync.Mutex
	events    []domain.AssistantActivityEvent
	bytes     int
	truncated bool
}

func (l *assistantActivityLog) add(event assistantStreamEvent) {
	switch event.Kind {
	case "thinkingStart", "thinking", "tool", "toolDone", "status":
	default:
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	starting := event.Kind == "thinkingStart"
	if starting {
		event.Kind = "thinking"
		event.ID = fmt.Sprintf("thinking-%d", len(l.events))
		event.Text = ""
	}
	if !starting && event.Kind == "thinking" && len(l.events) > 0 && l.events[len(l.events)-1].Kind == "thinking" {
		last := &l.events[len(l.events)-1]
		if event.Full && strings.Contains(last.Text, strings.TrimSpace(event.Text)) {
			return
		}
		if len(last.Text)+len(event.Text) > 32000 || l.bytes+len(event.Text) > 512<<10 {
			l.truncated = true
			return
		}
		last.Text += event.Text
		l.bytes += len(event.Text)
		return
	}
	if len(l.events) >= 512 || l.bytes+len(event.Text)+len(event.Detail) > 512<<10 {
		l.truncated = true
		return
	}
	event.Text = truncateRunes(event.Text, 32000)
	event.Detail = truncateRunes(event.Detail, 4000)
	event.Full = false
	l.events = append(l.events, event)
	l.bytes += len(event.Text) + len(event.Detail)
}
func (l *assistantActivityLog) snapshot() []domain.AssistantActivityEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := append([]domain.AssistantActivityEvent(nil), l.events...)
	if l.truncated {
		out = append(out, domain.AssistantActivityEvent{Kind: "status", Text: "部分执行记录超过保存上限，已省略。"})
	}
	return out
}
func publicAssistantEvent(event assistantStreamEvent, user domain.User) assistantStreamEvent {
	if event.At == nil {
		now := time.Now().UTC()
		event.At = &now
	}
	event.Text = redactAssistantSecrets(event.Text, user)
	event.Detail = redactAssistantSecrets(event.Detail, user)
	return event
}

// Execution metadata belongs to the UI, never the model's conversation prompt.
func assistantPromptMessages(messages []assistantMessage) []assistantMessage {
	out := append([]assistantMessage(nil), messages...)
	for i := range out {
		out[i].Activity = nil
		out[i].RunIDs = nil
		out[i].Assets = nil
		out[i].Model = ""
		out[i].Usage = nil
	}
	return out
}

func (l *assistantActivityLog) forUser(user domain.User) []domain.AssistantActivityEvent {
	out := l.snapshot()
	for i := range out {
		out[i] = publicAssistantEvent(out[i], user)
	}
	return out
}
