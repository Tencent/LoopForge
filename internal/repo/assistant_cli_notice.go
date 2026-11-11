package repo

import (
	"strings"

	"agentflow/internal/domain"

	"gorm.io/gorm"
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

func (r *AssistantRepo) replaceGenerationCLIFailure(engine, missing, generic, next string) error {
	var rows []assistantGenerationRow
	query := wherePayloadHas(r.DB.Where("status <> ?", "running"), r.DB, missing, generic)
	if err := query.Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		g := decodeGeneration(row)
		if !cliFailureMatches(g.Error, missing, generic, r.sessionUsesEngine(row.SessionID, engine)) {
			continue
		}
		g.Error = next
		if err := r.DB.Model(&assistantGenerationRow{}).Where("session_id = ? AND generation_id = ?", row.SessionID, row.GenerationID).Update("payload", mustJSON(g)).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *AssistantRepo) replaceSessionCLIFailure(engine, missing, generic, next string) error {
	var rows []assistantSessionRow
	query := wherePayloadHas(r.DB.Table("assistant_sessions"), r.DB, missing, generic)
	if err := query.Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		item := decodeAssistantSession(row)
		if !rewriteCLIFailureMessages(&item, missing, generic, next, sessionUsesEngine(item, engine)) {
			continue
		}
		if err := r.DB.Model(&assistantSessionRow{}).Where("id = ? AND user_id = ?", row.ID, row.UserID).Update("payload", mustJSON(item)).Error; err != nil {
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
