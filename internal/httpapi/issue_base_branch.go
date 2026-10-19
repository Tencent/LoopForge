package httpapi

import (
	"fmt"
	"slices"
	"strings"

	"agentflow/internal/domain"
)

// normalizeIssueBaseBranch accepts branch names, never revision expressions or
// command options. Empty creates a new branch from the configured main branch.
// Keep case and prefixes intact: both are meaningful parts of a Git branch name.
func normalizeIssueBaseBranch(raw string) (string, error) {
	branch := strings.TrimSpace(raw)
	if branch == "" {
		return "", nil
	}
	invalid := branch == "HEAD" || strings.HasPrefix(branch, "-") ||
		strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") ||
		strings.Contains(branch, "@{") || strings.ContainsAny(branch, "~^:?*[\\")
	for _, r := range branch {
		if r <= ' ' || r == 127 {
			invalid = true
		}
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			invalid = true
		}
	}
	if invalid {
		return "", fmt.Errorf("工作分支名称无效，请填写已有分支名称，例如 feature/my-feature")
	}
	return branch, nil
}

func validateIssueBaseBranchRepositories(branch string, repositories []domain.Repository) error {
	if branch == "" {
		return nil
	}
	if len(repositories) == 0 {
		return fmt.Errorf("指定已有工作分支前请先选择代码仓库")
	}
	if len(repositories) > 1 {
		return fmt.Errorf("继续已有工作分支目前仅支持单个代码仓库，请只选择一个仓库或留空新建工作分支")
	}
	for _, shared := range []string{repositories[0].DefaultBranch, repositories[0].IntegrateBranch, "master", "main"} {
		if branch == strings.TrimSpace(shared) {
			return fmt.Errorf("不能直接在共享分支 %s 上开发，请填写已有功能分支或留空从配置主分支新建工作分支", branch)
		}
	}
	return nil
}

func legacyIssueDefaultBranch(branch string, repository domain.Repository) bool {
	branch = strings.TrimSpace(branch)
	return branch != "" && branch == strings.TrimSpace(repository.DefaultBranch)
}

// Older intake and tracker sync populated BaseBranch with the first repository
// default. Preserve partial edits by interpreting that legacy value as blank.
func (h issuesHandlers) normalizeLegacyIssueBaseBranch(issue *domain.Issue) {
	ids := issue.EffectiveRepositoryIDs()
	if len(ids) == 0 {
		if project, ok := h.repo.Project(issue.ProjectID); ok && strings.TrimSpace(project.DefaultRepositoryID) != "" {
			ids = []string{project.DefaultRepositoryID}
		}
	}
	if len(ids) > 0 {
		if repository, ok := h.repo.Repository(ids[0]); ok && legacyIssueDefaultBranch(issue.BaseBranch, repository) {
			issue.BaseBranch = ""
		}
	}
}

// A CLI session remembers code from its original checkout. Keep the business
// answers and history, but start a fresh session when its repository changes.
func (h issuesHandlers) invalidateIssueClarifySessionOnRepositoryChange(issue *domain.Issue, previous domain.Issue) {
	ids := func(v domain.Issue) []string {
		out := v.EffectiveRepositoryIDs()
		if len(out) == 0 {
			if project, ok := h.repo.Project(v.ProjectID); ok && strings.TrimSpace(project.DefaultRepositoryID) != "" {
				out = []string{strings.TrimSpace(project.DefaultRepositoryID)}
			}
		}
		return out
	}
	if issue.ProjectID == previous.ProjectID && strings.TrimSpace(issue.BaseBranch) == strings.TrimSpace(previous.BaseBranch) && slices.Equal(ids(*issue), ids(previous)) {
		return
	}
	issue.Clarification.SessionID = ""
	issue.Clarification.SessionUserID = 0
}
