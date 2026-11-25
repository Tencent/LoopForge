package engine

import (
	"errors"
	"fmt"
	"slices"
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
	execution       domain.TelemetryContext
	sharedCodeBuddy bool
}

func (e *Engine) sopExecutorAccess(ec domain.TelemetryContext) sopExecutorAccess {
	return newSOPExecutorAccess(e.repo, ec)
}

func newSOPExecutorAccess(s *repo.Store, ec domain.TelemetryContext) sopExecutorAccess {
	user, _ := s.User(ec.ResponsibleUserID)
	runner, _ := s.Runner(ec.ExecutionRunnerID)
	return sopExecutorAccess{
		user: user, runner: runner, usesRunner: ec.ExecutionRunnerID != "", execution: ec,
		sharedCodeBuddy: resolveCodeBuddyCredential(s, user.ID).Sponsored,
	}
}

// ExecutorCredentials reports which CLIs have a credential for a SOP run that
// userID starts in projectID (empty for a personal SOP). It resolves the
// machine the same way a new SOP run does: the user's project default first,
// then the personal default when the project may use it. Pickers use it before
// the run's machine is known, so the run-time check still verifies that the
// machine provides the CLI.
func ExecutorCredentials(s *repo.Store, userID int64, projectID string) func(kind string) bool {
	runnerID := s.ResolveRunnerDefaults(userID, projectID).Runner.ID
	return newSOPExecutorAccess(s, domain.TelemetryContext{ResponsibleUserID: userID, ProjectID: projectID, ExecutionRunnerID: runnerID}).hasCredential
}

// ExecutorCredentials also follows the cloud runner pool: a user without a
// default machine runs on a pool runner, like selectSOPExecutionTarget picks.
// The pick is random, so a CLI counts when any online pool runner logs it in;
// the run-time check still verifies the machine it lands on.
func (e *Engine) ExecutorCredentials(userID int64, projectID string) func(kind string) bool {
	ec := domain.TelemetryContext{ResponsibleUserID: userID, ProjectID: projectID}
	ec.ExecutionRunnerID = e.repo.ResolveRunnerDefaults(userID, projectID).Runner.ID
	access := newSOPExecutorAccess(e.repo, ec)
	if ec.ExecutionRunnerID != "" {
		return access.hasCredential
	}
	e.mu.Lock()
	pool := e.cloudPool
	e.mu.Unlock()
	if !pool.serves(projectID) {
		return access.hasCredential
	}
	candidates := []sopExecutorAccess{access}
	for _, runner := range e.eligiblePoolRunners(pool.Label) {
		onRunner := access
		onRunner.runner, onRunner.usesRunner = runner, true
		onRunner.execution.ExecutionRunnerID = runner.ID
		onRunner.execution.ExecutionPool = domain.ExecutionModeCloud
		candidates = append(candidates, onRunner)
	}
	return func(kind string) bool {
		for _, candidate := range candidates {
			if candidate.hasCredential(kind) {
				return true
			}
		}
		return false
	}
}

func sopRunnerAuthMode(ec domain.TelemetryContext, runner domain.Runner, provider string) string {
	if ec.ExecutionPool == domain.ExecutionPoolProjectAuto && runner.OwnerUserID == 0 {
		return domain.RuntimeAuthPersonal
	}
	// A conversation allocated to a platform runner keeps using the
	// responsible user's credentials when it hands off to an SOP.
	if ec.ExecutionPinned && ec.WorkspaceRunID != "" && runner.OwnerUserID == 0 && ec.ResponsibleUserID > 0 {
		return domain.RuntimeAuthPersonal
	}
	return runner.RuntimeAuthModeForUser(provider, ec.ResponsibleUserID)
}

func (a sopExecutorAccess) runnerLogin(kind string) bool {
	return a.usesRunner && domain.SupportsRunnerLogin(kind) && sopRunnerAuthMode(a.execution, a.runner, kind) == domain.RuntimeAuthLocal
}

func (a sopExecutorAccess) configured(kind string) bool {
	if a.usesRunner {
		if !a.runner.SupportsProvider(kind) {
			return false
		}
		if kind == "codebuddy" && strings.TrimSpace(a.user.CodeBuddyBaseURL) != "" && !a.runner.SupportsCodeBuddyBaseURL() {
			return false
		}
	}
	return a.hasCredential(kind)
}

// hasCredential accepts a personal key, the platform's shared CodeBuddy Key,
// or account login on the run's machine.
func (a sopExecutorAccess) hasCredential(kind string) bool {
	if a.runner.SupportsProvider(kind) && a.runnerLogin(kind) {
		return true
	}
	return a.user.HasExecutorCredential(kind) || kind == "codebuddy" && a.sharedCodeBuddy
}

func (a sopExecutorAccess) defaults() domain.AssistantDefaults {
	preferred := a.user.AssistantDefaults
	if ex, ok := repo.BuiltInExecutorForKind(preferred.Executor); ok && a.user.HasExecutorCredential(ex.Kind) && a.configured(ex.Kind) {
		return a.defaultsFor(ex, preferred.Model, preferred.Effort)
	}
	executors := repo.BuiltInExecutors()
	for _, ex := range executors {
		if a.user.HasExecutorCredential(ex.Kind) && a.configured(ex.Kind) {
			return a.defaultsFor(ex, "", "")
		}
	}
	for _, ex := range executors {
		if a.runnerLogin(ex.Kind) && a.configured(ex.Kind) {
			return a.defaultsFor(ex, "", "")
		}
	}
	if a.configured("codebuddy") {
		return domain.AssistantDefaults{Executor: "codebuddy"}
	}
	return domain.AssistantDefaults{}
}

func (a sopExecutorAccess) defaultsFor(ex domain.ExecutorDefinition, model, effort string) domain.AssistantDefaults {
	if (ex.Kind == "codex" || ex.Kind == "claude-code") && a.runnerLogin(ex.Kind) {
		return a.runnerLoginDefaults(ex)
	}
	model = strings.TrimSpace(model)
	if model == "" && a.user.HasExecutorCredential(ex.Kind) {
		model = a.firstSavedModel(ex.Kind)
	}
	return domain.AssistantDefaults{Executor: ex.ID, Model: model, Effort: effort}
}

func (a sopExecutorAccess) runnerLoginDefaults(ex domain.ExecutorDefinition) domain.AssistantDefaults {
	defaults := domain.AssistantDefaults{Executor: ex.ID}
	for _, model := range a.runner.LoginModels[ex.Kind] {
		if model.ID == "" || model.ID == "default" || model.ID == ex.Kind+":default" || strings.HasPrefix(model.ID, "-") {
			continue
		}
		defaults.Model = model.ID
		levels := model.ReasoningEfforts
		if levels == nil {
			levels = model.DiscoveredReasoningEfforts
		}
		if levels != nil {
			for _, level := range *levels {
				if slices.Contains(domain.ExecutorEffortLevels[ex.Kind], level) {
					defaults.Effort = level
					break
				}
			}
		}
		break
	}
	return defaults
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
	if e.selectSOPExecutionTarget(&ec, true) {
		var best []SOPExecutorRequirement
		var fewestMissing int
		for _, candidate := range e.autoAssignCandidates(ec, false) {
			_, requirements, err := e.planSOPExecutors(wf, candidate.bind(ec), true)
			if err != nil {
				return nil, err
			}
			missing := 0
			for _, req := range requirements {
				if !req.Configured {
					missing++
				}
			}
			if missing == 0 {
				return requirements, nil
			}
			if best == nil || missing < fewestMissing {
				best, fewestMissing = requirements, missing
			}
		}
		if best != nil {
			return best, nil
		}
	}
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
