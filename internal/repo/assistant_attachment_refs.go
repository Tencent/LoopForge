package repo

import (
	"strings"
)

// AssistantAttachmentsInUse reports which attachment ids a conversation other
// than exceptSessionID still lists, in its transcript or in a pending turn.
// Forks inherit the attachment ids of the messages before the fork point.
func (r *AssistantRepo) AssistantAttachmentsInUse(ids []string, exceptSessionID string) map[string]bool {
	exceptSessionID = assistantRecordID(exceptSessionID)
	inUse := payloadMentions(r.DB, "assistant_sessions", ids, "id = ?", exceptSessionID)
	for id := range payloadMentions(r.DB, "assistant_generations", ids, "session_id = ?", exceptSessionID) {
		inUse[id] = true
	}
	return inUse
}

// ExistingAssistantSessionIDs reports which of ids still name a conversation.
// Conversation ids are unique across owners.
func (r *AssistantRepo) ExistingAssistantSessionIDs(ids []string) map[string]bool {
	out := map[string]bool{}
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if _, seen := out[id]; id != "" && !seen {
			out[id] = false
			clean = append(clean, id)
		}
	}
	for start := 0; start < len(clean); start += 500 {
		chunk := clean[start:min(start+500, len(clean))]
		var found []string
		if err := r.DB.Table("assistant_sessions").Where("id IN ?", chunk).Pluck("id", &found).Error; err != nil {
			// Unknown is not gone: keep offering the link.
			for _, id := range chunk {
				out[id] = true
			}
			continue
		}
		for _, id := range found {
			out[id] = true
		}
	}
	return out
}
