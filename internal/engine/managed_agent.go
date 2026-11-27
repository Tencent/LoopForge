package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/managedagent"
	"agentflow/internal/runtime"
)

// errManagedTurnUnknown marks a managed Agent attempt whose prompt was already
// sent before the engine lost track of it. Retrying would send the same turn
// again, so the node fails and waits for an explicit retry.
var errManagedTurnUnknown = errors.New("托管 Agent 上一次发送结果未知")

type managedAgentBinding struct {
	provider string
	project  string
	user     int64
}

func managedAgentProvider(node domain.WorkflowNode, role domain.AgentDefinition) string {
	provider := role.ExecutorID
	if provider == "" {
		provider = node.ExecutorID
	}
	if domain.IsManagedAgentExecutor(provider) {
		return provider
	}
	return ""
}

// ConfigureManagedAgent binds a control-plane connection to a project and
// responsible user before the server starts accepting runs.
func (e *Engine) ConfigureManagedAgent(provider, project string, user int64, client managedagent.TextAgent) error {
	if !domain.IsManagedAgentExecutor(provider) || strings.TrimSpace(project) == "" || user <= 0 || client == nil {
		return errors.New("managed Agent binding requires a provider, project, responsible user and client")
	}
	key := managedAgentBinding{provider: provider, project: project, user: user}
	if e.managedAgents == nil {
		e.managedAgents = make(map[managedAgentBinding]managedagent.TextAgent)
	}
	if _, exists := e.managedAgents[key]; exists {
		return fmt.Errorf("duplicate %s Agent binding for project %q and user %d", provider, project, user)
	}
	e.managedAgents[key] = client
	return nil
}

func (e *Engine) managedAgentClient(provider string, ec domain.TelemetryContext) managedagent.TextAgent {
	return e.managedAgents[managedAgentBinding{provider: provider, project: ec.ProjectID, user: ec.ResponsibleUserID}]
}

func (e *Engine) personalAgentRunner(ec domain.TelemetryContext) bool {
	runner, exists := e.repo.Runner(ec.ExecutionRunnerID)
	return exists && ec.ResponsibleUserID > 0 && runner.OwnerUserID == ec.ResponsibleUserID &&
		runner.Status == domain.RunnerStatusOnline && runner.SupportsProvider(domain.A2AExecutorID) &&
		e.repo.CanUseRunner(runner, ec.ProjectID, ec.ResponsibleUserID)
}

// ManagedAgentAvailable reports role-editor availability without disclosing
// the service's endpoint, session, or credentials.
func (e *Engine) ManagedAgentAvailable(projectID string, userID int64, provider string) bool {
	ec := domain.TelemetryContext{ProjectID: projectID, ResponsibleUserID: userID}
	if e.managedAgentClient(provider, ec) != nil {
		return true
	}
	if provider != domain.A2AExecutorID {
		return false
	}
	ec.ExecutionRunnerID = e.repo.ResolveRunnerDefaults(userID, projectID).Runner.ID
	return e.personalAgentRunner(ec)
}

func (e *Engine) executeManagedAgentNode(ctx context.Context, wf domain.WorkflowDefinition, run domain.Run, node domain.WorkflowNode, agent domain.AgentDefinition, provider string, generation, attempt int) error {
	if err := agent.ValidateManagedExecutor(provider); err != nil {
		return err
	}
	if run.ExecutionContext.IsEvolutionReplay() {
		return errors.New("进化回放暂不支持托管 Agent")
	}
	if len(node.ArtifactOutputs) > 0 || normalizeExecutionScope(node.ExecutionScope) == domain.ExecutionScopeRepositories ||
		len(node.CredentialEnv) > 0 {
		return fmt.Errorf("%s 节点 %q 的工作区产物和逐仓库执行需要 CLI", provider, node.Name)
	}
	var runnerID string
	var client managedagent.TextAgent
	if domain.UsesPersonalAgentRunner(provider, agent.ExternalAgentTarget) {
		if !e.personalAgentRunner(run.ExecutionContext) {
			return errors.New("A2A 节点需要责任人的在线个人执行器，且执行器已配置本机 A2A Agent")
		}
		runnerID = run.ExecutionContext.ExecutionRunnerID
	} else {
		client = e.managedAgentClient(provider, run.ExecutionContext)
		if client == nil {
			return fmt.Errorf("当前项目和运行责任人未配置 %s Agent", provider)
		}
	}

	// Fence a non-idempotent turn before dispatch. Recovery never sends the
	// same attempt a second time when the first request's outcome is unknown.
	dispatch := false
	if err := e.updateRunErr(run.ID, func(current *domain.Run) {
		nr := current.Nodes[node.ID]
		if !shouldCommitNodeOutcome(*current, nr, generation, attempt, false) {
			return
		}
		if nr.ManagedPromptAttempt != attempt {
			nr.ManagedPromptAttempt = attempt
			nr.Provider = provider
			dispatch = true
			return
		}
		// The fenced attempt cannot be resumed. Drop the "waiting to resume"
		// marker so an explicit retry starts a new attempt instead of
		// continuing this one and hitting the fence again.
		nr.Activity = "托管 Agent 上一次发送结果未知，确认后可重试"
	}); err != nil {
		return err
	}
	if !dispatch {
		return fmt.Errorf("%w：%s 节点本次执行已发起或终止，请确认原执行结果后重试", errManagedTurnUnknown, provider)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	prompt := strings.ReplaceAll(renderPromptWithStore(e.repo, wf, node, run), "{{artifacts}}", "")
	sink := func(event domain.TelemetryEvent) { e.observe(run.ID, node.ID, event) }
	var output string
	var err error
	if runnerID != "" {
		output, err = e.dispatchExternalAgent(ctx, run.ID, node.ID, runnerID, prompt, attempt, sink)
	} else {
		output, err = client.Run(ctx, prompt, func(text string) {
			sink(domain.TelemetryEvent{Category: "output", Name: "agent.output", Phase: "progress", Message: text, Provider: provider})
		})
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(output) == "" {
		return fmt.Errorf("%s Agent 结束时没有返回文本", provider)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	artifact := newArtifact(run.ID, node.ID, attempt, "result", "result.md", "text/markdown", []byte(output))
	return e.updateRunErr(run.ID, func(current *domain.Run) {
		nr := current.Nodes[node.ID]
		if !shouldCommitNodeOutcome(*current, nr, generation, attempt, true) {
			return
		}
		current.Artifacts = append(current.Artifacts, artifact)
		nr.Output, nr.Error, nr.Status, nr.FinishedAt = output, "", "succeeded", time.Now().UTC()
		e.event(current, node.ID, "node.succeeded", node.Name+" 已完成")
	})
}

func (e *Engine) dispatchExternalAgent(ctx context.Context, runID, nodeID, runnerID, prompt string, attempt int, sink runtime.EventSink) (string, error) {
	key := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", runID, nodeID, attempt)))
	task := domain.RunnerTask{
		ID:       fmt.Sprintf("rt-a2a-%x", key[:16]),
		RunnerID: runnerID, RunID: runID, NodeID: nodeID, Provider: domain.A2AExecutorID,
		Action: domain.RunnerTaskActionExternalAgent, Prompt: prompt, Status: domain.RunnerTaskQueued, MaxAttempts: 1,
	}
	if err := e.repo.PutRunnerTask(task); err != nil {
		return "", err
	}
	e.notifyRunnerDispatch(task.Provider)
	completed, err := e.awaitRunnerTask(ctx, task.ID, sink)
	if err != nil {
		return "", err
	}
	if completed.Status == domain.RunnerTaskSuccess {
		return completed.Result, nil
	}
	if completed.Error != "" {
		// A personal Runner reports a turn whose remote outcome it could not
		// observe with a structured marker. The prompt already reached the
		// remote Agent, so keep the sentinel: the engine must fail this
		// attempt instead of consuming MaxAttempts by resending it.
		if rest, ok := strings.CutPrefix(completed.Error, domain.RunnerResultTurnOutcomeUnknown+"："); ok {
			return "", fmt.Errorf("%w：%s", managedagent.ErrTurnTimedOut, rest)
		}
		return "", errors.New(completed.Error)
	}
	return "", fmt.Errorf("个人执行器 A2A 任务结束于 %s", completed.Status)
}
