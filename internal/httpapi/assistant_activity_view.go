package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"agentflow/internal/domain"
)

func assistantActivityRef(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// The closed trace shows only the reply text. Keep the boundaries used by
// finalReply and enough dispatch metadata for the artifact panel to list agents.
func previewAssistantActivity(message *domain.AssistantMessage) {
	if len(message.Activity) == 0 {
		return
	}
	raw, _ := json.Marshal(message.Activity)
	if len(raw) <= 8<<10 {
		return
	}
	var preview []domain.AssistantActivityEvent
	dispatches := make(map[string]bool)
	textOpen := false
	for _, event := range message.Activity {
		if event.Parent != "" {
			// Older traces can start at a child's event, without an explicit
			// dispatch. The client still lists that parent as a subagent.
			if !dispatches[event.Parent] {
				dispatches[event.Parent] = true
				if event.Kind != "agentModel" {
					preview = append(preview, domain.AssistantActivityEvent{Kind: "agentModel", Parent: event.Parent})
				}
				textOpen = false
			}
			if event.Kind == "agentModel" {
				preview = append(preview, event)
			}
			continue
		}
		switch event.Kind {
		case "text", "answer", "answerReset":
			preview = append(preview, event)
			textOpen = event.Kind != "answerReset"
		case "thinkingStart", "thinking", "tool", "status":
			if event.Kind == "tool" && (strings.EqualFold(event.Tool, "Agent") || strings.EqualFold(event.Tool, "Task")) {
				dispatches[event.ID] = true
				event.Input, event.Output, event.Text = "", "", ""
				preview = append(preview, event)
			} else if textOpen {
				preview = append(preview, domain.AssistantActivityEvent{Kind: "thinkingStart"})
			}
			textOpen = false
		case "toolDone":
			if dispatches[event.ID] {
				event.Input, event.Output, event.Detail, event.Text = "", "", "", ""
				preview = append(preview, event)
			}
		}
	}
	// A trace made almost entirely of visible prose gains nothing from a
	// second request. Send it intact.
	projected, _ := json.Marshal(preview)
	if len(raw)-len(projected) < 4<<10 {
		return
	}
	message.ActivityRef = assistantActivityRef(raw)
	message.Activity = preview
}

// A slim trace can be echoed by both old-session metadata saves and new
// generations. Restore only the same stored row and revision before the
// normal transcript/permission checks. Never persist a client-supplied ref.
func restoreAssistantActivities(messages, stored []domain.AssistantMessage) bool {
	for i := range messages {
		message := &messages[i]
		if message.ActivityRef == "" {
			continue
		}
		if i >= len(stored) || message.Role != "assistant" || stored[i].Role != "assistant" {
			return false
		}
		raw, _ := json.Marshal(stored[i].Activity)
		if message.ActivityRef != assistantActivityRef(raw) {
			return false
		}
		message.Activity = stored[i].Activity
		message.ActivityRef = ""
	}
	return true
}
