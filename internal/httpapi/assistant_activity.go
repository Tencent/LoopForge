package httpapi

import (
	"agentflow/internal/domain"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Bounds of the persisted activity transcript: one event's text, the whole
// log's payload bytes, and the number of events.
const (
	assistantActivityTextLimit  = 32000
	assistantActivityByteLimit  = 512 << 10
	assistantActivityEventLimit = 512
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
	case "answerReset":
		l.mu.Lock()
		defer l.mu.Unlock()
		l.dropTextLocked()
		return
	case "answer", "text":
		l.mu.Lock()
		defer l.mu.Unlock()
		l.appendTextLocked(event)
		return
	case "thinkingStart", "thinking", "tool", "toolDone", "status", "agentText":
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
	// Subagents run beside Main Bot, so only thoughts of the same agent join.
	if !starting && event.Kind == "thinking" && len(l.events) > 0 && l.events[len(l.events)-1].Kind == "thinking" && l.events[len(l.events)-1].Parent == event.Parent {
		last := &l.events[len(l.events)-1]
		if event.Full && strings.Contains(last.Text, strings.TrimSpace(event.Text)) {
			return
		}
		l.appendToLastLocked(last, event.Text)
		return
	}
	if event.Kind == "agentText" {
		for _, existing := range l.events {
			if existing.Kind == "agentText" && existing.Parent == event.Parent && existing.Text == event.Text {
				return
			}
		}
	}
	event.Input = truncateRunes(event.Input, 8000)
	event.Output = truncateRunes(event.Output, 8000)
	size := len(event.Text) + len(event.Detail) + len(event.Input) + len(event.Output)
	if len(l.events) >= assistantActivityEventLimit || l.bytes+size > assistantActivityByteLimit {
		l.truncated = true
		return
	}
	event.Text = truncateRunes(event.Text, assistantActivityTextLimit)
	event.Detail = truncateRunes(event.Detail, 4000)
	event.Full = false
	l.events = append(l.events, event)
	l.bytes += len(event.Text) + len(event.Detail) + len(event.Input) + len(event.Output)
}

// Text written between tool calls stays in the transcript, in order. A later
// tool opens the next paragraph instead of appending onto the previous one.
func (l *assistantActivityLog) appendTextLocked(event assistantStreamEvent) {
	text := event.Text
	if text == "" {
		return
	}
	// A token that is only a space or a newline is still Markdown. Dropping it
	// turns "## 标题" into "##标题" and glues a table to the heading.
	if strings.TrimSpace(text) == "" {
		if len(l.events) == 0 || l.events[len(l.events)-1].Kind != "text" {
			return
		}
		l.appendToLastLocked(&l.events[len(l.events)-1], text)
		return
	}
	// The CLI sends the finished block again after the tool that followed it.
	// That copy is already on screen; appending it would place the paragraph
	// twice, once on each side of the tool.
	if event.Full {
		trimmed := strings.TrimSpace(text)
		for _, existing := range l.events {
			if existing.Kind == "text" && strings.Contains(existing.Text, trimmed) {
				return
			}
		}
	}
	if len(l.events) > 0 && l.events[len(l.events)-1].Kind == "text" {
		last := &l.events[len(l.events)-1]
		trimmed := strings.TrimSpace(text)
		if event.Full && strings.Contains(last.Text, trimmed) {
			return
		}
		if event.Full && strings.TrimSpace(last.Text) != "" && (strings.HasPrefix(trimmed, strings.TrimSpace(last.Text)) || strings.HasPrefix(replyCompact(trimmed), replyCompact(last.Text))) {
			if len(text) > assistantActivityTextLimit || l.bytes-len(last.Text)+len(text) > assistantActivityByteLimit {
				l.truncated = true
				return
			}
			l.bytes += len(text) - len(last.Text)
			last.Text = text
			return
		}
		l.appendToLastLocked(last, text)
		return
	}
	if len(l.events) >= assistantActivityEventLimit || l.bytes+len(text) > assistantActivityByteLimit {
		l.truncated = true
		return
	}
	event.Kind = "text"
	event.Full = false
	event.Tool = ""
	event.Detail = ""
	event.Text = truncateRunes(text, assistantActivityTextLimit)
	if event.ID == "" {
		event.ID = fmt.Sprintf("text-%d", len(l.events))
	}
	l.events = append(l.events, event)
	l.bytes += len(event.Text)
}

// appendToLastLocked extends last, the log's final event, with text. When the
// event or the whole log would exceed its bound it marks the log truncated and
// leaves last unchanged.
func (l *assistantActivityLog) appendToLastLocked(last *domain.AssistantActivityEvent, text string) {
	if len(last.Text)+len(text) > assistantActivityTextLimit || l.bytes+len(text) > assistantActivityByteLimit {
		l.truncated = true
		return
	}
	last.Text += text
	l.bytes += len(text)
}

func (l *assistantActivityLog) dropTextLocked() {
	kept := make([]domain.AssistantActivityEvent, 0, len(l.events))
	used := 0
	for _, event := range l.events {
		if event.Kind == "text" {
			continue
		}
		kept = append(kept, event)
		used += len(event.Text) + len(event.Detail)
	}
	l.events = kept
	l.bytes = used
}

func replyCompact(text string) string {
	return strings.Join(strings.Fields(text), "")
}

// replyRicher keeps the copy that still has its spaces and line breaks when
// two strings are the same once whitespace is ignored.
func replyRicher(primary, alternate string) string {
	if len(alternate) > len(primary) {
		return alternate
	}
	return primary
}

func replyParagraphs(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	raw := strings.Split(text, "\n\n")
	out := make([]string, 0, len(raw))
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// dropRepeatedReply keeps distinct paragraphs. A trailing copy of the previous
// paragraph, or a second copy of the whole reply, is removed.
func dropRepeatedReply(text string) string {
	parts := replyParagraphs(text)
	for len(parts) >= 2 && replyCompact(parts[len(parts)-1]) == replyCompact(parts[len(parts)-2]) {
		parts = parts[:len(parts)-1]
	}
	if len(parts) >= 2 && len(parts)%2 == 0 {
		mid := len(parts) / 2
		if replyCompact(strings.Join(parts[:mid], "\n\n")) == replyCompact(strings.Join(parts[mid:], "\n\n")) {
			parts = parts[:mid]
		}
	}
	return strings.Join(parts, "\n\n")
}

// assistantVisibleReply keeps every paragraph the model wrote, including the
// ones that arrived before later tool calls. The CLI result is often only the
// last paragraph, and sometimes repeats that paragraph. A repeated copy is
// stored once.
func assistantVisibleReply(final string, events []domain.AssistantActivityEvent) string {
	final = stripAssistantCLINotices(final)
	parts := make([]string, 0, 4)
	for _, event := range events {
		if event.Kind != "text" {
			continue
		}
		if text := strings.TrimSpace(event.Text); text != "" && !assistantCLINoticeText(text) {
			parts = append(parts, text)
		}
	}
	joined := strings.Join(parts, "\n\n")
	final = strings.TrimSpace(final)
	if joined == "" {
		return dropRepeatedReply(final)
	}
	compactJoined := replyCompact(joined)
	compactFinal := replyCompact(final)
	if final == "" || compactFinal == "" || compactFinal == compactJoined+compactJoined || strings.Contains(joined, final) {
		return joined
	}
	if compactFinal == compactJoined {
		return replyRicher(joined, final)
	}
	if strings.Contains(final, joined) || strings.HasPrefix(compactFinal, compactJoined) {
		repeated := dropRepeatedReply(final)
		if replyCompact(repeated) == compactJoined {
			return replyRicher(joined, repeated)
		}
		return repeated
	}
	// The CLI result is usually only the last paragraph, sometimes with a
	// summary that never streamed as its own segment. Keep every earlier
	// paragraph and append just the new tail.
	last := parts[len(parts)-1]
	if replyCompact(final) == replyCompact(last) {
		if len(final) > len(last) && strings.HasSuffix(joined, last) {
			return strings.TrimSuffix(joined, last) + final
		}
		return joined
	}
	if strings.HasPrefix(final, last) {
		suffix := strings.TrimSpace(strings.TrimPrefix(final, last))
		if suffix == "" || replyCompact(suffix) == replyCompact(last) {
			return joined
		}
		return dropRepeatedReply(joined + "\n\n" + suffix)
	}
	return dropRepeatedReply(joined + "\n\n" + final)
}

// stripAssistantCLINotices drops the CLI's "Tool ... does not exist" lines
// from a reply. A reply made only of such lines is kept so the turn is not
// stored empty.
func stripAssistantCLINotices(text string) string {
	if !strings.Contains(text, "does not exist in the current tool set") {
		return text
	}
	lines := strings.Split(text, "\n")
	kept := lines[:0:0]
	for _, line := range lines {
		if !assistantCLINoticeText(line) {
			kept = append(kept, line)
		}
	}
	stripped := strings.TrimSpace(strings.Join(kept, "\n"))
	if stripped == "" {
		return text
	}
	return stripped
}

func transcriptWorthKeeping(text string, events []domain.AssistantActivityEvent) bool {
	if strings.TrimSpace(text) != "" {
		return true
	}
	for _, event := range events {
		switch event.Kind {
		case "text", "thinking":
			if strings.TrimSpace(event.Text) != "" {
				return true
			}
		case "tool", "toolDone":
			return true
		}
	}
	return false
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
	event.Input = redactAssistantSecrets(event.Input, user)
	event.Output = redactAssistantSecrets(event.Output, user)
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
		if out[i].Unfinished != "" {
			out[i].Content = assistantUnfinishedPromptContent(messages[i])
			out[i].Unfinished = ""
		}
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
