package httpapi

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

type assistantStreamProjection struct {
	answer   string
	draft    string
	activity assistantActivityLog
}

func (p *assistantStreamProjection) add(raw json.RawMessage) {
	var ev assistantStreamEvent
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	p.activity.add(ev)
	switch ev.Kind {
	case "answerReset":
		p.answer = ""
		p.draft = ""
	case "answer":
		if !ev.Full || !strings.Contains(p.answer, strings.TrimSpace(ev.Text)) {
			p.answer += ev.Text
		}
	case "draftStart":
		p.draft = ""
	case "draftDelta":
		if len(p.draft) < 256<<10 {
			p.draft += ev.Detail
		}
	}
}
func (p *assistantStreamProjection) snapshot(sequence int) *domain.AssistantStreamSnapshot {
	return &domain.AssistantStreamSnapshot{Sequence: sequence, Answer: p.answer, DraftMessage: assistantDraftMessagePrefix(p.draft), DraftJSON: p.draft, Activity: p.activity.snapshot()}
}

// Decode only complete JSON characters; partial escapes must not flash on screen.
func assistantDraftMessagePrefix(raw string) string {
	marker := `"message"`
	at := strings.Index(raw, marker)
	if at < 0 {
		return ""
	}
	rest := strings.TrimSpace(raw[at+len(marker):])
	if !strings.HasPrefix(rest, ":") {
		return ""
	}
	rest = strings.TrimSpace(rest[1:])
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	safe := 1
	for i := 1; i < len(rest); {
		switch rest[i] {
		case '"':
			var text string
			_ = json.Unmarshal([]byte(rest[:i+1]), &text)
			return text
		case '\\':
			if i+1 >= len(rest) {
				i = len(rest)
				break
			}
			count := 2
			if rest[i+1] == 'u' {
				count = 6
			}
			if i+count > len(rest) {
				i = len(rest)
				break
			}
			i += count
			safe = i
		default:
			i++
			safe = i
		}
	}
	var text string
	_ = json.Unmarshal([]byte(rest[:safe]+`"`), &text)
	return text
}

type assistantLiveGeneration struct {
	mu      sync.Mutex
	changed chan struct{}
	read    func() domain.AssistantGeneration
}

var assistantLiveGenerations sync.Map

func (l *assistantLiveGeneration) publish() {
	l.mu.Lock()
	close(l.changed)
	l.changed = make(chan struct{})
	l.mu.Unlock()
}
func (l *assistantLiveGeneration) signal() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.changed
}
func writeAssistantSSE(c *gin.Context, g domain.AssistantGeneration) {
	raw, _ := json.Marshal(g)
	_, _ = c.Writer.Write(append(append([]byte("event: generation\ndata: "), raw...), []byte("\n\n")...))
	c.Writer.Flush()
}

// assistantPersistedPollInterval matches the owner's checkpoint cadence.
var assistantPersistedPollInterval = 500 * time.Millisecond

// reload returns the session's current persisted generation; it lets a
// process stream a turn that another process (e.g. one draining after a
// handover) is executing.
func serveAssistantGenerationStream(c *gin.Context, persisted domain.AssistantGeneration, reload func() (domain.AssistantGeneration, bool)) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")
	value, ok := assistantLiveGenerations.Load(persisted.ID)
	if !ok {
		if persisted.Status != "running" || reload == nil {
			writeAssistantSSE(c, persisted)
			return
		}
		streamPersistedAssistantGeneration(c, persisted, reload)
		return
	}
	live := value.(*assistantLiveGeneration)
	cursor := 0
	first := true
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		changed := live.signal()
		g := live.read()
		if first || (len(g.Events) > 0 && cursor < g.Events[0].Sequence-1) {
			if g.Stream != nil {
				cursor = g.Stream.Sequence
				g.Events = nil
			}
			first = false
		} else {
			g.Stream = nil
			g.Events = assistantEventsAfter(g.Events, cursor)
		}
		cursor = latestAssistantEventSequence(g.Events, cursor)
		writeAssistantSSE(c, g)
		if g.Status != "running" {
			return
		}
		select {
		case <-changed:
		case <-c.Request.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = c.Writer.Write([]byte(": heartbeat\n\n"))
			c.Writer.Flush()
		}
	}
}

func streamPersistedAssistantGeneration(c *gin.Context, g domain.AssistantGeneration, reload func() (domain.AssistantGeneration, bool)) {
	cursor := 0
	if g.Stream != nil {
		cursor = g.Stream.Sequence
	}
	cursor = latestAssistantEventSequence(g.Events, cursor)
	writeAssistantSSE(c, g)
	id := g.ID
	poll := time.NewTicker(assistantPersistedPollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = c.Writer.Write([]byte(": heartbeat\n\n"))
			c.Writer.Flush()
			continue
		case <-poll.C:
		}
		if value, ok := assistantLiveGenerations.Load(id); ok {
			// The turn moved into this process; its live stream continues.
			g = value.(*assistantLiveGeneration).read()
		} else {
			current, ok := reload()
			if !ok || current.ID != id {
				return
			}
			g = current
		}
		if len(g.Events) > 0 && cursor < g.Events[0].Sequence-1 && g.Stream != nil {
			cursor = g.Stream.Sequence
			g.Events = nil
		} else {
			g.Stream = nil
			events := assistantEventsAfter(g.Events, cursor)
			cursor = latestAssistantEventSequence(events, cursor)
			g.Events = events
			if len(events) == 0 && g.Status == "running" {
				continue
			}
		}
		writeAssistantSSE(c, g)
		if g.Status != "running" {
			return
		}
	}
}

// assistantEventsAfter returns the events whose sequence is past cursor. The
// result is never nil so an empty batch still serializes as [].
func assistantEventsAfter(events []domain.AssistantGenerationEvent, cursor int) []domain.AssistantGenerationEvent {
	out := make([]domain.AssistantGenerationEvent, 0)
	for _, e := range events {
		if e.Sequence > cursor {
			out = append(out, e)
		}
	}
	return out
}

// latestAssistantEventSequence advances cursor to the highest event sequence.
func latestAssistantEventSequence(events []domain.AssistantGenerationEvent, cursor int) int {
	for _, e := range events {
		cursor = max(cursor, e.Sequence)
	}
	return cursor
}
