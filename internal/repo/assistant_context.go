package repo

import (
	"agentflow/internal/domain"
	"errors"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// summaryProjectionUnsupported reports a server that cannot parse the
// projection at all (no JSON_TABLE), as opposed to a transient failure.
func summaryProjectionUnsupported(err error) bool {
	var sqlErr *mysql.MySQLError
	if errors.As(err, &sqlErr) {
		return sqlErr.Number == 1064 || sqlErr.Number == 1305
	}
	return false
}

// Human participants come from the transcript, never project membership or a
// caller-supplied count. Legacy private messages belong to their storage owner;
// a legacy shared message without attribution remains explicitly unknown.
func deriveAssistantParticipants(s *domain.AssistantSession, messages []domain.AssistantMessage) {
	s.Participants = []domain.AssistantParticipant{}
	s.ParticipantsVersion = 1
	s.UnknownParticipants = false
	seen := map[int64]bool{}
	for _, m := range messages {
		if m.Role != "user" || m.Trigger != "" || strings.HasPrefix(m.Content, "已确认并执行 ") && strings.Contains(m.Content, "请核对这条草稿的保存回执，继续完成原始请求") {
			continue
		}
		id := m.AuthorID
		if id == 0 && !s.Shared {
			id = s.UserID
		}
		if id <= 0 {
			s.UnknownParticipants = true
			continue
		}
		if !seen[id] {
			s.Participants = append(s.Participants, domain.AssistantParticipant{ID: id, Name: m.AuthorName})
			seen[id] = true
		}
	}
}

// Old rows have no cached summary. Project only author metadata in SQL so a
// history list never transfers message bodies, attachments, or tool output.
func (r *AssistantRepo) assistantParticipantSummarySQL() string {
	if r.DB.Dialector.Name() == "sqlite" {
		return `CASE WHEN json_extract(payload, '$.participantsVersion') = 1 THEN '[]' ELSE
   (SELECT json_group_array(json_object('role','user','authorId',json_extract(m.value,'$.authorId'),'authorName',json_extract(m.value,'$.authorName')))
    FROM json_each(payload,'$.messages') m WHERE json_extract(m.value,'$.role')='user'
    AND COALESCE(json_extract(m.value,'$.trigger'),'')=''
    AND COALESCE(json_extract(m.value,'$.content'),'') NOT LIKE '已确认并执行 %请核对这条草稿的保存回执，继续完成原始请求%') END`
	}
	return `CASE WHEN JSON_EXTRACT(payload, '$.participantsVersion') = 1 THEN '[]' ELSE
  (SELECT COALESCE(JSON_ARRAYAGG(JSON_OBJECT('role','user','authorId',m.author_id,'authorName',m.author_name)),JSON_ARRAY())
   FROM JSON_TABLE(payload,'$.messages[*]' COLUMNS (role VARCHAR(32) PATH '$.role', author_id BIGINT PATH '$.authorId', author_name VARCHAR(255) PATH '$.authorName', message_trigger VARCHAR(64) PATH '$.trigger', content LONGTEXT PATH '$.content')) m
   WHERE m.role='user' AND COALESCE(m.message_trigger,'')='' AND COALESCE(m.content,'') NOT LIKE '已确认并执行 %请核对这条草稿的保存回执，继续完成原始请求%') END`
}

// Keep binding changes under the same row lock used by generation startup. A
// normal transcript save cannot replace this context with an older snapshot.
func (r *AssistantRepo) SetAssistantSessionIssue(userID int64, id, issueID string, expected time.Time) (domain.AssistantSession, error) {
	var saved domain.AssistantSession
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", assistantRecordID(id), userID).Take(&row).Error; err != nil {
			return fmt.Errorf("会话不存在")
		}
		s := decodeAssistantSession(row)
		if s.Shared {
			return fmt.Errorf("共享运行会话的需求由运行决定")
		}
		if !s.UpdatedAt.Equal(expected) {
			return fmt.Errorf("会话已更新，请重新打开后关联需求")
		}
		var active int64
		if err := tx.Model(&assistantGenerationRow{}).Where("session_id = ? AND status = ?", id, "running").Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("请等待回复结束或停止生成，再关联需求")
		}
		s.IssueID = strings.TrimSpace(issueID)
		s.HistoryRevision++
		s.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
		if row.Updated != nil && !s.UpdatedAt.After(row.Updated.UTC()) {
			s.UpdatedAt = row.Updated.Add(time.Millisecond)
		}
		deriveAssistantParticipants(&s, s.Messages)
		if err := tx.Model(&assistantSessionRow{}).Where("id = ? AND user_id = ?", s.ID, userID).Updates(map[string]any{"payload": mustJSON(s), "updated_at": s.UpdatedAt}).Error; err != nil {
			return err
		}
		saved = s
		return nil
	})
	return saved, err
}

// LinkAssistantSessionIssue links an ordinary conversation without a linked
// issue to issueID. It leaves updated_at alone so a
// generation still writing the session does not conflict, and reports whether
// it linked.
func (r *AssistantRepo) LinkAssistantSessionIssue(userID int64, id, issueID string) (bool, error) {
	linked := false
	err := r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantSessionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", assistantRecordID(id), userID).Take(&row).Error; err != nil {
			return fmt.Errorf("会话不存在")
		}
		s := decodeAssistantSession(row)
		if s.Shared || s.RunID != "" || s.IssueID != "" {
			return nil
		}
		s.IssueID = strings.TrimSpace(issueID)
		linked = true
		return tx.Model(&assistantSessionRow{}).Where("id = ? AND user_id = ?", row.ID, userID).Update("payload", mustJSON(s)).Error
	})
	return linked, err
}
