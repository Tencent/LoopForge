package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// runEnvironment owns the single sandbox/workspace assigned to a workflow run.
// SOP nodes serialize complete executions. Group tasks hold nodeMu only while
// preparing the shared environment and use independent invocation configuration.
type runEnvironment struct {
	sandbox              runtime.Sandbox
	nodeMu               sync.Mutex
	credentials          runtime.RunCredentials
	credentialsMu        sync.RWMutex
	repositoriesPrepared bool
	preparing            atomic.Bool
	activeExecutions     atomic.Int64

	outputsMu sync.Mutex
	// reported holds, per node, what the execution machine reports when it
	// supports RunnerTask.Outputs; absent nodes use the sandbox workspace.
	reported map[string]*reportedOutputs
}

func (r *runEnvironment) beginReportedOutputs(nodeID string, inputs []domain.RunnerInputFile) *reportedOutputs {
	r.outputsMu.Lock()
	defer r.outputsMu.Unlock()
	if r.reported == nil {
		r.reported = map[string]*reportedOutputs{}
	}
	outputs := newReportedOutputs(inputs)
	r.reported[nodeID] = outputs
	return outputs
}

func (r *runEnvironment) reportedOutputs(nodeID string) *reportedOutputs {
	r.outputsMu.Lock()
	defer r.outputsMu.Unlock()
	return r.reported[nodeID]
}

func (r *runEnvironment) endReportedOutputs(nodeID string) {
	r.outputsMu.Lock()
	defer r.outputsMu.Unlock()
	delete(r.reported, nodeID)
}

// prepareRepositoriesOnce keeps repository checkout and Agent-configuration
// index isolation at the run-environment lifecycle rather than the node
// lifecycle. nodeMu serializes callers, and a reconstructed environment starts
// with repositoriesPrepared=false so process restarts and lease takeovers
// still revalidate the persisted workspace once before executing a node.
func (r *runEnvironment) prepareRepositoriesOnce(prepare func() error) error {
	if r.repositoriesPrepared {
		return nil
	}
	if err := prepare(); err != nil {
		return err
	}
	r.repositoriesPrepared = true
	return nil
}

func (r *runEnvironment) ID() string        { return r.sandbox.ID() }
func (r *runEnvironment) Workspace() string { return r.sandbox.Workspace() }
func (r *runEnvironment) Driver() string {
	if value, ok := r.sandbox.(interface{ Driver() string }); ok {
		return value.Driver()
	}
	return ""
}
func (r *runEnvironment) Exec(ctx context.Context, command runtime.CommandSpec, sink runtime.EventSink) (string, error) {
	return r.sandbox.Exec(ctx, command, sink)
}
func (r *runEnvironment) Recreate(ctx context.Context) error {
	if rec, ok := r.sandbox.(interface{ Recreate(context.Context) error }); ok {
		return rec.Recreate(ctx)
	}
	return nil
}

func (r *runEnvironment) BindWorkspace(ctx context.Context) error {
	if bind, ok := r.sandbox.(interface{ BindWorkspace(context.Context) error }); ok {
		return bind.BindWorkspace(ctx)
	}
	return r.Recreate(ctx)
}
func (r *runEnvironment) Close(ctx context.Context) error { return r.sandbox.Close(ctx) }

func (r *runEnvironment) Detach() error {
	if detach, ok := r.sandbox.(interface{ Detach() error }); ok {
		return detach.Detach()
	}
	return nil
}

func resolveSandboxResources(st *repo.Store, profile domain.SandboxProfile) (runtime.SandboxResources, error) {
	var resources runtime.SandboxResources
	if strings.TrimSpace(profile.DockerfileID) != "" {
		dockerfile, ok := st.Dockerfile(profile.DockerfileID)
		if !ok {
			return resources, fmt.Errorf("dockerfile %q not found", profile.DockerfileID)
		}
		resources.Dockerfile = dockerfile
	} else if strings.EqualFold(strings.TrimSpace(profile.Driver), "docker") {
		return resources, fmt.Errorf("docker sandbox %q requires a dockerfileId", profile.ID)
	}
	if strings.TrimSpace(profile.EnvironmentID) != "" {
		if environment, ok := st.Environment(profile.EnvironmentID); ok {
			resources.Environment = environment
		}
	}
	return resources, nil
}

func resolvedSandboxSnapshot(profile domain.SandboxProfile, resources runtime.SandboxResources) *domain.RunnerSandboxSpec {
	snapshot := &domain.RunnerSandboxSpec{Profile: profile}
	if strings.TrimSpace(resources.Dockerfile.ID) != "" {
		dockerfile := resources.Dockerfile
		snapshot.Dockerfile = &dockerfile
	}
	if strings.TrimSpace(resources.Environment.ID) != "" {
		environment := resources.Environment
		snapshot.Environment = &environment
	}
	return snapshot
}

func sandboxResourcesFromSnapshot(snapshot *domain.RunnerSandboxSpec) runtime.SandboxResources {
	var resources runtime.SandboxResources
	if snapshot == nil {
		return resources
	}
	if snapshot.Dockerfile != nil {
		resources.Dockerfile = *snapshot.Dockerfile
	}
	if snapshot.Environment != nil {
		resources.Environment = *snapshot.Environment
	}
	return resources
}

func (e *Engine) acquireRunEnvironment(ctx context.Context, run domain.Run, profile domain.SandboxProfile, requirements runtime.SandboxRequirements) (*runEnvironment, bool, error) {
	needsRestore := !run.WorkspaceCleanedAt.IsZero()
	if !needsRestore && e.defaultExecutionRunner(run) == "" {
		if _, saved := e.repo.WorkspaceCheckpoint(run.ID); saved {
			_, err := os.Stat(e.sandboxes.RunWorkspace(run.ID))
			needsRestore = os.IsNotExist(err)
		}
	}
	if needsRestore {
		if err := e.restoreCheckpointBeforeExecution(ctx, run); err != nil {
			return nil, false, err
		}
	}
	current, ok := e.repo.RunState(run.ID)
	if !ok || current.Generation != run.Generation || !e.ownsRun(current) {
		return nil, false, repo.ErrRunLeaseLost
	}
	if err := e.adoptForeignWorkspace(current); err != nil {
		return nil, false, err
	}
	e.runEnvMu.Lock()
	defer e.runEnvMu.Unlock()
	// Credentials are resolved from the responsible user on every entry. The
	// sandbox directory stays; only the in-memory handle is refreshed. A retry
	// otherwise keeps the token captured at the first acquire, including an
	// empty token from a run that had no responsible user yet.
	credentials := e.resolveRunCredentials(current)
	if existing := e.runEnvs[run.ID]; existing != nil {
		existing.setCredentials(credentials)
		return existing, false, nil
	}
	resources := sandboxResourcesFromSnapshot(run.WorkflowSnapshot.SandboxSnapshot)
	if run.WorkflowSnapshot.SandboxSnapshot == nil {
		var err error
		resources, err = resolveSandboxResources(e.repo, profile)
		if err != nil {
			return nil, false, err
		}
	}
	sandbox, err := e.sandboxes.AcquireRun(ctx, run.ID, profile, requirements, credentials, resources)
	if err != nil {
		return nil, false, err
	}
	env := &runEnvironment{
		sandbox:     sandbox,
		credentials: credentials,
	}
	e.runEnvs[run.ID] = env
	first, err := e.repo.BindRunWorkspace(run.ID, e.instanceID, run.Generation, e.workspaceHost, e.sandboxes.Root())
	if err != nil {
		delete(e.runEnvs, run.ID)
		_ = env.Close(context.Background())
		return nil, false, err
	}
	return env, first, nil
}

func (e *Engine) resolveRunCredentials(run domain.Run) runtime.RunCredentials {
	userID := run.ExecutionContext.ResponsibleUserID
	credentials := runtime.RunCredentials{
		CodeBuddy: resolveCodeBuddyCredential(e.repo, userID),
		GitToken:  resolveGitToken(e.repo, userID),
	}
	identity := resolveGitIdentity(e.repo, userID)
	credentials.GitUserName = identity.name
	credentials.GitUserEmail = identity.email
	if user, ok := e.repo.User(userID); ok {
		credentials.TaihuPAT = user.TaihuPAT
		credentials.Codex = runtime.ProviderCredential{APIKey: user.CodexAPIKey, BaseURL: user.CodexBaseURL}
		credentials.ClaudeCode = runtime.ProviderCredential{APIKey: user.ClaudeCodeAPIKey, BaseURL: user.ClaudeCodeBaseURL}
	}
	scriptAssets, _, _ := runtime.PrepareMCPServers(e.resolveScriptMCPAssets(run.ExecutionContext.ProjectID, userID), runtime.MCPTarget{TaihuPAT: credentials.TaihuPAT, GitToken: credentials.GitToken})
	credentials.MCPServers = scriptAssets.MCPServers
	return credentials
}

func (env *runEnvironment) credentialSnapshot() runtime.RunCredentials {
	env.credentialsMu.RLock()
	defer env.credentialsMu.RUnlock()
	return env.credentials
}

func (env *runEnvironment) setCredentials(credentials runtime.RunCredentials) {
	env.credentialsMu.Lock()
	env.credentials = credentials
	env.credentialsMu.Unlock()
	if sink, ok := env.sandbox.(interface {
		SetCredentials(runtime.RunCredentials)
	}); ok {
		sink.SetCredentials(credentials)
	}
}

// adoptForeignWorkspace moves the workspace bind of a run whose bound host is
// gone to this instance. Whatever this host still has under the run ID is a
// leftover of an earlier stay and is discarded, so the checkout is rebuilt
// from the remote work branch.
func (e *Engine) adoptForeignWorkspace(run domain.Run) error {
	if !e.foreignWorkspaceBind(run) {
		return nil
	}
	if e.workspaceBlocked(run) {
		return repo.ErrRunWorkspaceBound
	}
	if err := e.releaseRunEnvironment(run.ID, true); err != nil {
		return fmt.Errorf("discard stale local workspace: %w", err)
	}
	if err := e.repo.RebindRunWorkspace(run.ID, e.instanceID, run.Generation, e.workspaceHost, e.sandboxes.Root()); err != nil {
		return err
	}
	message := fmt.Sprintf("工作区原绑定 %s:%s 已离线，改由 %s 从远程工作分支重建工作区", run.WorkspaceHost, run.WorkspaceRoot, e.workspaceHost)
	e.updateRun(run.ID, func(r *domain.Run) {
		r.WorkspaceHost, r.WorkspaceRoot = e.workspaceHost, e.sandboxes.Root()
		e.event(r, "", "workspace.rebound", message)
	})
	return nil
}

func (e *Engine) releaseRunEnvironment(runID string, destroy bool) error {
	e.runEnvMu.Lock()
	env := e.runEnvs[runID]
	delete(e.runEnvs, runID)
	if destroy {
		delete(e.workspaceFacts, runID)
	}
	e.runEnvMu.Unlock()

	var closeErr error
	if env != nil {
		closeErr = env.Close(context.Background())
	}
	if destroy {
		if err := e.sandboxes.DestroyRun(runID); err != nil {
			if closeErr != nil {
				return fmt.Errorf("close run sandbox: %v; destroy workspace: %w", closeErr, err)
			}
			return err
		}
	}
	return closeErr
}

func (e *Engine) detachRunEnvironment(runID string) {
	e.runEnvMu.Lock()
	env := e.runEnvs[runID]
	delete(e.runEnvs, runID)
	e.runEnvMu.Unlock()
	if env != nil {
		_ = env.Detach()
	}
}

func (e *Engine) detachAllRunEnvironments() {
	e.runEnvMu.Lock()
	ids := make([]string, 0, len(e.runEnvs))
	for id := range e.runEnvs {
		ids = append(ids, id)
	}
	e.runEnvMu.Unlock()
	for _, id := range ids {
		e.detachRunEnvironment(id)
	}
}

// LocalRunWorkspace resolves a control-plane workspace only when this process
// can read the authoritative checkout. Runner-owned workspaces use the
// bounded read-only channel maintained by the HTTP layer.
func (e *Engine) LocalRunWorkspace(run domain.Run) (string, string) {
	if e.defaultExecutionRunner(run) != "" {
		return "", "工作区位于远程执行机器，当前版本暂不支持远程浏览"
	}
	if e.foreignWorkspaceBind(run) {
		return "", "工作区绑定在其他 AgentFlow 实例"
	}
	workspace := e.sandboxes.RunWorkspace(run.ID)
	if strings.TrimSpace(workspace) == "" {
		return "", "当前实例未配置工作区目录"
	}
	return workspace, ""
}

// RunExecutionRunner returns the execution machine frozen into the run.
// Empty means that the authoritative workspace belongs to the control plane.
func (e *Engine) RunExecutionRunner(run domain.Run) string {
	return e.defaultExecutionRunner(run)
}
