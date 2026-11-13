package runner

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// A workspace request is authenticated by the control plane, which supplies
// the run's frozen sandbox/workflow; browser-supplied shell definitions never
// cross this boundary. Refuse old incomplete requests rather than falling back
// to a shell with the runner daemon's host environment.
func (d *Daemon) workspaceSandboxTask(root string, request domain.RunnerWorkspaceRequest) (domain.RunnerTask, error) {
	if request.WorkspaceLayout != domain.RunnerWorkspaceLayoutShared {
		return domain.RunnerTask{}, fmt.Errorf("legacy per-node workspace layout does not support environment preparation or sandbox terminals; file browsing remains available")
	}
	if request.Sandbox == nil || request.Workflow == nil {
		return domain.RunnerTask{}, fmt.Errorf("frozen run sandbox and workflow are required; upgrade the control plane before opening this workspace")
	}
	relative, err := filepath.Rel(d.cfg.DefaultWorkspaceRoot, root)
	if err != nil {
		return domain.RunnerTask{}, err
	}
	return domain.RunnerTask{ID: "workspace-" + request.RunID, RunID: request.RunID, Provider: "terminal", Workspace: filepath.ToSlash(relative), Sandbox: request.Sandbox}, nil
}

func onceRelease(callbacks ...func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, callback := range callbacks {
				if callback != nil {
					callback()
				}
			}
		})
	}
}

const runnerWorkspacePreparationTimeout = 15 * time.Minute

func (d *Daemon) prepareRunnerWorkspace(ctx context.Context, root string, request domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse {
	ctx, cancel := context.WithTimeout(ctx, runnerWorkspacePreparationTimeout)
	defer cancel()
	task, err := d.workspaceSandboxTask(root, request)
	if err != nil {
		return runnerWorkspaceError(err)
	}
	gate := d.requestGate(request)
	key := requestGateKey(request)
	if !gate.TryLock() {
		// Another task of this wave (a parallel SOP branch, a group role, a
		// child SOP in the shared checkout) holds the read fence. Its exact
		// live environment already carries the stamp this recipe needs; reuse
		// it instead of rerunning scripts under running members.
		if gate.TryRLock() {
			state := d.runnerWorkspacePreparationStatus(ctx, root, request)
			gate.RUnlock()
			if state.Status == "ready" {
				return runnerWorkspacePayload(state)
			}
		}
		if _, preparing := d.workspacePreparing.Load(key); !preparing && (d.workspaceTerminals.HasRunScope(request.RunID) || key != request.RunID && d.workspaceTerminals.HasRunScope(key)) {
			return workspaceTerminalActiveResponse("workspace is in use: a workspace terminal is still open; close it before continuing")
		}
		return workspaceBusyResponse("workspace is in use; preparation waits for active execution or terminal to finish")
	}
	defer gate.Unlock()
	d.workspacePreparing.Store(key, true)
	defer d.workspacePreparing.Delete(key)
	if err := os.MkdirAll(root, 0700); err != nil {
		return runnerWorkspaceError(err)
	}
	if err := checkoutInvocationRepositories(ctx, root, request.PreparationInvocation, runnerTaskSandboxDriver(task), request.RefreshCleanRepositories); err != nil {
		return runnerWorkspaceError(fmt.Errorf("prepare workspace repositories: %w", err))
	}
	sb, release, err := d.acquireTaskSandbox(ctx, task)
	if err != nil {
		return runnerWorkspaceError(err)
	}
	defer release()
	execution, err := d.workspaceExecutionSandbox(ctx, sb, request)
	if err != nil {
		return runnerWorkspaceError(err)
	}
	status, err := runtime.EnsureWorkspacePreparation(ctx, execution, *request.Workflow, request.Repositories, nil)
	if err != nil {
		return domain.RunnerWorkspaceResponse{Status: 409, Error: err.Error()}
	}
	return runnerWorkspacePayload(status)
}

func (d *Daemon) handleRunnerWorkspaceTerminal(ctx context.Context, root, scope string, request domain.RunnerWorkspaceRequest, concurrent bool) (runtime.TerminalResult, error) {
	if request.Terminal.Operation != "open" {
		return d.workspaceTerminals.HandleSandbox(ctx, nil, scope, *request.Terminal, nil)
	}
	if !request.Terminal.ExpiresAt.IsZero() && !request.Terminal.ExpiresAt.After(time.Now()) {
		return runtime.TerminalResult{}, fmt.Errorf("terminal authorization has expired")
	}
	ctx, cancel := context.WithTimeout(ctx, runnerWorkspacePreparationTimeout)
	defer cancel()
	task, err := d.workspaceSandboxTask(root, request)
	if err != nil {
		return runtime.TerminalResult{}, err
	}
	gate := d.requestGate(request)
	key := requestGateKey(request)
	if !gate.TryLock() {
		if concurrent {
			return d.openBusyGroupTerminal(ctx, root, scope, request, task)
		}
		return runtime.TerminalResult{}, newWorkspaceBusyError("workspace is in use; finish active execution before preparing the terminal")
	}
	unlock := onceRelease(gate.Unlock)
	d.workspacePreparing.Store(key, true)
	defer d.workspacePreparing.Delete(key)
	if err := checkoutInvocationRepositories(ctx, root, request.PreparationInvocation, runnerTaskSandboxDriver(task)); err != nil {
		unlock()
		return runtime.TerminalResult{}, fmt.Errorf("prepare workspace repositories: %w", err)
	}
	sb, releaseSandbox, err := d.acquireTaskSandbox(ctx, task)
	if err != nil {
		unlock()
		return runtime.TerminalResult{}, err
	}
	release := onceRelease(releaseSandbox, unlock)
	execution, err := d.workspaceExecutionSandbox(ctx, sb, request)
	if err != nil {
		release()
		return runtime.TerminalResult{}, err
	}
	if _, err = runtime.EnsureWorkspacePreparation(ctx, execution, *request.Workflow, request.Repositories, nil); err != nil {
		release()
		return runtime.TerminalResult{}, err
	}
	if concurrent {
		// Group members may write alongside the user, but another preparation or
		// restore must not invalidate the terminal's live environment.
		unlock()
		gate.RLock()
		release = onceRelease(releaseSandbox, gate.RUnlock)
	}
	result, err := d.workspaceTerminals.HandleSandboxWithGitSSH(ctx, execution, scope, *request.Terminal, release, request.TerminalGitSSHKeys)
	if err != nil {
		release()
	}
	return result, err
}

// Preparation uses the same closed Git/MCP credential bindings as a workflow
// script. The transient wrapper is never closed: the shared base is owned by the
// run sandbox registry and the acquisition reference held by the caller.
func (d *Daemon) workspaceExecutionSandbox(ctx context.Context, sb runtime.Sandbox, request domain.RunnerWorkspaceRequest) (runtime.Sandbox, error) {
	credentials := runtime.RunCredentials{}
	inv := request.PreparationInvocation
	if inv != nil {
		credentials.GitToken = invocationGitToken(inv)
		credentials.GitLab = invocationGitLab(inv)
		credentials.GitSSHKeys = inv.GitSSHKeys
		credentials.GitUserName = inv.GitUserName
		credentials.GitUserEmail = inv.GitUserEmail
		servers := inv.MCPCallServers
		if len(servers) == 0 {
			servers = inv.Assets.MCPServers
		}
		assets, _, err := runtime.PrepareMCPServers(domain.AgentAssetBundle{MCPServers: servers, MCPFiles: inv.Assets.MCPFiles}, d.mcpTarget(false))
		if err != nil {
			return nil, fmt.Errorf("prepare workspace MCP: %w", err)
		}
		// The terminal only offers MCP as optional tools. An MCP whose install
		// script needs an isolated sandbox is left out instead of making the
		// whole workspace unusable in a local sandbox.
		assets, skipped := runtime.WithoutUnprovisionableMCP(sb, assets)
		if len(skipped) > 0 {
			log.Printf("workspace %s: skipped MCP %s (install script needs docker, bwrap or seatbelt)", request.RunID, strings.Join(skipped, ", "))
		}
		if err = runtime.ProvisionMCPAssets(ctx, sb, sb.Workspace(), assets, nil); err != nil {
			return nil, fmt.Errorf("provision workspace MCP: %w", err)
		}
		credentials.MCPServers = assets.MCPServers
	}
	return runtime.WithSandboxCredentials(sb, credentials), nil
}

// Reading readiness never creates a sandbox or hashes source files that group
// members may be changing. A successful prior check is reusable within the
// current wave only while its exact sandbox generation is still alive.
func (d *Daemon) runnerWorkspacePreparationStatus(ctx context.Context, root string, request domain.RunnerWorkspaceRequest) runtime.WorkspacePreparationStatus {
	state := runtime.WorkspacePreparationStatus{Status: "unprepared", Steps: []runtime.WorkspacePreparationStep{}}
	task, err := d.workspaceSandboxTask(root, request)
	if err != nil {
		state.Error = err.Error()
		return state
	}
	sb, release := d.acquireExistingTaskSandbox(task)
	if sb == nil {
		state.Error = "工作区执行环境需要重新准备"
		return state
	}
	defer release()
	state = runtime.ReadWorkspacePreparationStatus(root, sb.ID())
	if _, active := d.workspacePreparing.Load(requestGateKey(request)); state.Status == "preparing" && !active {
		state.Status = "unprepared"
		state.Error = "上次工作区准备已中断，请重新准备"
	}
	if state.Status != "ready" {
		if state.Status != "preparing" && request.Workflow != nil && runtime.WorkspacePreparationTrivial(*request.Workflow) {
			return runtime.WorkspacePreparationStatus{Status: "ready", Steps: []runtime.WorkspacePreparationStep{}}
		}
		return state
	}
	epoch, err := runtime.ReadSandboxEnvironmentIdentity(ctx, sb)
	if err != nil || !runtime.WorkspacePreparationReusable(state, *request.Workflow, request.Repositories, epoch) {
		state.Status = "unprepared"
		state.Error = "工作区执行环境或准备配置已变化，需要重新准备"
	}
	return state
}

// A group terminal joins an existing prepared wave. It must not rerun a recipe,
// rehash source while members write it, or create a replacement environment.
func (d *Daemon) openBusyGroupTerminal(ctx context.Context, root, scope string, request domain.RunnerWorkspaceRequest, task domain.RunnerTask) (runtime.TerminalResult, error) {
	gate := d.requestGate(request)
	if !gate.TryRLock() {
		return runtime.TerminalResult{}, newWorkspaceBusyError("workspace preparation is in progress; retry after it finishes")
	}
	sb, releaseSandbox := d.acquireExistingTaskSandbox(task)
	if sb == nil {
		gate.RUnlock()
		return runtime.TerminalResult{}, fmt.Errorf("workspace environment is not prepared")
	}
	release := onceRelease(releaseSandbox, gate.RUnlock)
	state := runtime.ReadWorkspacePreparationStatus(root, sb.ID())
	epoch, err := runtime.ReadSandboxEnvironmentIdentity(ctx, sb)
	if !runtime.WorkspacePreparationTrivial(*request.Workflow) && (err != nil || !runtime.WorkspacePreparationReusable(state, *request.Workflow, request.Repositories, epoch)) {
		release()
		return runtime.TerminalResult{}, fmt.Errorf("workspace environment changed; wait until active execution finishes before preparing")
	}
	credentials := runtime.RunCredentials{}
	if inv := request.PreparationInvocation; inv != nil {
		credentials.GitToken = invocationGitToken(inv)
		credentials.GitLab = invocationGitLab(inv)
		credentials.GitUserName = inv.GitUserName
		credentials.GitUserEmail = inv.GitUserEmail
	}
	result, err := d.workspaceTerminals.HandleSandboxWithGitSSH(ctx, runtime.WithSandboxCredentials(sb, credentials), scope, *request.Terminal, release, request.TerminalGitSSHKeys)
	if err != nil {
		release()
	}
	return result, err
}

// A normal SOP script already ran in its graph position. Recording only stamps
// that exact live environment; it must never create a new one and mark it ready.
func (d *Daemon) recordRunnerWorkspacePreparation(ctx context.Context, root string, request domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse {
	task, err := d.workspaceSandboxTask(root, request)
	if err != nil {
		return runnerWorkspaceError(err)
	}
	gate := d.requestGate(request)
	if !gate.TryRLock() {
		return workspaceBusyResponse("workspace preparation is in progress")
	}
	defer gate.RUnlock()
	sb, release := d.acquireExistingTaskSandbox(task)
	if sb == nil {
		return domain.RunnerWorkspaceResponse{Status: 409, Error: "the script execution environment is no longer available; cannot record it as prepared"}
	}
	defer release()
	output := request.PreparationOutput
	if len(output) > 32<<10 {
		output = output[:32<<10]
	}
	// Multiple successful group scripts may report at once. Serialize only the
	// stamp merge while retaining a shared workspace read fence; a finishing task
	// may still own its read fence and must not be mistaken for a prep conflict.
	value, _ := d.workspacePreparation.LoadOrStore(requestGateKey(request), &sync.Mutex{})
	recordLock := value.(*sync.Mutex)
	recordLock.Lock()
	defer recordLock.Unlock()
	state, err := runtime.RecordWorkspacePreparation(ctx, sb, *request.Workflow, request.Repositories, request.PreparationNodeID, output)
	if err != nil {
		return domain.RunnerWorkspaceResponse{Status: 409, Error: err.Error()}
	}
	return runnerWorkspacePayload(state)
}
