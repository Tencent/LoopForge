package repo

import (
	"agentflow/internal/domain"
	"errors"
	"strings"
)

var ErrIssueCloseBusy = errors.New("需求正在澄清或执行 SOP，请先停止后再关闭")

// CloseIssue belongs to the issue domain and must not close active work.
func (r *IssuesRepo) CloseIssue(issueID string) (domain.Issue, error) {
	return r.PatchIssue(strings.TrimSpace(issueID), func(issue *domain.Issue) error {
		allowed, err := r.IssueClosureAllowed([]domain.Issue{*issue})
		if err != nil {
			return err
		}
		if !allowed[issue.ID] {
			return ErrIssueCloseBusy
		}
		issue.Status = "closed"
		issue.CloseSource = "manual"
		issue.AutomationState = "ignored"
		return nil
	})
}
