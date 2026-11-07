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

// TimelineMessages returns the newest page of one timeline: the main one when
// threadID is empty, otherwise that thread.
// MessageWithAttachment finds the room message that carries an attachment.
func (r *RoomsRepo) MessageWithAttachment(roomID, attachmentID string) (domain.ChatMessage, bool) {
	if r == nil || r.DB == nil || roomID == "" || attachmentID == "" || strings.ContainsAny(attachmentID, "%_\\") {
		return domain.ChatMessage{}, false
	}
	var rows []chatMessageRow
	if err := r.DB.Table("chat_messages").Where("room_id = ? AND CAST(payload AS CHAR) LIKE ?", roomID, "%"+attachmentID+"%").Limit(20).Find(&rows).Error; err != nil {
		return domain.ChatMessage{}, false
	}
	for _, row := range rows {
		var item domain.ChatMessage
		if json.Unmarshal(row.Payload, &item) != nil {
			continue
		}
		for _, a := range item.Attachments {
			if a.ID == attachmentID {
				return item, true
			}
		}
	}
	return domain.ChatMessage{}, false
}

func (r *RoomsRepo) TimelineMessages(roomID, threadID string, limit int) []domain.ChatMessage {
	roomID, threadID = strings.TrimSpace(roomID), strings.TrimSpace(threadID)
	if roomID == "" {
		return []domain.ChatMessage{}
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	q := r.DB.Table("chat_messages").Where("room_id = ?", roomID)
	if threadID == "" {
		q = q.Where("(thread_id = '' OR thread_id IS NULL)")
	} else {
		q = q.Where("thread_id = ?", threadID)
	}
	var rows []chatMessageRow
	if err := q.Order("created_at desc").Order("id desc").Limit(limit).Find(&rows).Error; err != nil {
		return []domain.ChatMessage{}
	}
	out := make([]domain.ChatMessage, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		var item domain.ChatMessage
		if json.Unmarshal(rows[i].Payload, &item) != nil {
			continue
		}
		if item.ID == "" {
			item.ID = rows[i].ID
		}
		if item.RoomID == "" {
			item.RoomID = rows[i].RoomID
		}
		out = append(out, item)
	}
	return out
}

// ThreadMessageCounts returns how many messages each thread of roomID holds.
func (r *RoomsRepo) ThreadMessageCounts(roomID string) map[string]int {
	type row struct {
		ThreadID string
		N        int
	}
	var rows []row
	out := map[string]int{}
	if err := r.DB.Table("chat_messages").Select("thread_id, COUNT(*) AS n").Where("room_id = ? AND thread_id <> ''", strings.TrimSpace(roomID)).Group("thread_id").Scan(&rows).Error; err != nil {
		return out
	}
	for _, item := range rows {
		out[item.ThreadID] = item.N
	}
	return out
}

// DispatchMessages returns every dispatch card of roomID, oldest first.
func (r *RoomsRepo) DispatchMessages(roomID string) []domain.ChatMessage {
	var rows []chatMessageRow
	if err := r.DB.Table("chat_messages").Where("room_id = ? AND kind = ?", strings.TrimSpace(roomID), domain.ChatKindDispatch).Order("created_at").Order("id").Find(&rows).Error; err != nil {
		return []domain.ChatMessage{}
	}
	out := make([]domain.ChatMessage, 0, len(rows))
	for _, row := range rows {
		var item domain.ChatMessage
		if json.Unmarshal(row.Payload, &item) != nil {
			continue
		}
		if item.ID == "" {
			item.ID = row.ID
		}
		out = append(out, item)
	}
	return out
}

// RoomSignal summarises what needs attention in a room.
type RoomSignal struct {
	PendingApprovals int `json:"pendingApprovals"`
	LiveRuns         int `json:"liveRuns"`
	Lanes            int `json:"lanes"`
}

// RoomSignals counts unresolved approvals and unfinished dispatched runs per
// room.
func (r *RoomsRepo) RoomSignals(roomIDs []string) map[string]RoomSignal {
	out := map[string]RoomSignal{}
	if len(roomIDs) == 0 {
		return out
	}
	var rows []chatMessageRow
	if err := r.DB.Table("chat_messages").Where("room_id IN ? AND kind IN ?", roomIDs, []string{domain.ChatKindApproval, domain.ChatKindDispatch}).Find(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		var item domain.ChatMessage
		if json.Unmarshal(row.Payload, &item) != nil {
			continue
		}
		sig := out[row.RoomID]
		if item.Kind == domain.ChatKindDispatch {
			sig.Lanes++
		}
		switch {
		case item.Kind == domain.ChatKindApproval && !item.Resolved:
			sig.PendingApprovals++
		case item.Kind == domain.ChatKindDispatch && item.RunID != "" && item.RunFinal == "":
			sig.LiveRuns++
		}
		out[row.RoomID] = sig
	}
	return out
}

// Message returns one message of roomID.
func (r *RoomsRepo) Message(roomID, id string) (domain.ChatMessage, bool) {
	var row chatMessageRow
	if err := r.DB.Table("chat_messages").Where("id = ? AND room_id = ?", strings.TrimSpace(id), strings.TrimSpace(roomID)).Take(&row).Error; err != nil {
		return domain.ChatMessage{}, false
	}
	var item domain.ChatMessage
	if json.Unmarshal(row.Payload, &item) != nil {
		return domain.ChatMessage{}, false
	}
	if item.ID == "" {
		item.ID = row.ID
	}
	if item.RoomID == "" {
		item.RoomID = row.RoomID
	}
	return item, true
}

func (r *RoomsRepo) PutMessage(v domain.ChatMessage) error {
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	return upsert(r.DB, "chat_messages", map[string]any{
		"id": v.ID, "room_id": v.RoomID, "user_id": v.UserID, "kind": v.Kind, "issue_id": v.IssueID,
		"thread_id": v.ThreadID, "payload": mustJSON(v), "created_at": timePtr(v.CreatedAt),
	})
}
