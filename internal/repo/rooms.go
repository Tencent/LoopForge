package repo

import (
	"encoding/json"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// RoomsRepo stores project group chats and their messages.
type RoomsRepo struct {
	*deps
}

func (r *RoomsRepo) Rooms(projectID string) []domain.ChatRoom {
	projectID = strings.TrimSpace(projectID)
	q := r.DB.Table("chat_rooms").Order("updated_at desc")
	if projectID != "" {
		q = q.Where("project_id = ?", projectID)
	}
	var rows []chatRoomRow
	if err := q.Find(&rows).Error; err != nil {
		return []domain.ChatRoom{}
	}
	out := make([]domain.ChatRoom, 0, len(rows))
	for _, row := range rows {
		var item domain.ChatRoom
		if json.Unmarshal(row.Payload, &item) != nil {
			continue
		}
		if item.ID == "" {
			item.ID = row.ID
		}
		if item.ProjectID == "" {
			item.ProjectID = row.ProjectID
		}
		if item.Name == "" {
			item.Name = row.Name
		}
		out = append(out, item)
	}
	return out
}

func (r *RoomsRepo) Room(id string) (domain.ChatRoom, bool) {
	item, ok := payloadOne[domain.ChatRoom](r.DB, "chat_rooms", strings.TrimSpace(id))
	return item, ok
}

func (r *RoomsRepo) PutRoom(v domain.ChatRoom) error {
	now := time.Now().UTC()
	if old, ok := r.Room(v.ID); ok {
		v.CreatedAt = old.CreatedAt
		if v.CreatedBy == 0 {
			v.CreatedBy = old.CreatedBy
		}
		if v.ProjectID == "" {
			v.ProjectID = old.ProjectID
		}
	} else if v.CreatedAt.IsZero() {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	return upsert(r.DB, "chat_rooms", map[string]any{
		"id": v.ID, "project_id": v.ProjectID, "name": v.Name, "created_by": v.CreatedBy,
		"payload": mustJSON(v), "created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
	})
}

func (r *RoomsRepo) Messages(roomID string, limit int) []domain.ChatMessage {
	roomID = strings.TrimSpace(roomID)
	if roomID == "" {
		return []domain.ChatMessage{}
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	var rows []chatMessageRow
	// Newest page first, then flip so the thread reads top-down.
	if err := r.DB.Table("chat_messages").Where("room_id = ?", roomID).Order("created_at desc").Order("id desc").Limit(limit).Find(&rows).Error; err != nil {
		return []domain.ChatMessage{}
	}
	out := make([]domain.ChatMessage, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		var item domain.ChatMessage
		if json.Unmarshal(row.Payload, &item) != nil {
			continue
		}
		if item.ID == "" {
			item.ID = row.ID
		}
		if item.RoomID == "" {
			item.RoomID = row.RoomID
		}
		out = append(out, item)
	}
	return out
}

// DispatchMessage finds the dispatch card in roomID that started runID, even
// when it has scrolled out of the latest Messages page.
func (r *RoomsRepo) DispatchMessage(roomID, runID string) (domain.ChatMessage, bool) {
	roomID, runID = strings.TrimSpace(roomID), strings.TrimSpace(runID)
	if roomID == "" || runID == "" {
		return domain.ChatMessage{}, false
	}
	var rows []chatMessageRow
	if err := r.DB.Table("chat_messages").Where("room_id = ? AND kind = ?", roomID, domain.ChatKindDispatch).Order("created_at desc").Find(&rows).Error; err != nil {
		return domain.ChatMessage{}, false
	}
	for _, row := range rows {
		var item domain.ChatMessage
		if json.Unmarshal(row.Payload, &item) != nil || item.RunID != runID {
			continue
		}
		if item.ID == "" {
			item.ID = row.ID
		}
		if item.RoomID == "" {
			item.RoomID = row.RoomID
		}
		return item, true
	}
	return domain.ChatMessage{}, false
}

func (r *RoomsRepo) PutMessage(v domain.ChatMessage) error {
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	return upsert(r.DB, "chat_messages", map[string]any{
		"id": v.ID, "room_id": v.RoomID, "user_id": v.UserID, "kind": v.Kind, "issue_id": v.IssueID,
		"payload": mustJSON(v), "created_at": timePtr(v.CreatedAt),
	})
}
