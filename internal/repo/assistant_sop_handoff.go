package repo

import (
	"agentflow/internal/domain"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"gorm.io/gorm/clause"
)

// SOPHandoffSource fences the chat read together with the existing run revision
// CAS. A message arriving while the model summarizes must not be silently lost.
type SOPHandoffSource struct{ SessionID, SHA256 string }

func SOPHandoffSessionSource(s domain.AssistantSession) SOPHandoffSource {
	raw, _ := json.Marshal(s.Messages)
	return SOPHandoffSource{SessionID: s.ID, SHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}
}
func (r *AssistantRepo) checkSOPHandoffSource(source SOPHandoffSource) error {
	var row assistantSessionRow
	if err := r.DB.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", source.SessionID).Take(&row).Error; err != nil {
		return err
	}
	if SOPHandoffSessionSource(decodeAssistantSession(row)) != source {
		return fmt.Errorf("群聊有新消息，请重新生成交接摘要后切回 SOP")
	}
	var count int64
	if err := r.DB.Model(&assistantGenerationRow{}).Where("session_id = ? AND status = ?", source.SessionID, "running").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("Main Bot 仍在回复，请等待结束后再切回 SOP")
	}
	return nil
}
