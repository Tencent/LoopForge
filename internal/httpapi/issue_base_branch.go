package httpapi

import (
	"fmt"
	"maps"
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
		return fmt.Errorf("baseBranch 仅支持单个代码仓库；多个仓库请通过 repositoryBranches 分别指定继续开发分支")
	}
	if issueSharedBranch(branch, repositories[0]) {
		return fmt.Errorf("不能直接在共享分支 %s 上开发，请填写已有功能分支或留空从配置主分支新建工作分支", branch)
	}
	return nil
}

// issueSharedBranch reports whether branch is a main or integration branch that
// work must never be committed to directly.
func issueSharedBranch(branch string, repository domain.Repository) bool {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return false
	}
	for _, shared := range []string{repository.DefaultBranch, repository.IntegrateBranch, "master", "main"} {
		if branch == strings.TrimSpace(shared) {
			return true
		}
	}
	return false
}

// A supplied repositoryBranches map is authoritative, including an empty map
// that restores automatic branch creation for every bound repository.
func normalizeIssueRepositoryBranches(issue *domain.Issue, repositories []domain.Repository) error {
	if issue.RepositoryBranches == nil {
		branch, err := normalizeIssueBaseBranch(issue.BaseBranch)
		if err != nil {
			return err
		}
		issue.BaseBranch = branch
		return validateIssueBaseBranchRepositories(branch, repositories)
	}
	bound := make(map[string]domain.Repository, len(repositories))
	for _, repository := range repositories {
		bound[repository.ID] = repository
	}
	branches := make(map[string]string, len(issue.RepositoryBranches))
	for id, raw := range issue.RepositoryBranches {
		repository, ok := bound[id]
		if !ok {
			return fmt.Errorf("继续开发分支指定了未绑定的代码仓库 %s", id)
		}
		branch, err := normalizeIssueBaseBranch(raw)
		if err == nil {
			err = validateIssueBaseBranchRepositories(branch, []domain.Repository{repository})
		}
		if err != nil {
			return fmt.Errorf("仓库 %s：%w", repository.Name, err)
		}
		if branch != "" {
			branches[id] = branch
		}
	}
	issue.RepositoryBranches = branches
	issue.BaseBranch = ""
	if len(repositories) == 1 {
		issue.BaseBranch = branches[repositories[0].ID]
	}
	return nil
}

// Partial edits and reimports keep branches for repositories that remain bound.
// Expanding a legacy single-repository issue preserves its original work branch.
func (h issuesHandlers) inheritIssueRepositoryBranches(issue *domain.Issue, previous domain.Issue) {
	if previous.RepositoryBranches == nil && len(issue.EffectiveRepositoryIDs()) <= 1 {
		return
	}
	h.normalizeLegacyIssueBaseBranch(&previous)
	branches := make(map[string]string)
	for _, id := range issue.EffectiveRepositoryIDs() {
		if branch := previous.WorkBranchForRepository(id); branch != "" {
			branches[id] = branch
		}
	}
	issue.RepositoryBranches = branches
	issue.BaseBranch = ""
}

// A stored shared branch can never be a valid selection: older intake and
// tracker sync populated BaseBranch with the repository default, which may have
// been renamed since. Interpret any shared branch as blank.
func legacyIssueDefaultBranch(branch string, repository domain.Repository) bool {
	return issueSharedBranch(branch, repository)
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
	if issue.ProjectID == previous.ProjectID && strings.TrimSpace(issue.BaseBranch) == strings.TrimSpace(previous.BaseBranch) && maps.Equal(issue.RepositoryBranches, previous.RepositoryBranches) && slices.Equal(ids(*issue), ids(previous)) {
		return
	}
	issue.Clarification.SessionID = ""
	issue.Clarification.SessionUserID = 0
}
