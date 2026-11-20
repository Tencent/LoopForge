package repo

import (
	"agentflow/internal/domain"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Approval state is stored in the private generation payload, so any API
// instance can answer without exposing raw tool arguments in the generation DTO.
type assistantNativeApprovalState struct {
	domain.AssistantNativeApproval
	Phase       string `json:"phase"`
	InputDigest string `json:"inputDigest"`
}

func nativeApprovalState(item assistantNativeApprovalState) domain.AssistantNativeApproval {
	out := item.AssistantNativeApproval
	out.InputDigest = item.InputDigest
	return out
}

// nativeApprovalGenerationActive measures the heartbeat on the same clock that
// stamped it (see heartbeatNow).
func (r *AssistantRepo) nativeApprovalGenerationActive(db *gorm.DB, row assistantGenerationRow) bool {
	return row.Status == "running" && !row.CancelRequested && !r.heartbeatLate(db, row.UpdatedAt)
}

func (r *AssistantRepo) updateNativeApprovals(owner int64, session, generation string, requireLive bool, update func(*assistantGenerationPayload) error) error {
	return r.DB.Transaction(func(tx *gorm.DB) error {
		var row assistantGenerationRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ? AND user_id = ? AND generation_id = ?", session, owner, generation).Take(&row).Error; err != nil {
			return fmt.Errorf("本轮已结束，审批已失效")
		}
		if requireLive && !r.nativeApprovalGenerationActive(tx, row) {
			return fmt.Errorf("本轮已结束，审批已失效")
		}
		payload := generationPayload(row)
		if err := update(&payload); err != nil {
			return err
		}
		// Approval traffic does not renew a dead worker's lease. Only the
		// generation's existing checkpoint loop updates updated_at.
		return tx.Model(&row).UpdateColumn("payload", mustJSON(payload)).Error
	})
}

func (r *AssistantRepo) CreateNativeApproval(owner int64, session, phase string, item domain.AssistantNativeApproval) error {
	if item.GenerationID == "" || item.ID == "" || phase == "" || item.ActorID <= 0 || item.Tool == "" || !json.Valid(item.Input) {
		return fmt.Errorf("原生工具审批参数无效")
	}
	digest := sha256.Sum256(item.Input)
	inputDigest := hex.EncodeToString(digest[:])
	return r.updateNativeApprovals(owner, session, item.GenerationID, true, func(payload *assistantGenerationPayload) error {
		for _, old := range payload.NativeApprovals {
			if old.ID != item.ID {
				continue
			}
			if old.Phase != phase || old.ActorID != item.ActorID || old.Tool != item.Tool || old.InputDigest != inputDigest {
				return fmt.Errorf("审批调用标识已被其他操作使用")
			}
			return nil // A transport retry is still the same invocation.
		}
		item.Decision = "pending"
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		payload.NativeApprovals = append(payload.NativeApprovals, assistantNativeApprovalState{AssistantNativeApproval: item, Phase: phase, InputDigest: inputDigest})
		return nil
	})
}

func (r *AssistantRepo) NativeApproval(owner int64, session, id string) (domain.AssistantNativeApproval, bool) {
	var row assistantGenerationRow
	if err := r.DB.Where("session_id = ? AND user_id = ?", session, owner).Take(&row).Error; err != nil || !r.nativeApprovalGenerationActive(r.DB, row) {
		return domain.AssistantNativeApproval{}, false
	}
	for _, item := range generationPayload(row).NativeApprovals {
		if item.ID == id {
			return nativeApprovalState(item), true
		}
	}
	return domain.AssistantNativeApproval{}, false
}

func (r *AssistantRepo) PendingNativeApprovals(owner int64, session string) []domain.AssistantNativeApproval {
	out := []domain.AssistantNativeApproval{}
	var row assistantGenerationRow
	if err := r.DB.Where("session_id = ? AND user_id = ?", session, owner).Take(&row).Error; err != nil || !r.nativeApprovalGenerationActive(r.DB, row) {
		return out
	}
	for _, item := range generationPayload(row).NativeApprovals {
		if item.Decision == "pending" {
			out = append(out, nativeApprovalState(item))
		}
	}
	return out
}

func (r *AssistantRepo) DecideNativeApproval(owner, actor int64, session, id, decision string) error {
	if decision != "allow" && decision != "deny" {
		return fmt.Errorf("无效审批结果")
	}
	var row assistantGenerationRow
	if err := r.DB.Where("session_id = ? AND user_id = ?", session, owner).Take(&row).Error; err != nil {
		return fmt.Errorf("本轮已结束，审批已失效")
	}
	return r.updateNativeApprovals(owner, session, row.GenerationID, true, func(payload *assistantGenerationPayload) error {
		for i := range payload.NativeApprovals {
			item := &payload.NativeApprovals[i]
			if item.ID != id || item.ActorID != actor {
				continue
			}
			if item.Decision != "pending" && item.Decision != decision {
				return fmt.Errorf("审批已作出不同决定")
			}
			item.Decision = decision
			// Keep only the exact-input digest for transport retries once the
			// browser no longer needs to display the full tool arguments.
			item.Input = nil
			return nil
		}
		return fmt.Errorf("审批已失效或不属于当前发起人")
	})
}

// ReleaseNativeApproval removes the card for a single finished or cancelled
// gateway call. Other calls waiting in the same phase retain their approvals.
func (r *AssistantRepo) ReleaseNativeApproval(owner int64, session, generation, phase, id string) error {
	return r.updateNativeApprovals(owner, session, generation, false, func(payload *assistantGenerationPayload) error {
		payload.NativeApprovals = slices.DeleteFunc(payload.NativeApprovals, func(item assistantNativeApprovalState) bool {
			return item.Phase == phase && item.ID == id
		})
		return nil
	})
}

// ReleaseNativeApprovals revokes only the exiting CLI phase, including its
// resolved callbacks. Sibling sessions and later generations are untouched.
func (r *AssistantRepo) ReleaseNativeApprovals(owner int64, session, generation, phase string) error {
	return r.updateNativeApprovals(owner, session, generation, false, func(payload *assistantGenerationPayload) error {
		payload.NativeApprovals = slices.DeleteFunc(payload.NativeApprovals, func(item assistantNativeApprovalState) bool { return item.Phase == phase })
		return nil
	})
}
