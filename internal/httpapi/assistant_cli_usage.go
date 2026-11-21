package httpapi

import (
	"encoding/json"
	"strconv"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// assistantCLIUsage collects snapshots inside one invocation. A terminal usage
// object is authoritative; message snapshots are a fallback when the process
// fails without a result, and repeated start/delta/assistant frames do not add
// the same message's tokens twice.
type assistantCLIUsage struct {
	final     *domain.TokenUsage
	messages  map[string]map[string]any
	active    string
	anonymous bool
	next      int
}

func (c *assistantCLIUsage) consume(line string) {
	var frame struct {
		Type    string         `json:"type"`
		Usage   map[string]any `json:"usage"`
		Message struct {
			ID    string         `json:"id"`
			Usage map[string]any `json:"usage"`
		} `json:"message"`
		Event struct {
			Type    string         `json:"type"`
			Usage   map[string]any `json:"usage"`
			Message struct {
				ID    string         `json:"id"`
				Usage map[string]any `json:"usage"`
			} `json:"message"`
		} `json:"event"`
	}
	if json.Unmarshal([]byte(line), &frame) != nil {
		return
	}
	switch frame.Type {
	case "result", "error", "usage":
		if u := runtime.ParseTokenUsage(frame.Usage); assistantUsageReported(u) {
			c.final = &u
		}
	case "assistant":
		id := frame.Message.ID
		if id == "" {
			id = c.active
			if id == "" {
				c.next++
				id = "anonymous-" + strconv.Itoa(c.next)
			}
		} else if c.anonymous && c.active != "" {
			c.message(id, c.messages[c.active])
			delete(c.messages, c.active)
		}
		c.message(id, frame.Message.Usage)
		c.active, c.anonymous = "", false
	case "stream_event":
		switch frame.Event.Type {
		case "message_start":
			c.active = frame.Event.Message.ID
			c.anonymous = c.active == ""
			if c.anonymous {
				c.next++
				c.active = "anonymous-" + strconv.Itoa(c.next)
			}
			c.message(c.active, frame.Event.Message.Usage)
		case "message_delta":
			c.message(c.active, frame.Event.Usage)
		}
	}
}

func (c *assistantCLIUsage) message(id string, usage map[string]any) {
	if len(usage) == 0 {
		return
	}
	if id == "" {
		id = c.active
	}
	if c.messages == nil {
		c.messages = map[string]map[string]any{}
	}
	if c.messages[id] == nil {
		c.messages[id] = map[string]any{}
	}
	for key, value := range usage {
		c.messages[id][key] = value
	}
}

func (c *assistantCLIUsage) snapshot() domain.TokenUsage {
	if c.final != nil {
		return *c.final
	}
	var total assistantTurnUsage
	for _, message := range c.messages {
		total.add(runtime.ParseTokenUsage(message))
	}
	if u := total.snapshot(); u != nil {
		return *u
	}
	return domain.TokenUsage{}
}
