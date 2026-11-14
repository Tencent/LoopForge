package runner

import (
	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
	"agentflow/internal/runtime"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var conversationKeyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (d *Daemon) conversationProcess(ctx context.Context, request domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse {
	var req runtime.ConversationProcessRequest
	if err := json.Unmarshal(request.Conversation, &req); err != nil || !conversationKeyPattern.MatchString(req.SessionKey) {
		return domain.RunnerWorkspaceResponse{Status: 400, Error: "invalid conversation process request"}
	}
	root := filepath.Join(d.cfg.DefaultWorkspaceRoot, ".conversations", req.SessionKey)
	workspace := ""
	if request.RunID != "" {
		var err error
		workspace, err = d.runnerWorkspaceRoot(request)
		if err != nil {
			return runnerWorkspaceError(err)
		}
		if request.Sandbox == nil || request.Sandbox.Profile.Driver != "docker" {
			return domain.RunnerWorkspaceResponse{Status: 400, Error: "对话工作区仅支持 Docker"}
		}
	}
	d.conversationMu.Lock()
	forward := d.conversationForward
	d.conversationMu.Unlock()
	if forward == nil {
		return runnerWorkspaceError(fmt.Errorf("conversation control channel unavailable"))
	}
	var factory runtime.ConversationSandboxFactory
	if request.RunID == "" {
		factory = d.hostConversationFactory(req.Command)
	} else {
		factory = d.workspaceConversationFactory(request, workspace)
	}
	result, err := d.conversationProcesses.Handle(ctx, root, workspace, request.Sandbox, req, forward, factory)
	if err != nil {
		return runnerWorkspaceError(err)
	}
	return runnerWorkspacePayload(result)
}

// hostConversationFactory runs the conversation CLI directly on the runner
// host: it holds a managed CLI claim lease for the process lifetime and
// rewrites argv[0] to the resolved provider binary.
func (d *Daemon) hostConversationFactory(command *runtime.ConversationCommand) runtime.ConversationSandboxFactory {
	return func(envCtx context.Context, _ string) (runtime.Sandbox, func(), error) {
		var lease *managedcli.ClaimLease
		var err error
		var inventory managedcli.Inventory
		if d.cfg.ManagedCLIRoot != "" {
			lease, err = managedcli.BeginClaim(envCtx, d.cfg.ManagedCLIRoot)
			if err == nil {
				inventory, err = managedcli.ReadInventory(d.cfg.ManagedCLIRoot)
			}
			if err != nil {
				_ = lease.Close()
				return nil, nil, err
			}
		}
		release := func() { _ = lease.Close() }
		provider := command.Provider
		binary := d.conversationCLIBinary(provider, inventory)
		if binary == "" {
			release()
			return nil, nil, fmt.Errorf("执行机器未安装 CLI %s", provider)
		}
		command.Argv[0] = binary
		return nil, release, nil
	}
}

// conversationCLIBinary resolves the host CLI for a conversation: an explicit
// runner binary wins, then the managed inventory, the detected runtimes and
// finally a PATH lookup of the provider's default binary name.
func (d *Daemon) conversationCLIBinary(provider string, inventory managedcli.Inventory) string {
	binary := strings.TrimSpace(d.cfg.ExtraBinaries[provider])
	if binary == "" {
		binary, _ = inventory.BinaryFor(provider)
	}
	if binary == "" {
		binary, _ = providerBinaryPath(d.currentRuntimes(), provider)
	}
	if binary == "" {
		binary, _ = lookupCLI(defaultProviderBinary(provider))
	}
	return binary
}

// workspaceConversationFactory runs the conversation inside the run's Docker
// workspace sandbox, preparing the workspace under the writer gate and then
// downgrading to a reader lock for the conversation's lifetime.
func (d *Daemon) workspaceConversationFactory(request domain.RunnerWorkspaceRequest, workspace string) runtime.ConversationSandboxFactory {
	return func(envCtx context.Context, nativeRoot string) (runtime.Sandbox, func(), error) {
		gate := d.requestGate(request)
		if err := lockConversationWorkspace(envCtx, gate); err != nil {
			return nil, nil, err
		}
		task, err := d.workspaceSandboxTask(workspace, request)
		if err != nil {
			gate.Unlock()
			return nil, nil, err
		}
		sb, release, err := d.acquireTaskSandbox(envCtx, task)
		if err != nil {
			gate.Unlock()
			return nil, nil, err
		}
		if err = runtime.ConfigureConversationSandbox(envCtx, sb, nativeRoot); err == nil {
			var execution runtime.Sandbox
			execution, err = d.workspaceExecutionSandbox(envCtx, sb, request)
			if err == nil {
				_, err = runtime.EnsureWorkspacePreparation(envCtx, execution, *request.Workflow, request.Repositories, nil)
			}
		}
		if err != nil {
			release()
			gate.Unlock()
			return nil, nil, err
		}
		// A running conversation excludes recreation/checkpoints (writers),
		// while its own delivery task may share the sandbox as a reader.
		gate.Unlock()
		gate.RLock()
		finish := onceRelease(release, gate.RUnlock)
		return sb, finish, nil
	}
}

// A cancelled background export may have been queued when the API preempted
// it. Wait for that gate here, before any CLI is created; never replay start.
func lockConversationWorkspace(ctx context.Context, gate *sync.RWMutex) error {
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if gate.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return newWorkspaceBusyError("workspace is in use")
		case <-ticker.C:
		}
	}
}
