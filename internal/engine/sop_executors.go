package engine

import (
	"errors"
	"fmt"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

type SOPExecutorRequirement struct {
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
}

var errSOPExecutorUnconfigured = errors.New("请在个人配置中添加当前执行环境可用的执行器，再重试此节点")

type sopExecutorAccess struct {
	user            domain.User
	runner          domain.Runner
	usesRunner      bool
	sharedCodeBuddy bool
}

func (e *Engine) sopExecutorAccess(ec domain.TelemetryContext) sopExecutorAccess {
	user, _ := e.repo.User(ec.ResponsibleUserID)
	runner, _ := e.repo.Runner(ec.ExecutionRunnerID)
	return sopExecutorAccess{
		user: user, runner: runner, usesRunner: ec.ExecutionRunnerID != "",
		sharedCodeBuddy: resolveCodeBuddyCredential(e.repo, user.ID).Sponsored,
	}
}

func (a sopExecutorAccess) configured(kind string) bool {
	if a.usesRunner {
		if !a.runner.SupportsProvider(kind) {
			return false
		}
		if domain.SupportsRunnerLogin(kind) && a.runner.RuntimeAuthMode(kind) == domain.RuntimeAuthLocal {
			return true
		}
		if kind == "codebuddy" && strings.TrimSpace(a.user.CodeBuddyBaseURL) != "" && !a.runner.SupportsCodeBuddyBaseURL() {
			return false
		}
	}
	return a.user.HasExecutorCredential(kind) || kind == "codebuddy" && a.sharedCodeBuddy
}

func (a sopExecutorAccess) defaults() domain.AssistantDefaults {
	preferred := a.user.AssistantDefaults
	if ex, ok := repo.BuiltInExecutorForKind(preferred.Executor); ok && a.user.HasExecutorCredential(ex.Kind) && a.configured(ex.Kind) {
		model := strings.TrimSpace(preferred.Model)
		if model == "" {
			model = a.firstSavedModel(ex.Kind)
		}
		return domain.AssistantDefaults{Executor: ex.ID, Model: model, Effort: preferred.Effort}
	}
	executors := repo.BuiltInExecutors()
	for _, ex := range executors {
		if a.user.HasExecutorCredential(ex.Kind) && a.configured(ex.Kind) {
			return domain.AssistantDefaults{Executor: ex.ID, Model: a.firstSavedModel(ex.Kind)}
		}
	}
	for _, ex := range executors {
		if a.usesRunner && domain.SupportsRunnerLogin(ex.Kind) && a.runner.RuntimeAuthMode(ex.Kind) == domain.RuntimeAuthLocal && a.configured(ex.Kind) {
			return domain.AssistantDefaults{Executor: ex.ID}
		}
	}
	if a.configured("codebuddy") {
		return domain.AssistantDefaults{Executor: "codebuddy"}
	}
	return domain.AssistantDefaults{}
}

func (a sopExecutorAccess) firstSavedModel(kind string) string {
	var models []domain.CLIModel
	switch kind {
	case "codebuddy":
		models = a.user.CodeBuddyModels
	case "codex":
		models = a.user.CodexModels
	case "claude-code":
		models = a.user.ClaudeCodeModels
	default:
		models = a.user.AgentCLICredentials[kind].Models
	}
	if len(models) != 0 {
		return models[0].ID
	}
	return ""
}

func templateSOPRoleIDs(st *repo.Store, wf domain.WorkflowDefinition) map[string]bool {
	source := wf.ID
	if !wf.Generic {
		source = ""
		for _, tag := range wf.Tags {
			if id, found := strings.CutPrefix(tag, "generic-source:"); found {
				source = id
				break
			}
		}
	}
	template, ok := st.GenericTemplateByWorkflow(source)
	if !ok {
		return nil
	}
	ids := make(map[string]bool, len(template.Agents))
	for _, role := range template.Agents {
		ids[role.ID] = true
		if !wf.Generic {
			ids[role.ID+"-"+wf.ProjectID] = true
		}
	}
	return ids
}

// Only roles without a pinned model or effort can inherit an executor.
// The caller decides which legacy CodeBuddy roles are eligible.
func followsSOPExecutorDefault(node domain.WorkflowNode, role domain.AgentDefinition) bool {
	return node.Type == "agent" && node.ExecutorID == "" &&
		(role.ExecutorID == "" || role.ExecutorID == "codebuddy") &&
		strings.TrimSpace(role.Model) == "" && strings.TrimSpace(role.Effort) == ""
}

func (e *Engine) planSOPExecutors(wf domain.WorkflowDefinition, ec domain.TelemetryContext, preflight bool) (map[string]domain.AssistantDefaults, []SOPExecutorRequirement, error) {
	roleIDs := templateSOPRoleIDs(e.repo, wf)
	access := e.sopExecutorAccess(ec)
	defaults := access.defaults()
	selected := make(map[string]domain.AssistantDefaults)
	requirements := make([]SOPExecutorRequirement, 0)
	seen := make(map[string]bool)
	for _, node := range wf.Nodes {
		if node.Type != "agent" {
			continue
		}
		role, ok := workflowAgent(e.repo, wf, node.AgentID)
		if !ok {
			if !preflight {
				continue
			}
			return nil, nil, fmt.Errorf("agent %q not found", node.AgentID)
		}
		legacyTemplate := roleIDs[role.ID] && role.ProjectID == wf.ProjectID
		baselineResolver := wf.ID == BaselineAlignWorkflowID && role.ID == BaselineAlignAgentID
		var ex domain.ExecutorDefinition
		if (role.ExecutorID == "" || legacyTemplate || baselineResolver) && followsSOPExecutorDefault(node, role) {
			selected[node.ID] = defaults
			if defaults.Executor != "" {
				ex, _ = repo.BuiltInExecutor(defaults.Executor)
			}
		} else if preflight {
			var err error
			ex, err = resolveNodeExecutor(e.repo, node, role)
			if err != nil {
				return nil, nil, err
			}
		}
		if preflight && !seen[ex.Kind] {
			seen[ex.Kind] = true
			requirements = append(requirements, SOPExecutorRequirement{
				Kind: ex.Kind, Name: ex.Name, Configured: ex.Kind != "" && access.configured(ex.Kind),
			})
		}
	}
	return selected, requirements, nil
}

// SOPExecutorRequirements checks the same executor configuration a manual
// start uses. It only reports provider names and configuration status.
func (e *Engine) SOPExecutorRequirements(wf domain.WorkflowDefinition, userID int64, projectID string) ([]SOPExecutorRequirement, error) {
	wf, err := configureBaselineAlignCLI(wf)
	if err != nil {
		return nil, err
	}
	wf, err = domain.AttachPreviewBuild(wf)
	if err != nil {
		return nil, err
	}
	ec := domain.TelemetryContext{ResponsibleUserID: userID, ProjectID: projectID}
	e.selectSOPExecutionTarget(&ec)
	_, requirements, err := e.planSOPExecutors(wf, ec, true)
	return requirements, err
}

func workflowNodeExecutor(st *repo.Store, wf domain.WorkflowDefinition, node domain.WorkflowNode, role domain.AgentDefinition) (domain.ExecutorDefinition, error) {
	if defaults, ok := wf.ExecutorDefaults[node.ID]; ok && followsSOPExecutorDefault(node, role) {
		if defaults.Executor == "" {
			return domain.ExecutorDefinition{}, errSOPExecutorUnconfigured
		}
		role.ExecutorID, role.Model, role.Effort = defaults.Executor, defaults.Model, defaults.Effort
	}
	return resolveNodeExecutor(st, node, role)
}

// Runs can be queued before their responsible user has configured an executor.
// Resolve the deferred default on the bound machine before the first role runs.
func (e *Engine) resolveDeferredSOPExecutor(run domain.Run, node domain.WorkflowNode) (domain.Run, error) {
	defaults, pending := run.WorkflowSnapshot.ExecutorDefaults[node.ID]
	if !pending || defaults.Executor != "" {
		return run, nil
	}
	role, found := workflowAgent(e.repo, run.WorkflowSnapshot, node.AgentID)
	if !found || !followsSOPExecutorDefault(node, role) {
		return run, nil
	}
	selected := e.sopExecutorAccess(run.ExecutionContext).defaults()
	if selected.Executor == "" {
		return run, errSOPExecutorUnconfigured
	}
	err := e.updateRunErr(run.ID, func(current *domain.Run) {
		for id, choice := range current.WorkflowSnapshot.ExecutorDefaults {
			if choice.Executor == "" {
				current.WorkflowSnapshot.ExecutorDefaults[id] = selected
			}
		}
		run = *current
	})
	return run, err
}
