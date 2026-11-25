package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// SOPWorkBranchRequest contains the task and the repository's naming
// instructions. The model needs neither repository access nor Git credentials.
type SOPWorkBranchRequest struct {
	Execution   domain.TelemetryContext  `json:"-"`
	Executor    domain.AssistantDefaults `json:"-"`
	Repository  string                   `json:"repository"`
	Task        string                   `json:"task"`
	IssueNumber int                      `json:"issueNumber,omitempty"`
	Prompt      string                   `json:"namingInstructions,omitempty"`
	Feedback    string                   `json:"feedback,omitempty"`
}

func (e *Engine) assignSOPWorkBranches(ctx context.Context, ec domain.TelemetryContext, repositories []domain.RepositoryContext, input string, now time.Time) error {
	issueID := strings.TrimSpace(ec.IssueID)
	getIssue := sync.OnceValues(func() (domain.Issue, bool) {
		if issueID == "" {
			return domain.Issue{}, false
		}
		return e.repo.Issue(issueID)
	})
	generateDefault := sync.OnceValue(func() string {
		if issue, found := getIssue(); found {
			return domain.DefaultWorkBranch(issue, now)
		}
		return defaultWorkBranch(ec.IssueNumber, issueID, now)
	})
	for i := range repositories {
		repository := &repositories[i]
		if repository.ReadOnly || repository.AlignOnly || repository.RequireExistingWorkBranch ||
			repository.CreateNewWorkBranch || strings.TrimSpace(repository.WorkBranch) != "" {
			continue
		}
		definition, ok := e.repo.Repository(repository.RepositoryID)
		if !ok || definition.WorkBranchNaming == nil || ec.ProjectID != "" && ec.ProjectID != definition.ProjectID {
			continue
		}
		naming, err := repo.NormalizeRepositoryWorkBranchNaming(*definition.WorkBranchNaming)
		if err != nil {
			return fmt.Errorf("仓库 %q 的工作分支命名配置无效：%w", definition.Name, err)
		}
		branch := naming.Prefix + strings.TrimPrefix(generateDefault(), domain.DefaultWorkBranchPrefix)
		validate := func(branch string) error {
			return validateSOPWorkBranch(branch, *repository, definition)
		}
		if naming.Mode == domain.WorkBranchNamingAI {
			executor, _ := repo.BuiltInExecutor(naming.ExecutorID)
			task := input
			if issue, found := getIssue(); found {
				task = strings.Join([]string{issue.Title, issue.Description, issue.Clarification.Summary, issue.IdentityBlock()}, "\n\n")
			}
			branch, err = e.generateSOPWorkBranch(ctx, SOPWorkBranchRequest{
				Execution: ec, Executor: domain.AssistantDefaults{Executor: executor.Kind, Model: naming.Model}, Repository: definition.Name,
				Task: task, IssueNumber: ec.IssueNumber, Prompt: naming.Prompt,
			}, validate)
		} else {
			err = validate(branch)
		}
		if err != nil {
			return fmt.Errorf("仓库 %q 的工作分支生成失败：%w", definition.Name, err)
		}
		repository.WorkBranch = branch
		repository.CreateNewWorkBranch = naming.Mode == domain.WorkBranchNamingAI || naming.Prefix != domain.DefaultWorkBranchPrefix
	}
	assignWorkBranches(repositories, generateDefault)
	return nil
}

func validateSOPWorkBranch(branch string, repository domain.RepositoryContext, definition domain.Repository) error {
	if !domain.ValidNewWorkBranchName(branch) {
		return fmt.Errorf("请提供合法的 Git 分支名")
	}
	repository.WorkBranch = branch
	if !existingDevelopmentBranch(repository) || branch == "agentflow-base" ||
		domain.SameGitBranch(branch, definition.DefaultBranch) || domain.SameGitBranch(branch, definition.IntegrateBranch) {
		return fmt.Errorf("分支名与主分支、集成分支或工作区基线冲突")
	}
	return nil
}

func (e *Engine) generateSOPWorkBranch(ctx context.Context, request SOPWorkBranchRequest, validate func(string) error) (string, error) {
	if e.GenerateSOPWorkBranch == nil {
		return "", fmt.Errorf("AI 分支命名不可用，请检查 SOP 执行器")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var invalid error
	for range 2 {
		branch, err := e.GenerateSOPWorkBranch(ctx, request)
		if err != nil {
			return "", err
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		branch = strings.TrimSpace(branch)
		invalid = validate(branch)
		if invalid == nil {
			return branch, nil
		}
		request.Feedback = invalid.Error()
	}
	return "", invalid
}
