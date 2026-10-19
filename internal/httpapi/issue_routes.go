package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func (h issuesHandlers) issueDetail(c *gin.Context, issue domain.Issue) {
	views, err := h.issueDeletionViews([]domain.Issue{issue})
	if err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, map[string]any{"issue": views[0], "comments": h.repo.Comments(issue.ID)})
}

func (h issuesHandlers) addIssueComment(c *gin.Context, issue domain.Issue) {
	var v domain.IssueComment
	if decode(c, &v) && v.Body != "" {
		v.ID = fmt.Sprintf("comment-%d", time.Now().UnixNano())
		v.IssueID = issue.ID
		if v.AuthorType == "" {
			v.AuthorType = "member"
		}
		respond(c, h.repo.PutComment(v))
	}
}

func (h issuesHandlers) issueRuns(c *gin.Context, issue domain.Issue) {
	// 返回该 issue 的所有历史 runs（按时间倒序）
	allRuns, err := h.repo.RunSummariesFor(domain.AnalyticsFilter{}, issue.ID)
	if err != nil {
		respond(c, err)
		return
	}
	var issueRuns []domain.Run
	for _, r := range allRuns {
		if r.ExecutionContext.IssueID == issue.ID {
			issueRuns = append(issueRuns, r)
		}
	}
	c.JSON(200, runSummaries(issueRuns))
}

func (h issuesHandlers) assessIssueClarification(c *gin.Context, issue domain.Issue) {
	var request struct {
		Answers map[string]string `json:"answers"`
	}
	if !decode(c, &request) {
		return
	}
	project, found := h.repo.Project(issue.ProjectID)
	if !found {
		c.JSON(400, map[string]string{"error": "project not found"})
		return
	}
	assessment := assessClarification(project.Clarification, request.Answers)
	if !issue.Clarification.StartedAt.IsZero() {
		assessment.StartedAt = issue.Clarification.StartedAt
	} else {
		assessment.StartedAt = time.Now().UTC()
	}
	assessment.Gated = true
	issue.Kind = "requirement"
	issue.Clarification = assessment
	if err := h.repo.PutIssue(issue); err != nil {
		respond(c, err)
		return
	}
	c.JSON(200, assessment)
}

// clarificationSubmission is the clarify panel's save payload: a draft
// (Ready false), a final summary (Ready true), or a write-back retry.
type clarificationSubmission struct {
	Summary    string                         `json:"summary"`
	Ready      bool                           `json:"ready"`
	History    []domain.ClarificationMessage  `json:"history"`
	Questions  []domain.ClarificationQuestion `json:"questions"`
	Dimensions map[string]int                 `json:"dimensions"`
	Missing    []string                       `json:"missing"`
	Retry      bool                           `json:"retry"`
}

func (h issuesHandlers) submitIssueClarification(c *gin.Context, issue domain.Issue) {
	var request clarificationSubmission
	if !decode(c, &request) {
		return
	}
	project, found := h.repo.Project(issue.ProjectID)
	issue, err := applyClarificationSubmission(issue, project, found, request, time.Now().UTC())
	if err != nil {
		respond(c, err)
		return
	}
	if err := h.repo.PutIssue(issue); err != nil {
		respond(c, err)
		return
	}
	assessment := issue.Clarification
	if request.Retry && assessment.WriteBackStatus == "pending" {
		if !issueSyncedFromTracker(issue) {
			assessment.WriteBackStatus = "skipped"
		} else {
			body := clarificationComment(issue, assessment.Summary)
			if err := h.sync.WriteBackClarification(h.repo, issue, body); err != nil {
				assessment.WriteBackStatus = "failed"
				assessment.WriteBackError = err.Error()
			} else {
				assessment.WriteBackStatus = "succeeded"
				assessment.WriteBackError = ""
				assessment.WriteBackAt = time.Now().UTC()
			}
		}
		issue.Clarification = assessment
		if err := h.repo.PutIssue(issue); err != nil {
			respond(c, err)
			return
		}
	} else if request.Ready && assessment.WriteBackStatus == "pending" && !issueSyncedFromTracker(issue) {
		assessment.WriteBackStatus = "skipped"
		issue.Clarification = assessment
		if err := h.repo.PutIssue(issue); err != nil {
			respond(c, err)
			return
		}
	}
	stored, _ := h.repo.Issue(issue.ID)
	c.JSON(200, stored)
}

// applyClarificationSubmission updates the issue's clarification state for a
// submission: drafts only record progress, a final summary is scored against
// the project's policy and appended to the description, and final/retry
// submissions mark tracker write-back pending unless this exact summary was
// already written back.
func applyClarificationSubmission(issue domain.Issue, project domain.Project, projectFound bool, request clarificationSubmission, now time.Time) (domain.Issue, error) {
	summary := strings.TrimSpace(request.Summary)
	if request.Ready && !request.Retry && summary == "" {
		return issue, HTTPError{Status: http.StatusBadRequest, Message: "clarification summary is required"}
	}
	assessment := issue.Clarification
	if !request.Retry {
		assessment.Summary = summary
		assessment.History = request.History
		assessment.Questions = request.Questions
		if request.Ready {
			assessment.Status = domain.ClarificationReady
			dimensions := clarificationRatingDimensions(issue, project, projectFound)
			score, rated := scoreClarificationDimensions(dimensions, request.Dimensions)
			assessment.Score = score
			assessment.Dimensions = rated
			assessment.Missing = trimClarificationMissing(request.Missing)
			if len(assessment.Missing) == 0 {
				for _, dim := range dimensions {
					if rated[dim.ID] < 100 {
						assessment.Missing = append(assessment.Missing, dim.ID)
					}
				}
			}
			if isBugStyleWorkItem(issue) {
				assessment.Passed = true
			} else {
				policy := domain.DefaultClarificationPolicy()
				if projectFound {
					policy = domain.EffectiveClarificationPolicy(project.Clarification)
				}
				assessment.Passed = domain.ClarificationPassed(policy, score)
				assessment.Gated = true
			}
			assessment.AssessedAt = now
			promoteClarifiedIssueTodo(&issue)
		} else {
			assessment.Status = "in_progress"
		}
	}
	if request.Ready || request.Retry {
		if strings.TrimSpace(assessment.Summary) == "" {
			return issue, HTTPError{Status: http.StatusBadRequest, Message: "clarification summary is required"}
		}
		fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(assessment.Summary)))
		if assessment.WriteBackStatus != "succeeded" || assessment.WriteBackFingerprint != fingerprint {
			assessment.WriteBackStatus = "pending"
			assessment.WriteBackError = ""
			assessment.WriteBackFingerprint = fingerprint
		}
	}
	issue.Clarification = assessment
	if request.Ready || request.Retry {
		issue.Description = appendClarificationDescription(issue.Description, assessment.Summary)
	}
	return issue, nil
}

// issueSyncedFromTracker reports whether clarification can be written back
// to an external tracker ticket.
func issueSyncedFromTracker(issue domain.Issue) bool {
	return issue.ExternalRef != nil && (issue.Source == "gongfeng" || issue.Source == "tapd")
}

func (h issuesHandlers) chatClarifyIssue(c *gin.Context, issue domain.Issue) {
	if c.Request.Method == http.MethodGet {
		h.stream.attachIssueClarify(c, issue)
		return
	}
	var request chatClarifyRequest
	if !decode(c, &request) {
		return
	}
	h.stream.runChatClarify(c, issue, request)
}

func (h issuesHandlers) cancelChatClarifyIssue(c *gin.Context, issue domain.Issue) {
	cancelled := h.stream.cancelClarifyJob(issue.ID)
	c.JSON(200, map[string]bool{"cancelled": cancelled})
}

func (h issuesHandlers) reopenIssue(c *gin.Context, issue domain.Issue) {
	reopened, err := h.repo.ReopenDeliveredIssue(issue.ID)
	if err != nil {
		c.JSON(409, map[string]string{"error": err.Error()})
		return
	}
	saved, err := h.appendStoredOperation(c, reopened.ID, "reopen", "重新打开需求")
	if err != nil {
		respond(c, err)
		return
	}
	h.writeIssueView(c, 200, saved)
}

func (h issuesHandlers) closeIssue(c *gin.Context, issue domain.Issue) {
	alreadyClosed := issue.ManuallyClosed()
	closed, err := h.repo.CloseIssue(issue.ID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, repo.ErrIssueCloseBusy) {
			status = http.StatusConflict
		}
		c.JSON(status, map[string]string{"error": err.Error()})
		return
	}
	if !alreadyClosed {
		saved, err := h.appendStoredOperation(c, closed.ID, "close", "手动关闭需求")
		if err != nil {
			respond(c, err)
			return
		}
		closed = saved
	}
	h.writeIssueView(c, 200, closed)
}

func (h issuesHandlers) resyncTAPDIssue(c *gin.Context, issue domain.Issue) {
	if issue.Source != "tapd" || issue.ExternalRef == nil || strings.TrimSpace(issue.ExternalRef.ExternalID) == "" {
		c.JSON(400, map[string]string{"error": "仅 TAPD 来源的需求支持重新同步"})
		return
	}
	if h.sync == nil {
		c.JSON(400, map[string]string{"error": "TAPD 同步未配置"})
		return
	}
	h.configureSync(h.repo.SyncConfig())
	updated, err := h.sync.ResyncTAPDIssue(h.repo, issue)
	if err != nil {
		respond(c, err)
		return
	}
	saved, err := h.appendStoredOperation(c, updated.ID, "resync-tapd", "从 TAPD 重新同步需求内容")
	if err != nil {
		respond(c, err)
		return
	}
	h.writeIssueView(c, 200, saved)
}

func (h issuesHandlers) issueRequirement(c *gin.Context, issue domain.Issue) {
	snap, ok := h.repo.RequirementSnapshot(issue.ID)
	if !ok {
		c.JSON(http.StatusNotFound, map[string]string{"error": "issue not found"})
		return
	}
	c.JSON(http.StatusOK, snap)
}

func (h issuesHandlers) issueSOPs(c *gin.Context, issue domain.Issue) {
	if c.Request.Method == http.MethodGet {
		project, _ := h.repo.Project(issue.ProjectID)
		c.JSON(http.StatusOK, map[string]any{
			"playbookId": issue.PlaybookID,
			"playbooks":  project.Playbooks(),
			"bindings":   domain.ResolveSOPBindings(issue, project),
		})
		return
	}
	var request struct {
		PlaybookID string                    `json:"playbookId"`
		Bindings   *[]domain.IssueSOPBinding `json:"bindings"`
	}
	if !decode(c, &request) {
		return
	}
	project, _ := h.repo.Project(issue.ProjectID)
	if id := strings.TrimSpace(request.PlaybookID); id != "" {
		if _, ok := project.Playbook(id); !ok {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "未知的需求套餐"})
			return
		}
		issue.PlaybookID = id
		if request.Bindings == nil {
			issue.SOPBindings = nil
		}
		if book, ok := project.Playbook(id); ok && len(book.Bindings) > 0 {
			issue.WorkflowID = book.Bindings[0].WorkflowID
		}
	}
	if request.Bindings != nil {
		bindings, err := h.normalizeIssueSOPBindings(issue.ProjectID, *request.Bindings)
		if err != nil {
			c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if bindings == nil {
			bindings = []domain.IssueSOPBinding{}
		}
		issue.SOPBindings = bindings
		if len(bindings) > 0 {
			issue.WorkflowID = bindings[0].WorkflowID
		}
	}
	if err := h.repo.PutIssue(issue); err != nil {
		respond(c, err)
		return
	}
	stored, _ := h.repo.Issue(issue.ID)
	c.JSON(http.StatusOK, stored)
}

func (h issuesHandlers) dispatchIssueRoute(c *gin.Context, issue domain.Issue) {
	var request struct {
		Rerun      bool   `json:"rerun"`
		WorkflowID string `json:"workflowId"`
	}
	if !decodeOptional(c, &request) {
		return
	}
	var triggerUserID int64
	if current, ok := h.requestUser(c.Request, true); ok && current != nil && current.ID > 0 {
		triggerUserID = current.ID
	}
	task, err := dispatchIssue(h.deps, dispatchRequest{IssueID: issue.ID, WorkflowID: request.WorkflowID, Rerun: request.Rerun, TriggerUserID: triggerUserID})
	if err != nil {
		respond(c, err)
		return
	}
	detail := "触发 SOP"
	if request.Rerun {
		detail = "重新运行 SOP"
	}
	_, _ = h.appendStoredOperation(c, issue.ID, "dispatch", detail)
	c.JSON(201, task)
}

// deleteIssue checks the complete history before cleaning failed/cancelled runs.
func (h issuesHandlers) deleteIssue(c *gin.Context, issue domain.Issue) {
	ids, err := h.repo.IssueDeletionRuns(issue.ID)
	if err == nil {
		for _, id := range ids {
			if err = h.engine.DeleteRun(id); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = h.repo.DeleteIssue(issue.ID)
	}
	if err != nil {
		switch {
		case errors.Is(err, repo.ErrIssueBusy), errors.Is(err, repo.ErrIssueSucceeded):
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		case errors.Is(err, repo.ErrIssueNotFound):
			c.JSON(http.StatusNotFound, map[string]string{"error": "issue not found"})
		default:
			respond(c, err)
		}
		return
	}
	c.Status(http.StatusNoContent)
}
