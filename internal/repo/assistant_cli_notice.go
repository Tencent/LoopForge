package repo

import (
	"errors"
	"strings"

	"agentflow/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReplaceInstalledCLIFailure rewrites the failure a conversation kept while a
// CLI was absent. The install keeps running after the browser leaves, so the
// stored "not installed" or generic generation failure has to change with it.
// missing is the sentence for this engine. generic is replaced only when the
// conversation was using that engine.
func (r *AssistantRepo) ReplaceInstalledCLIFailure(engine, missing, generic, next string) error {
	engine = strings.TrimSpace(engine)
	missing = strings.TrimSpace(missing)
	next = strings.TrimSpace(next)
	if r == nil || r.DB == nil || engine == "" || missing == "" || next == "" {
		return nil
	}
	if err := r.replaceGenerationCLIFailure(engine, missing, generic, next); err != nil {
		return err
	}
	return r.replaceSessionCLIFailure(engine, missing, generic, next)
}

// Both rewrites only use the first query to find candidates. Each row is then
// re-read under a row lock in its own transaction and only the stored failure
// is changed, so a message, rename or sandbox switch that landed while the
// install ran is kept instead of being overwritten by a stale snapshot.
func (r *AssistantRepo) replaceGenerationCLIFailure(engine, missing, generic, next string) error {
	var rows []assistantGenerationRow
	query := wherePayloadHas(r.DB.Select("session_id", "generation_id").Where("status <> ?", "running"), r.DB, missing, generic)
	if err := query.Find(&rows).Error; err != nil {
		return err
	}
	for _, candidate := range rows {
		sameEngine := r.sessionUsesEngine(candidate.SessionID, engine)
		err := r.DB.Transaction(func(tx *gorm.DB) error {
			var row assistantGenerationRow
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ? AND generation_id = ? AND status <> ?", candidate.SessionID, candidate.GenerationID, "running").Take(&row).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			g := decodeGeneration(row)
			if !cliFailureMatches(g.Error, missing, generic, sameEngine) {
				return nil
			}
			g.Error = next
			return tx.Model(&assistantGenerationRow{}).Where("session_id = ? AND generation_id = ?", row.SessionID, row.GenerationID).Update("payload", mustJSON(g)).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *AssistantRepo) replaceSessionCLIFailure(engine, missing, generic, next string) error {
	var rows []assistantSessionRow
	query := wherePayloadHas(r.DB.Table("assistant_sessions").Select("id"), r.DB, missing, generic)
	if err := query.Find(&rows).Error; err != nil {
		return err
	}
	for _, candidate := range rows {
		err := r.DB.Transaction(func(tx *gorm.DB) error {
			var row assistantSessionRow
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", candidate.ID).Take(&row).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			item := decodeAssistantSession(row)
			if !rewriteCLIFailureMessages(&item, missing, generic, next, sessionUsesEngine(item, engine)) {
				return nil
			}
			return tx.Model(&assistantSessionRow{}).Where("id = ? AND user_id = ?", row.ID, row.UserID).Update("payload", mustJSON(item)).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// wherePayloadHas matches JSON payloads that contain the failure text.
// LIKE does not see the sqlite JSON blob, so both drivers search the cast text.
func wherePayloadHas(query *gorm.DB, db *gorm.DB, missing, generic string) *gorm.DB {
	expr := "instr(CAST(payload AS TEXT), ?) > 0"
	if db.Dialector.Name() == "mysql" {
		expr = "INSTR(CAST(payload AS CHAR), ?) > 0"
	}
	if strings.TrimSpace(generic) == "" {
		return query.Where(expr, missing)
	}
	return query.Where(expr+" OR "+expr, missing, generic)
}

func (r *AssistantRepo) sessionUsesEngine(sessionID, engine string) bool {
	var row assistantSessionRow
	if err := r.DB.Table("assistant_sessions").Where("id = ?", sessionID).Take(&row).Error; err != nil {
		return false
	}
	return sessionUsesEngine(decodeAssistantSession(row), engine)
}

func sessionUsesEngine(item domain.AssistantSession, engine string) bool {
	if strings.TrimSpace(item.Executor) == engine {
		return true
	}
	model := strings.TrimSpace(item.Model)
	return model == engine || strings.HasPrefix(model, engine+":")
}

func cliFailureMatches(text, missing, generic string, sameEngine bool) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if text == missing {
		return true
	}
	return sameEngine && generic != "" && text == generic
}

func rewriteCLIFailureMessages(item *domain.AssistantSession, missing, generic, next string, sameEngine bool) bool {
	changed := false
	for i := range item.Messages {
		if !cliFailureMatches(item.Messages[i].Unfinished, missing, generic, sameEngine) {
			continue
		}
		item.Messages[i].Unfinished = next
		changed = true
	}
	return changed
}
