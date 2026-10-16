package httpapi

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

type dispatchRequest struct {
	IssueID, WorkflowID, ScheduleJobID, TriggerOrigin string
	Rerun                                             bool
	TriggerUserID                                     int64
}

func dispatchIssue(d *deps, req dispatchRequest) (domain.Task, error) {
	issue, ok := d.repo.Issue(req.IssueID)
	if !ok {
		return domain.Task{}, HTTPError{http.StatusNotFound, "issue not found"}
	}
	if reason := issue.ExecutionBlockedReason(); reason != "" {
		return domain.Task{}, HTTPError{http.StatusConflict, reason}
	}
	responsible, err := d.repo.ResponsibleUser(issue)
	if err != nil {
		return domain.Task{}, HTTPError{http.StatusConflict, err.Error()}
	}
	if issue.AssigneeUserID == 0 {
		issue.AssigneeUserID = responsible.ID
		if err := d.repo.PutIssue(issue); err != nil {
			return domain.Task{}, err
		}
	}
	runUserID := responsible.ID
	if req.TriggerUserID > 0 {
		if user, ok := d.repo.User(req.TriggerUserID); ok && user.ID > 0 {
			runUserID = user.ID
		}
	}
	scheduled := strings.TrimSpace(req.ScheduleJobID) != ""
	project, _ := d.repo.Project(issue.ProjectID)
	policy := domain.EffectiveClarificationPolicy(project.Clarification)
	if domain.RequirementSOPBlocked(issue, policy) {
		return domain.Task{}, HTTPError{http.StatusConflict, fmt.Sprintf("需求澄清未达标：当前 %d 分，需要 %d 分", issue.Clarification.Score, policy.Threshold)}
	}
	workflowID := strings.TrimSpace(req.WorkflowID)
	if workflowID == "" {
		workflowID = issue.WorkflowID
	}
	if workflowID == "" {
		if playbook := domain.ResolveSOPBindings(issue, project); len(playbook) > 0 {
			workflowID = playbook[0].WorkflowID
		}
	}
	if workflowID == "" {
		if wf, ok := d.repo.Workflow(repo.ProjectIssueWorkflowID(issue.ProjectID)); ok && wf.ID != "" {
			workflowID = wf.ID
		}
	}
	if workflowID == "" {
		workflowID = repo.DefaultWorkflowID(issue.Source, issue.Kind)
	}
	if workflowID == "" {
		workflowID = project.WorkflowID
	}
	if workflowID == "" {
		return domain.Task{}, HTTPError{http.StatusBadRequest, "issue requires a workflow (bind to SOP first)"}
	}
	if issue.SOPBindings != nil {
		if binding, ok := domain.ResolveSOPBinding(issue, project, workflowID); ok && binding.AssigneeUserID > 0 {
			if user, found := d.repo.User(binding.AssigneeUserID); found && user.ID > 0 {
				runUserID = user.ID
			}
		}
	}
	hasSameSOP := d.repo.IssueHasTaskForWorkflow(issue.ID, workflowID)
	// A schedule only picks up untouched issues; another SOP lane is a human decision.
	if scheduled && d.repo.IssueHasTask(issue.ID) {
		return domain.Task{}, HTTPError{http.StatusConflict, "该需求已经跑过 SOP"}
	}
	if !scheduled && !req.Rerun && hasSameSOP {
		return domain.Task{}, HTTPError{http.StatusConflict, "该需求的这条 SOP 已经跑过，如需重新执行请明确选择重新运行"}
	}
	if req.Rerun && !hasSameSOP {
		return domain.Task{}, HTTPError{http.StatusBadRequest, "该需求尚未运行这条 SOP，不能使用重新运行"}
	}
	repositoryIDs := issue.EffectiveRepositoryIDs()
	if len(repositoryIDs) == 0 {
		if p, ok := d.repo.Project(issue.ProjectID); ok && strings.TrimSpace(p.DefaultRepositoryID) != "" {
			repositoryIDs = []string{p.DefaultRepositoryID}
		}
	}
	var repositories []domain.RepositoryContext
	for _, id := range repositoryIDs {
		repo, found := d.repo.Repository(id)
		if !found || repo.ProjectID != issue.ProjectID {
			return domain.Task{}, HTTPError{http.StatusBadRequest, "bound repository is invalid"}
		}
		branch := repo.DefaultBranch
		// The issue-level base branch is only meaningful for a single
		// repository; with several, each repo uses its own configured default.
		if len(repositoryIDs) == 1 && strings.TrimSpace(issue.BaseBranch) != "" {
			branch = strings.TrimSpace(issue.BaseBranch)
		}
		rc := domain.RepositoryContext{RepositoryID: repo.ID, Provider: repo.Provider, Name: repo.Name, GongfengProjectID: repo.GongfengProjectID, WebURL: repo.WebURL, CloneURL: repo.CloneURL, BaseBranch: branch, LocalPath: repo.LocalPath, PipelineProjectID: repo.PipelineProjectID, PipelineDefID: repo.PipelineDefID, PipelineURLPattern: repo.PipelineURLPattern, DefaultBranch: repo.DefaultBranch}
		if scheduled {
			if integ := domain.NormalizeIntegrateBranch(repo.IntegrateBranch); integ != "" {
				rc.IntegrateBranch = integ
				rc.BaseBranch = integ
			}
		}
		repositories = append(repositories, rc)
	}
	input := issue.Title + "\n\n" + issue.Description
	if block := issue.IdentityBlock(); block != "" {
		input += "\n\n" + block
	}
	// Assign workspace directories before building the prompt so the Agent
	// sees the real per-repository directories. The engine re-assigns the
	// same values.
	domain.AssignRepositoryDirs(repositories)
	if summary := strings.TrimSpace(issue.Clarification.Summary); summary != "" {
		input += "\n\nClarification summary:\n" + summary
	}
	if issue.Kind == "requirement" && len(issue.Clarification.Answers) > 0 {
		input += fmt.Sprintf("\n\nClarification score: %d/100 (passed=%t)", issue.Clarification.Score, issue.Clarification.Passed)
		if p, ok := d.repo.Project(issue.ProjectID); ok {
			for _, d := range p.Clarification.Dimensions {
				if a := issue.Clarification.Answers[d.ID]; a != "" {
					input += "\n- " + d.Name + ": " + a
				}
			}
		}
	}
	if len(repositories) > 0 {
		if len(repositories) == 1 {
			input += "\n\nRepository context (frozen): the repository is checked out in a first-level subdirectory; SOP deliverables stay at the workspace root"
		} else {
			input += "\n\nRepository context (frozen, multi-repository): every repository below is checked out side by side; treat them as equals"
		}
		for _, rc := range repositories {
			input += fmt.Sprintf("\n- repository: %s (%s)\n  - provider: %s\n  - base branch: %s\n  - clone URL: %s\n  - local directory: ./%s (code changes for this repository MUST be written inside this directory)", rc.Name, rc.GongfengProjectID, rc.Provider, rc.BaseBranch, rc.CloneURL, rc.WorkspaceDir)
			if scheduled && strings.TrimSpace(rc.IntegrateBranch) != "" {
				input += fmt.Sprintf("\n  - scheduled integration branch: %s\n  - configured main branch: %s\n  - before this SOP starts, AgentFlow merges the configured main branch into the integration baseline (never the other way); the workspace is checked out from that baseline. After the SOP, only the work branch is merged back to the integration baseline. Humans merge the baseline into the configured main branch.", rc.IntegrateBranch, rc.DefaultBranch)
			}
		}
		input += "\n- SOP deliverables (fix-plan.md and other reports not tied to one repository) go to the workspace root, never inside a repository directory\n- do not write to any host absolute path"
	}
	input = appendSOPHandoff(issue, project, workflowID, input)
	taskID, dispatchMode := primaryTaskID(issue.ID, workflowID), "primary"
	if scheduled {
		dispatchMode = "scheduled"
	}
	if req.Rerun {
		taskID, dispatchMode = fmt.Sprintf("task-%d", time.Now().UnixNano()), "rerun"
	}
	triggerOrigin := strings.TrimSpace(req.TriggerOrigin)
	if triggerOrigin == "" {
		if scheduled {
			triggerOrigin = "timer"
		} else {
			triggerOrigin = "manual"
		}
	}
	var displayRepository domain.RepositoryContext
	if len(repositories) > 0 {
		displayRepository = repositories[0]
	}
	task := domain.Task{ID: taskID, IssueID: issue.ID, AgentID: issue.AssigneeID, WorkflowID: workflowID, Status: "queued", Repository: displayRepository, DispatchMode: dispatchMode, ScheduleJobID: strings.TrimSpace(req.ScheduleJobID), TriggerOrigin: triggerOrigin}
	stored, created, err := d.repo.CreateTask(task)
	if err != nil {
		return domain.Task{}, err
	}
	if !created {
		if scheduled {
			return domain.Task{}, HTTPError{http.StatusConflict, "该需求的这条 SOP 已经跑过"}
		}
		return domain.Task{}, HTTPError{http.StatusConflict, "该需求的这条 SOP 正在创建或已经完成首次执行，请选择重新运行"}
	}
	issue.LatestTaskID = stored.ID
	issue.Status = "in_progress"
	if err := d.repo.PutIssue(issue); err != nil {
		task.Status = "failed"
		_ = d.repo.PutTask(task)
		return domain.Task{}, err
	}
	tapdID := issue.RequirementTAPDID()
	run, err := d.engine.StartWithRepositoriesContext(workflowID, input, repositories, domain.TelemetryContext{ProjectID: issue.ProjectID, IssueID: issue.ID, TAPDID: tapdID, ResponsibleUserID: runUserID, IssueNumber: issue.Number, TaskID: taskID, ScheduleJobID: strings.TrimSpace(req.ScheduleJobID)})
	if err != nil {
		task.Status = "failed"
		_ = d.repo.PutTask(task)
		issue.Status = "blocked"
		_ = d.repo.PutIssue(issue)
		return domain.Task{}, err
	}
	// Bind only after the run actually starts. A second lane keeps the SOP
	// that already ran; a stored list of several SOPs is left unchanged.
	// A failed start returns above and does not reach this write.
	alreadyStarted := strings.TrimSpace(issue.WorkflowID) != "" && issue.WorkflowID != workflowID && d.repo.IssueHasTaskForWorkflow(issue.ID, issue.WorkflowID)
	if len(issue.SOPBindings) > 1 {
		if strings.TrimSpace(issue.WorkflowID) == "" {
			issue.WorkflowID = workflowID
		}
	} else if !alreadyStarted {
		issue.WorkflowID = workflowID
		if len(issue.SOPBindings) == 1 {
			issue.SOPBindings[0].WorkflowID = workflowID
		}
	}
	if err := d.repo.PutIssue(issue); err != nil {
		return domain.Task{}, err
	}
	// Starting the issue over is not a retry of the previous run, so the
	// container that run was holding for a retry has no reader left.
	d.engine.SupersedeIssueRuns(issue.ID, run.ID)
	task.RunID, task.Status, task.Repository = run.ID, taskStatusFromRun(run.Status), run.Repository
	if err := d.repo.PutTask(task); err != nil {
		return domain.Task{}, err
	}
	stored, _ = d.repo.Task(task.ID)
	return stored, nil
}

func primaryTaskID(issueID, workflowID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(issueID) + "\n" + strings.TrimSpace(workflowID)))
	return fmt.Sprintf("task-primary-%x", sum[:12])
}

func appendSOPHandoff(issue domain.Issue, project domain.Project, workflowID, input string) string {
	bindings := domain.ResolveSOPBindings(issue, project)
	index := -1
	var current domain.IssueSOPBinding
	for i, binding := range bindings {
		if binding.WorkflowID == workflowID {
			index = i
			current = binding
			break
		}
	}
	if index < 0 {
		return input
	}
	prev := make([]domain.IssueSOPBinding, 0, len(current.DependsOn)+1)
	if len(current.DependsOn) > 0 {
		want := map[string]bool{}
		for _, id := range current.DependsOn {
			want[id] = true
		}
		for _, binding := range bindings {
			if want[binding.ID] {
				prev = append(prev, binding)
			}
		}
	} else if index > 0 {
		prev = append(prev, bindings[index-1])
	}
	if len(prev) == 0 {
		return input
	}
	input += "\n\nHandoff from previous SOP steps on this requirement:"
	for _, binding := range prev {
		input += "\n- " + binding.WorkflowID
		if len(current.Handoff) > 0 {
			input += " → expected artifacts: " + strings.Join(current.Handoff, ", ")
		}
	}
	input += "\nContinue from those deliverables; do not redo the previous SOP."
	return input
}
