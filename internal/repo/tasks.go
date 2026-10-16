package repo

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"gorm.io/gorm"
)

// ---------- Tasks ----------

func (r *TasksRepo) Tasks() []domain.Task {
	out := payloadList[domain.Task](r.DB, "tasks", "")
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (r *TasksRepo) Task(id string) (domain.Task, bool) {
	return payloadOne[domain.Task](r.DB, "tasks", id)
}

func (r *TasksRepo) PutTask(v domain.Task) error {
	now := time.Now().UTC()
	if old, ok := r.Task(v.ID); ok {
		v.CreatedAt = old.CreatedAt
	} else {
		v.CreatedAt = now
	}
	v.UpdatedAt = now
	return upsert(r.DB, "tasks", map[string]any{
		"id": v.ID, "issue_id": v.IssueID, "run_id": v.RunID, "status": v.Status,
		"payload": mustJSON(v), "created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
	})
}

// CreateTask inserts a task exactly once. Primary dispatches use a stable task
// ID, so the tasks primary key is also the cross-instance idempotency key.
func (r *TasksRepo) CreateTask(v domain.Task) (domain.Task, bool, error) {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.IssueID) == "" {
		return domain.Task{}, false, fmt.Errorf("task id and issue id are required")
	}
	now := time.Now().UTC()
	v.CreatedAt, v.UpdatedAt = now, now
	created, err := insertIgnore(r.DB, "tasks", map[string]any{
		"id": v.ID, "issue_id": v.IssueID, "run_id": v.RunID, "status": v.Status,
		"payload": mustJSON(v), "created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
	})
	if err == nil && created {
		return v, true, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrDuplicatedKey) {
		return domain.Task{}, false, err
	}
	existing, ok := r.Task(v.ID)
	if !ok {
		return domain.Task{}, false, fmt.Errorf("task reservation was not persisted")
	}
	return existing, false, nil
}

// deleteIssueTasks participates in the issue deletion transaction.
func (r *TasksRepo) deleteIssueTasks(tx *gorm.DB, issueID string) error {
	return tx.Where("issue_id = ?", issueID).Delete(&taskRow{}).Error
}
