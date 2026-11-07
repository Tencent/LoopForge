package repo

import (
	"log"
	"strings"

	"agentflow/internal/domain"
)

// scheduledAssistantTitlePrefix is how scheduled conversations were named
// before the source column existed.
const scheduledAssistantTitlePrefix = "定时 · "

// ListAssistantSessionsBySource returns conversation metadata for one source
// (chat / sop / scheduled). Transcripts stay in the database.
func (r *AssistantRepo) ListAssistantSessionsBySource(source string) []domain.AssistantSession {
	source = strings.TrimSpace(source)
	if source == "" {
		return []domain.AssistantSession{}
	}
	return r.assistantSessionsBy("source = ?", source)
}

// ListAssistantSessionsByTask returns the conversations a scheduled task or
// assistant task produced, so a new run can keep only its own.
func (r *AssistantRepo) ListAssistantSessionsByTask(source, taskID string) []domain.AssistantSession {
	source = strings.TrimSpace(source)
	taskID = strings.TrimSpace(taskID)
	if source == "" || taskID == "" {
		return []domain.AssistantSession{}
	}
	return r.assistantSessionsBy("source = ? AND task_id = ?", source, taskID)
}

func (r *AssistantRepo) assistantSessionsBy(where string, args ...any) []domain.AssistantSession {
	rows, err := r.findAssistantSessionSummaries(where, args...)
	if err != nil {
		log.Printf("assistant session query failed (%s): %v", where, err)
		return []domain.AssistantSession{}
	}
	out := make([]domain.AssistantSession, 0, len(rows))
	for _, row := range rows {
		item := decodeAssistantSession(row)
		item.Messages = nil
		out = append(out, item)
	}
	return out
}

// inferredAssistantSessionSource recovers the origin of a conversation whose
// source column predates the marker, for the single row a client is about to
// rewrite: continuing an old conversation must not reset it to chat.
//
// Bulk backfill is deliberately not done from code. Rows written before the
// column existed are marked by migrations/021_assistant_session_sources.sql,
// which the operator applies manually, in line with the repository rule that
// production never migrates data on boot.
func inferredAssistantSessionSource(item domain.AssistantSession) string {
	if source := strings.TrimSpace(item.Source); source != "" {
		return source
	}
	if item.Shared && strings.TrimSpace(item.RunID) != "" {
		return domain.AssistantSourceSOP
	}
	if !item.TitleEdited && strings.HasPrefix(strings.TrimSpace(item.Title), scheduledAssistantTitlePrefix) {
		return domain.AssistantSourceScheduled
	}
	return domain.AssistantSourceChat
}
