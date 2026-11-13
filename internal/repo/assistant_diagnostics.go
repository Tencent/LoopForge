package repo

import (
	"encoding/json"
	"errors"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Separate from the current-generation slot: a retry must not replace the
// previous turn's evidence. Access always includes both owner and session.
type assistantDiagnosticsRow struct {
	GenerationID string `gorm:"primaryKey;size:64"`
	SessionID    string `gorm:"size:64;index:idx_assistant_diagnostics_session"`
	UserID       int64  `gorm:"index:idx_assistant_diagnostics_session"`
	Payload      []byte `gorm:"type:json"`
	UpdatedAt    time.Time
}

func (assistantDiagnosticsRow) TableName() string { return "assistant_diagnostics" }

func initialAssistantDiagnostics(g domain.AssistantGeneration) domain.AssistantDiagnostics {
	return domain.AssistantDiagnostics{Version: 2, GenerationID: g.ID, SessionID: g.SessionID, Status: g.Status, StartedAt: g.StartedAt, UpdatedAt: time.Now().UTC(), Failure: g.Failure, Attempts: []domain.AssistantCLIAttempt{}}
}

func checkpointAssistantDiagnostics(tx *gorm.DB, g domain.AssistantGeneration, reports []domain.AssistantDiagnostics) error {
	now := time.Now().UTC()
	if len(reports) > 0 {
		// The caller owns the full report, so nothing needs to be read back:
		// one upsert replaces the previous checkpoint.
		report := reports[0]
		report.LastCheckpointAt = &now
		report.GenerationID, report.SessionID = g.ID, g.SessionID
		report.Status, report.Failure, report.UpdatedAt = g.Status, g.Failure, now
		domain.AnalyzeAssistantDiagnostics(&report)
		row := assistantDiagnosticsRow{GenerationID: g.ID, SessionID: g.SessionID, UserID: g.UserID, Payload: mustJSON(report), UpdatedAt: now}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "generation_id"}}, DoUpdates: clause.AssignmentColumns([]string{"payload", "updated_at"})}).Create(&row).Error
	}
	var row assistantDiagnosticsRow
	err := tx.Where("generation_id = ? AND session_id = ? AND user_id = ?", g.ID, g.SessionID, g.UserID).Take(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	report := initialAssistantDiagnostics(g)
	if err == nil {
		_ = json.Unmarshal(row.Payload, &report)
		if report.LastCheckpointAt == nil && !row.UpdatedAt.IsZero() {
			prev := row.UpdatedAt.UTC()
			report.LastCheckpointAt = &prev
		}
	}
	if g.Status == "interrupted" && report.Termination == nil {
		report.Termination = &domain.AssistantDiagnosticTermination{At: now, Code: "CHECKPOINT_LOST", Source: "platform", Message: "超过 45 秒未收到运行实例心跳，生成已中断"}
		seq := len(report.Timeline) + 1
		if len(report.Timeline) > 0 && report.Timeline[len(report.Timeline)-1].Sequence >= seq {
			seq = report.Timeline[len(report.Timeline)-1].Sequence + 1
		}
		report.Timeline = append(report.Timeline, domain.AssistantDiagnosticEvent{At: now, Sequence: seq, Source: "platform", Kind: "generation.termination", Code: "CHECKPOINT_LOST", Message: report.Termination.Message})
	}
	report.GenerationID, report.SessionID = g.ID, g.SessionID
	report.Status, report.Failure, report.UpdatedAt = g.Status, g.Failure, now
	domain.AnalyzeAssistantDiagnostics(&report)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&assistantDiagnosticsRow{GenerationID: g.ID, SessionID: g.SessionID, UserID: g.UserID, Payload: mustJSON(report), UpdatedAt: now}).Error
	}
	return tx.Model(&row).Updates(map[string]any{"payload": mustJSON(report), "updated_at": now}).Error
}

func (r *AssistantRepo) AssistantDiagnostics(userID int64, sessionID, generationID string) (domain.AssistantDiagnostics, bool) {
	var row assistantDiagnosticsRow
	if err := r.DB.Where("user_id = ? AND session_id = ? AND generation_id = ?", userID, sessionID, generationID).Take(&row).Error; err != nil {
		return domain.AssistantDiagnostics{}, false
	}
	var report domain.AssistantDiagnostics
	if json.Unmarshal(row.Payload, &report) != nil {
		return domain.AssistantDiagnostics{}, false
	}
	if report.Status == "running" && !row.UpdatedAt.IsZero() && time.Since(row.UpdatedAt) > 45*time.Second {
		now := time.Now().UTC()
		report.Status = "interrupted"
		if report.LastCheckpointAt == nil {
			prev := row.UpdatedAt.UTC()
			report.LastCheckpointAt = &prev
		}
		if report.Termination == nil {
			report.Termination = &domain.AssistantDiagnosticTermination{At: now, Code: "CHECKPOINT_LOST", Source: "platform", Message: "超过 45 秒未收到运行实例心跳，生成已中断"}
		}
		if report.Failure == nil {
			report.Failure = &domain.AssistantFailure{Code: "EXECUTION_INTERRUPTED", Phase: "server", Message: "服务端生成已中断，请检查已执行操作后重试", Suggestion: "检查已执行操作后再重试；可下载诊断信息交给管理员排查。"}
		}
	}
	domain.AnalyzeAssistantDiagnostics(&report)
	return report, true
}
