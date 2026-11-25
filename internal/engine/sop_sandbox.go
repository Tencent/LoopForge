package engine

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// SOPSandboxSelection contains the metadata the one-shot selector needs.
// Sandbox recipes and credentials stay in the catalog.
type SOPSandboxSelection struct {
	Execution           domain.TelemetryContext  `json:"-"`
	Executor            domain.AssistantDefaults `json:"-"`
	ProjectName         string                   `json:"projectName,omitempty"`
	ProjectDescription  string                   `json:"projectDescription,omitempty"`
	WorkflowName        string                   `json:"workflowName"`
	WorkflowDescription string                   `json:"workflowDescription,omitempty"`
	Task                string                   `json:"task,omitempty"`
	Repositories        []string                 `json:"repositories,omitempty"`
	ScriptLanguages     []string                 `json:"scriptLanguages,omitempty"`
	DefaultSandboxID    string                   `json:"defaultSandboxId"`
	WorkspaceSandboxID  string                   `json:"workspaceSandboxId,omitempty"`
	Candidates          []SOPSandboxCandidate    `json:"candidates"`
}

type SOPSandboxCandidate struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Driver    string `json:"driver"`
	Toolchain string `json:"toolchain,omitempty"`
	Project   bool   `json:"projectSandbox,omitempty"`
}

func (e *Engine) selectSOPSandbox(ctx context.Context, wf domain.WorkflowDefinition, ec *domain.TelemetryContext, repositories []domain.RepositoryContext, input, workspaceSandboxID string) (domain.SandboxProfile, string, error) {
	choices := e.repo.ConversationSandboxes(ec.ProjectID)
	runner, _ := e.repo.Runner(ec.ExecutionRunnerID)
	nativeAvailable := runner.SupportsNativeSandbox
	dockerAvailable := true
	var poolLabel string
	if ec.ExecutionPool == domain.ExecutionModeCloud && !ec.ExecutionPinned && ec.ParentRunID == "" {
		poolLabel = e.cloudPoolLabel()
	}
	if poolLabel != "" {
		// A new pool run can start on any compatible online machine.
		// Existing workspaces are tied to their owning machine.
		runners := e.eligiblePoolRunners(poolLabel)
		nativeAvailable = func(driver string) bool {
			return slices.ContainsFunc(runners, func(runner domain.Runner) bool {
				return sopNativeRunnerSupports(runner, driver, wf, repositories)
			})
		}
	} else if ec.ExecutionRunnerID == "" && ec.ExecutionPool != domain.ExecutionModeCloud && e.executes() {
		nativeAvailable = runtime.NativeSandboxAvailable
		dockerAvailable = runtime.DockerReady()
	}
	supported := func(profile domain.SandboxProfile) bool {
		if profile.Driver == domain.SandboxDriverDocker {
			return dockerAvailable && !profile.Privileged
		}
		return nativeAvailable(profile.Driver)
	}
	choices = slices.DeleteFunc(choices, func(profile domain.SandboxProfile) bool { return !supported(profile) })
	if len(choices) == 0 {
		return domain.SandboxProfile{}, "", fmt.Errorf("当前项目和执行机器没有可用的 SOP 沙箱")
	}
	rank := func(profile domain.SandboxProfile) int {
		switch {
		case profile.ID == workspaceSandboxID:
			return 0
		case profile.ID == repo.DefaultSandboxProfileID:
			return 1
		case profile.ProjectID == ec.ProjectID:
			return 2
		default:
			return 3
		}
	}
	slices.SortFunc(choices, func(a, b domain.SandboxProfile) int {
		return cmp.Or(rank(a)-rank(b), cmp.Compare(a.ID, b.ID))
	})
	var selected string
	if len(choices) > 1 && e.SelectSOPSandbox != nil {
		request := e.sopSandboxSelection(wf, *ec, repositories, input, choices, workspaceSandboxID)
		selectionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		selected = strings.TrimSpace(e.SelectSOPSandbox(selectionCtx, request))
		cancel()
	}
	if err := ctx.Err(); err != nil {
		return domain.SandboxProfile{}, "", err
	}
	if selected != "" {
		for i, choice := range choices {
			if choice.ID == selected {
				// A shortlist item may have been edited or unshared while the Agent
				// was deciding. Recheck eligibility before freezing the profile.
				copy(choices[1:i+1], choices[:i])
				choices[0] = choice
				break
			}
		}
	}
	for _, choice := range choices {
		profile, err := e.repo.ConversationSandbox(ec.ProjectID, choice.ID)
		if err != nil || !supported(profile) {
			continue
		}
		if poolLabel != "" && profile.Driver != domain.SandboxDriverDocker {
			// The set of online machines may have changed during the model
			// call. Use the next candidate if the native choice lost its host.
			compatible := func(runner domain.Runner) bool {
				return sopNativeRunnerSupports(runner, profile.Driver, wf, repositories)
			}
			target, ok := e.repo.Runner(ec.ExecutionRunnerID)
			if !ok || !poolRunnerEligible(target, poolLabel) || !compatible(target) {
				target, ok = e.pickPoolRunner(poolLabel, compatible)
				if !ok {
					continue
				}
				ec.ExecutionRunnerID = target.ID
			}
		}
		source := "自动选择使用默认沙箱"
		switch {
		case profile.ID == selected:
			source = "Agent 选择沙箱"
		case profile.ID == workspaceSandboxID:
			source = "自动选择沿用工作区沙箱"
		case len(choices) == 1:
			source = "自动选择使用唯一可用的沙箱"
		}
		return profile, source + " " + profile.ID, nil
	}
	return domain.SandboxProfile{}, "", fmt.Errorf("当前项目和执行机器没有可用的 SOP 沙箱")
}

func sopNativeRunnerSupports(runner domain.Runner, driver string, wf domain.WorkflowDefinition, repositories []domain.RepositoryContext) bool {
	return runner.SupportsNativeSandbox(driver) &&
		(len(wf.ProjectCredentialIDs) == 0 || runner.SupportsProjectCredentials()) &&
		runnerRepositoryCapabilityError(runner, repositories) == nil
}

func (e *Engine) sopSandboxSelection(wf domain.WorkflowDefinition, ec domain.TelemetryContext, repositories []domain.RepositoryContext, input string, choices []domain.SandboxProfile, workspaceSandboxID string) SOPSandboxSelection {
	project, _ := e.repo.Project(ec.ProjectID)
	task := input
	if issue, ok := e.repo.Issue(ec.IssueID); ok && issue.ProjectID == ec.ProjectID && ec.ParentRunID == "" {
		// Dispatch also puts repository URLs and checkout instructions into the
		// run input. The selector only needs the requirement itself.
		task = strings.Join([]string{issue.Title, issue.Description, issue.Clarification.Summary}, "\n\n")
	}
	request := SOPSandboxSelection{
		Execution: ec, Executor: e.sopExecutorAccess(ec).defaults(),
		ProjectName: project.Name, ProjectDescription: project.Description,
		WorkflowName: wf.Name, WorkflowDescription: wf.Description, Task: task,
		DefaultSandboxID: choices[0].ID,
	}
	for _, profile := range choices {
		request.Candidates = append(request.Candidates, SOPSandboxCandidate{
			ID: profile.ID, Name: profile.Name, Driver: profile.Driver,
			Toolchain: profile.Toolchain, Project: ec.ProjectID != "" && profile.ProjectID == ec.ProjectID,
		})
		if profile.ID == workspaceSandboxID {
			request.WorkspaceSandboxID = workspaceSandboxID
		}
	}
	for _, repository := range repositories {
		request.Repositories = append(request.Repositories, cmp.Or(repository.Name, repository.RepositoryID))
	}
	if len(repositories) == 0 && project.DefaultRepositoryID != "" {
		if repository, ok := e.repo.Repository(project.DefaultRepositoryID); ok && repository.ProjectID == ec.ProjectID {
			request.Repositories = []string{cmp.Or(repository.Name, repository.ID)}
		}
	}
	for _, node := range wf.Nodes {
		if language := node.ScriptLanguage; language != "" && !slices.Contains(request.ScriptLanguages, language) {
			request.ScriptLanguages = append(request.ScriptLanguages, language)
		}
	}
	return request
}
