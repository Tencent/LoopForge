package repo

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// ---------- Issues ----------

// IssueAnalyticsViews loads the columns analytics needs without hydrating the
// full issue payload (clarification, comments, and delivery lock blobs).
func (r *IssuesRepo) IssueAnalyticsViews() []domain.Issue {
	urlExpr := "json_extract(payload, '$.externalRef.externalUrl')"
	tapdIDExpr := "json_extract(payload, '$.tapdId')"
	clarificationStartExpr := "json_extract(payload, '$.clarification.startedAt')"
	deliveryExpr := "json_extract(payload, '$.deliveryState')"
	if r.isMySQL() {
		urlExpr = "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.externalRef.externalUrl'))"
		tapdIDExpr = "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.tapdId'))"
		clarificationStartExpr = "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.clarification.startedAt'))"
		deliveryExpr = "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.deliveryState'))"
	}
	type liteRow struct {
		ID                     string `gorm:"column:id"`
		ProjectID              string `gorm:"column:project_id"`
		Title                  string `gorm:"column:title"`
		Status                 string `gorm:"column:status"`
		Kind                   string `gorm:"column:kind"`
		Source                 string `gorm:"column:source"`
		WorkflowID             string `gorm:"column:workflow_id"`
		AssigneeUserID         *int64 `gorm:"column:assignee_user_id"`
		ExternalID             string `gorm:"column:external_id"`
		ExternalURL            string `gorm:"column:external_url"`
		TapdID                 string `gorm:"column:tapd_id"`
		ClarificationStartedAt string `gorm:"column:clarification_started_at"`
		DeliveryState          string `gorm:"column:delivery_state"`
	}
	var rows []liteRow
	if err := r.DB.Table("issues").Select(
		"id", "project_id", "title", "status", "kind", "source", "workflow_id", "assignee_user_id", "external_id",
		urlExpr+" AS external_url",
		tapdIDExpr+" AS tapd_id",
		clarificationStartExpr+" AS clarification_started_at",
		deliveryExpr+" AS delivery_state",
	).Find(&rows).Error; err != nil {
		return []domain.Issue{}
	}
	out := make([]domain.Issue, 0, len(rows))
	for _, row := range rows {
		issue := domain.Issue{
			ID:            row.ID,
			ProjectID:     row.ProjectID,
			Title:         row.Title,
			Status:        row.Status,
			Kind:          row.Kind,
			Source:        row.Source,
			TapdID:        strings.TrimSpace(row.TapdID),
			WorkflowID:    row.WorkflowID,
			DeliveryState: strings.TrimSpace(row.DeliveryState),
		}
		if started, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(row.ClarificationStartedAt)); err == nil {
			issue.Clarification.StartedAt = started
		}
		if row.AssigneeUserID != nil {
			issue.AssigneeUserID = *row.AssigneeUserID
		}
		extID := strings.TrimSpace(row.ExternalID)
		extURL := strings.TrimSpace(row.ExternalURL)
		if extID != "" || extURL != "" {
			issue.ExternalRef = &domain.ExternalRef{Source: issue.Source, ExternalID: extID, ExternalURL: extURL}
		}
		out = append(out, issue)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r *IssuesRepo) Issues() []domain.Issue {
	var rows []issueRow
	if err := r.DB.Find(&rows).Error; err != nil {
		return []domain.Issue{}
	}
	out := make([]domain.Issue, 0, len(rows))
	for _, row := range rows {
		var issue domain.Issue
		if jsonUnmarshal(row.Payload, &issue) != nil {
			continue
		}
		if row.AssigneeUserID != nil {
			issue.AssigneeUserID = *row.AssigneeUserID
		} else {
			issue.AssigneeUserID = 0
		}
		out = append(out, issue)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectID == out[j].ProjectID {
			return out[i].Number > out[j].Number
		}
		return out[i].ProjectID < out[j].ProjectID
	})
	if len(out) == 0 {
		// 与 master 保持同一 wire 行为：空结果序列化为 null 而非 []。
		return nil
	}
	return out
}

func (r *IssuesRepo) Issue(id string) (domain.Issue, bool) {
	var row issueRow
	if err := r.DB.Where("id = ?", id).Take(&row).Error; err != nil {
		return domain.Issue{}, false
	}
	var issue domain.Issue
	if jsonUnmarshal(row.Payload, &issue) != nil {
		return domain.Issue{}, false
	}
	if row.AssigneeUserID != nil {
		issue.AssigneeUserID = *row.AssigneeUserID
	} else {
		issue.AssigneeUserID = 0
	}
	return issue, true
}

// preserveIssueDeliveryFields keeps delivery lock metadata when Gongfeng/TAPD sync
// writes an issue snapshot that omitted those fields (race with MR reconcile).
func preserveIssueDeliveryFields(v *domain.Issue, old domain.Issue) {
	if strings.TrimSpace(old.DeliveryState) == "" {
		return
	}
	if strings.TrimSpace(v.DeliveryState) == "" {
		v.DeliveryState = old.DeliveryState
	}
	if strings.TrimSpace(v.DeliveryMRURL) == "" {
		v.DeliveryMRURL = old.DeliveryMRURL
	}
	if strings.TrimSpace(v.DeliveryMRState) == "" {
		v.DeliveryMRState = old.DeliveryMRState
	}
	if len(v.DeliveryMRs) == 0 && domain.SameGongfengMR(v.DeliveryMRURL, old.DeliveryMRURL) {
		v.DeliveryMRs = old.DeliveryMRs
	}
	if v.DeliveryUpdatedAt.IsZero() {
		v.DeliveryUpdatedAt = old.DeliveryUpdatedAt
	}
}

func (r *IssuesRepo) PutIssue(v domain.Issue) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.putIssueLocked(v)
}

// PatchIssue loads the current row, applies fn, and writes it back under the
// same lock as PutIssue so a clarification job cannot clobber a concurrent save.
func (r *IssuesRepo) PatchIssue(id string, fn func(*domain.Issue) error) (domain.Issue, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.Issue{}, errors.New("issue id is required")
	}
	if fn == nil {
		return domain.Issue{}, errors.New("patch function is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	issue, ok := r.Issue(id)
	if !ok {
		return domain.Issue{}, errors.New("issue not found")
	}
	if err := fn(&issue); err != nil {
		return issue, err
	}
	if err := r.putIssueLocked(issue); err != nil {
		return issue, err
	}
	stored, ok := r.Issue(id)
	if ok {
		return stored, nil
	}
	return issue, nil
}

func (r *IssuesRepo) putIssueLocked(v domain.Issue) error {
	now := time.Now().UTC()
	old, ok := r.Issue(v.ID)
	if ok {
		v.CreatedAt = old.CreatedAt
		if v.Number == 0 {
			v.Number = old.Number
		}
		if strings.TrimSpace(v.AutomationState) == "" {
			v.AutomationState = old.AutomationState
		}
		if len(v.Operations) == 0 && len(old.Operations) > 0 {
			v.Operations = old.Operations
		}
		if !old.Clarification.StartedAt.IsZero() && (v.Clarification.StartedAt.IsZero() || v.Clarification.StartedAt.After(old.Clarification.StartedAt)) {
			v.Clarification.StartedAt = old.Clarification.StartedAt
		}
		preserveIssueDeliveryFields(&v, old)
	} else {
		v.CreatedAt = now
		if v.Number == 0 {
			v.Number = 1
			var maxNumber sql.NullInt64
			if err := r.DB.Raw(`SELECT MAX(number) FROM issues WHERE project_id=?`, v.ProjectID).Scan(&maxNumber).Error; err != nil {
				return err
			}
			if maxNumber.Valid {
				v.Number = int(maxNumber.Int64) + 1
			}
		}
	}
	if v.TrackerClosed() && v.Status != "closed" {
		v.Status = "closed"
		v.CloseSource = "external"
	}
	if v.Status == "closed" {
		if v.CloseSource == "" {
			if ok && old.Status == "closed" {
				v.CloseSource = old.CloseSource
				if v.CloseSource == "" {
					v.CloseSource = "manual" // pre-CloseSource manual close
				}
			} else if v.TrackerClosed() {
				v.CloseSource = "external"
			} else {
				v.CloseSource = "manual"
			}
		}
	} else {
		v.CloseSource = ""
	}
	if v.Status == "done" || v.Status == "closed" || v.Status == "cancelled" || v.GongfengClosed() {
		v.AutomationState = "ignored"
	}
	if v.AssigneeUserID == 0 {
		if project, found := r.projects.Project(v.ProjectID); found {
			v.AssigneeUserID = project.OwnerID
		}
	}
	bindDefaultWorkflow(&v, r.projectWorkflowID(v.ProjectID), r.projectIssueWorkflowID(v.ProjectID))
	v.UpdatedAt = now
	return r.writeIssue(v)
}

func (r *IssuesRepo) writeIssue(v domain.Issue) error {
	ext := ""
	if v.ExternalRef != nil {
		ext = v.ExternalRef.ExternalID
	}
	var assignee any
	if v.AssigneeUserID > 0 {
		assignee = v.AssigneeUserID
	}
	return upsert(r.DB, "issues", map[string]any{
		"id": v.ID, "project_id": v.ProjectID, "number": v.Number, "title": v.Title,
		"status": v.Status, "kind": v.Kind, "source": v.Source, "workflow_id": v.WorkflowID,
		"assignee_user_id": assignee, "external_id": ext, "payload": mustJSON(v),
		"created_at": timePtr(v.CreatedAt), "updated_at": timePtr(v.UpdatedAt),
	})
}


// ResponsibleUser resolves the explicit issue responsibility first and falls
// back to the project's separately configured owner.
func (r *IssuesRepo) ResponsibleUser(issue domain.Issue) (domain.User, error) {
	userID := issue.AssigneeUserID
	if userID == 0 {
		project, ok := r.projects.Project(issue.ProjectID)
		if !ok || project.OwnerID == 0 {
			return domain.User{}, errors.New("项目未配置 Owner，需求也未配置责任人")
		}
		userID = project.OwnerID
	}
	user, ok := r.users.User(userID)
	if !ok {
		return domain.User{}, errors.New("需求责任人或项目 Owner 未关联有效用户")
	}
	return user, nil
}

// projectWorkflowID returns the project's configured default SOP.
func (r *IssuesRepo) projectWorkflowID(projectID string) string {
	if strings.TrimSpace(projectID) == "" {
		return ""
	}
	if p, ok := payloadOne[domain.Project](r.DB, "projects", projectID); ok {
		return strings.TrimSpace(p.WorkflowID)
	}
	return ""
}

func (r *IssuesRepo) projectIssueWorkflowID(projectID string) string {
	id := ProjectIssueWorkflowID(projectID)
	if id == "" {
		return ""
	}
	if _, ok := payloadOne[domain.WorkflowDefinition](r.DB, "workflows", id); ok {
		return id
	}
	return ""
}

func (r *IssuesRepo) IssueHasTask(issueID string) bool {
	return r.IssueHasTaskForWorkflow(issueID, "")
}

func (r *IssuesRepo) IssueHasTaskForWorkflow(issueID, workflowID string) bool {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return false
	}
	workflowID = strings.TrimSpace(workflowID)
	for _, task := range r.tasks.Tasks() {
		if task.IssueID != issueID {
			continue
		}
		taskWF := strings.TrimSpace(task.WorkflowID)
		// 旧 Task 可能没有 workflowId，按「这条需求已经跑过」处理，避免定时任务重复派发。
		if workflowID == "" || taskWF == "" || taskWF == workflowID {
			return true
		}
	}
	if workflowID != "" {
		return false
	}
	if issue, ok := r.Issue(issueID); ok && strings.TrimSpace(issue.LatestTaskID) != "" {
		return true
	}
	return false
}

// ---------- Issue comments ----------

func (r *IssuesRepo) Comments(issueID string) []domain.IssueComment {
	out := payloadList[domain.IssueComment](r.DB, "issue_comments", "issue_id = ?", issueID)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (r *IssuesRepo) PutComment(v domain.IssueComment) error {
	v.CreatedAt = time.Now().UTC()
	return upsert(r.DB, "issue_comments", map[string]any{
		"id": v.ID, "issue_id": v.IssueID, "payload": mustJSON(v), "created_at": timePtr(v.CreatedAt),
	})
}
