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
	if message.Role != "assistant" || len(message.Activity) == 0 {
		return
	}
	raw, _ := json.Marshal(message.Activity)
	if len(raw) <= 8<<10 {
		return
	}
	var preview []domain.AssistantActivityEvent
	// A failed completion without a preceding tool opens its own row in the
	// client. Completions for known tools update an existing row in place.
	toolIsDispatch := make(map[string]bool)
	textOpen := false
	for _, event := range message.Activity {
		if event.Parent != "" {
			// Older traces can start at a child's event, without an explicit
			// dispatch. The client still lists that parent as a subagent.
			if _, seen := toolIsDispatch[event.Parent]; !seen {
				toolIsDispatch[event.Parent] = true
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
			if event.Kind != "answerReset" && event.Text == "" {
				continue
			}
			preview = append(preview, event)
			textOpen = event.Kind != "answerReset"
		case "thinkingStart", "thinking", "tool", "status":
			if event.Kind == "status" && event.Text == "" {
				continue
			}
			if event.Kind == "tool" {
				dispatch := strings.EqualFold(event.Tool, "Agent") || strings.EqualFold(event.Tool, "Task") ||
					(event.Tool == "" && toolIsDispatch[event.ID])
				toolIsDispatch[event.ID] = dispatch
				if dispatch {
					event.Input, event.Output, event.Text = "", "", ""
					preview = append(preview, event)
					textOpen = false
					continue
				}
			}
			if textOpen {
				preview = append(preview, domain.AssistantActivityEvent{Kind: "thinkingStart"})
			}
			textOpen = false
		case "toolDone":
			dispatch, seen := toolIsDispatch[event.ID]
			if dispatch {
				event.Input, event.Output, event.Detail, event.Text = "", "", "", ""
				preview = append(preview, event)
			} else if !seen && event.Failed {
				toolIsDispatch[event.ID] = false
				if textOpen {
					preview = append(preview, domain.AssistantActivityEvent{Kind: "thinkingStart"})
					textOpen = false
				}
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
