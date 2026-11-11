package knowledge

import (
	"strings"

	"agentflow/internal/domain"
)

// ActivityLimit bounds both the read window of the 知识中心 panel and the
// number of records the repository retains per project.
const ActivityLimit = domain.KnowledgeActivityLimit

// Caller identifies who invoked a read-only knowledge tool. The SOP proxy
// carries AgentID/RunID in the signed token; Main Bot fills RunID and the
// chat SessionID from its AgentToolSession.
type Caller struct {
	ProjectID string
	AgentID   string
	UserID    int64
	RunID     string
	SessionID string
}

// ActivityStore is the persistence behind the 知识中心 "Agent 最近的检索"
// panel. Like ManagedStore it is discovered from Service.store with a type
// assertion so the narrow Store interface used by existing tests stays small.
type ActivityStore interface {
	PutKnowledgeActivity(domain.KnowledgeActivity) error
	KnowledgeActivities(projectID string, limit int) []domain.KnowledgeActivity
}

// RecordActivity persists one tool call. It is deliberately best effort: the
// caller has already produced the tool result, so an activity write error must
// only be logged, never returned as the tool result. The error is exposed for
// that logging.
func (s *Service) RecordActivity(item domain.KnowledgeActivity) error {
	if s == nil || s.store == nil {
		return nil
	}
	item.ProjectID = strings.TrimSpace(item.ProjectID)
	if item.ProjectID == "" || strings.TrimSpace(item.Tool) == "" {
		return nil
	}
	if item.At.IsZero() {
		item.At = s.now()
	}
	store, ok := s.store.(ActivityStore)
	if !ok {
		return nil
	}
	return store.PutKnowledgeActivity(item)
}

// Activities returns the newest tool calls of one project. An empty project
// reads nothing so one project can never inspect another project's records.
func (s *Service) Activities(projectID string, limit int) []domain.KnowledgeActivity {
	out := make([]domain.KnowledgeActivity, 0)
	if s == nil || s.store == nil {
		return out
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return out
	}
	if limit <= 0 || limit > ActivityLimit {
		limit = ActivityLimit
	}
	store, ok := s.store.(ActivityStore)
	if !ok {
		return out
	}
	return store.KnowledgeActivities(projectID, limit)
}
