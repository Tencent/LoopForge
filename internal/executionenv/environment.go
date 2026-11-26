// Package executionenv defines the boundary for acquiring execution resources.
// A workspace can be shared by several environments (for example nested SOPs).
package executionenv

import (
	"context"
	"fmt"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
)

// Identity names the run sandbox and its workspace independently. Generation
// identifies the scheduler lease that requested the environment; it is not a
// resource lease and does not by itself fence operations on another machine.
type Identity struct {
	EnvironmentID string `json:"environmentId"`
	WorkspaceID   string `json:"workspaceId"`
	Generation    int    `json:"generation"`
}

type AcquireRequest struct {
	Identity     Identity
	Profile      domain.SandboxProfile
	Requirements runtime.SandboxRequirements
	Credentials  runtime.RunCredentials
	Resources    runtime.SandboxResources
}

// LocalBackend is the portion of environment management used for locally
// hosted execution and the control plane's Runner staging workspace. The
// returned handle is process-local; a remote implementation needs its own
// durable resource lease and must not assume this handle can cross a process.
type LocalBackend interface {
	Acquire(context.Context, AcquireRequest) (runtime.Sandbox, error)
	WorkspaceExists(workspaceID string) (bool, error)
	Destroy(environmentID string) error
}

// Manager is the host side capability set used by the engine for the
// transitional local adapter. Keeping this contract in executionenv is
// intentional: engine code can be compiled against a remote implementation
// without importing runtime.Manager or knowing how Docker/workspace cleanup is
// performed. The extra reclamation methods are control-plane maintenance
// hooks; they are never part of SOP node orchestration.
type Manager interface {
	LocalBackend
	Root() string
	RunWorkspace(runID string) string
	DiscardRunContainer(context.Context, string)
	RunsWithIdleDockerSandbox(context.Context, []string) ([]string, error)
	ReclaimDockerSandboxes(context.Context, []string) (int, error)
	ReclaimCachedDockerImages(context.Context, []domain.DockerfileDefinition) (int, error)
	ReclaimExcept([]string) (int, error)
	DockerImageCached(context.Context, domain.DockerfileDefinition) bool
	LiveDockerSandbox(string) bool
}

// RuntimeAdapter preserves the existing local sandbox and workspace behavior.
type RuntimeAdapter struct {
	manager *runtime.Manager
}

var _ Manager = (*RuntimeAdapter)(nil)

func NewRuntimeAdapter(manager *runtime.Manager) *RuntimeAdapter {
	return &RuntimeAdapter{manager: manager}
}

func (a *RuntimeAdapter) Acquire(ctx context.Context, request AcquireRequest) (runtime.Sandbox, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("local execution environment manager is not configured")
	}
	if strings.TrimSpace(request.Identity.EnvironmentID) == "" || strings.TrimSpace(request.Identity.WorkspaceID) == "" {
		return nil, fmt.Errorf("environment and workspace IDs are required")
	}
	return a.manager.AcquireRunIn(ctx, request.Identity.EnvironmentID, request.Identity.WorkspaceID, request.Profile, request.Requirements, request.Credentials, request.Resources)
}

func (a *RuntimeAdapter) WorkspaceExists(workspaceID string) (bool, error) {
	if a == nil || a.manager == nil || strings.TrimSpace(workspaceID) == "" {
		return false, fmt.Errorf("local execution manager and workspace ID are required")
	}
	return a.manager.RunWorkspaceExists(workspaceID)
}

func (a *RuntimeAdapter) Destroy(environmentID string) error {
	if a == nil || a.manager == nil || strings.TrimSpace(environmentID) == "" {
		return fmt.Errorf("local execution manager and environment ID are required")
	}
	return a.manager.DestroyRun(environmentID)
}

func (a *RuntimeAdapter) Root() string {
	if a == nil || a.manager == nil {
		return ""
	}
	return a.manager.Root()
}
func (a *RuntimeAdapter) RunWorkspace(id string) string {
	if a == nil || a.manager == nil {
		return ""
	}
	return a.manager.RunWorkspace(id)
}
func (a *RuntimeAdapter) DiscardRunContainer(ctx context.Context, id string) {
	if a != nil && a.manager != nil {
		a.manager.DiscardRunContainer(ctx, id)
	}
}
func (a *RuntimeAdapter) RunsWithIdleDockerSandbox(ctx context.Context, ids []string) ([]string, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("local execution environment manager is not configured")
	}
	return a.manager.RunsWithIdleDockerSandbox(ctx, ids)
}
func (a *RuntimeAdapter) ReclaimDockerSandboxes(ctx context.Context, ids []string) (int, error) {
	if a == nil || a.manager == nil {
		return 0, fmt.Errorf("local execution environment manager is not configured")
	}
	return a.manager.ReclaimDockerSandboxes(ctx, ids)
}
func (a *RuntimeAdapter) ReclaimCachedDockerImages(ctx context.Context, dfs []domain.DockerfileDefinition) (int, error) {
	if a == nil || a.manager == nil {
		return 0, fmt.Errorf("local execution environment manager is not configured")
	}
	return a.manager.ReclaimCachedDockerImages(ctx, dfs)
}
func (a *RuntimeAdapter) ReclaimExcept(ids []string) (int, error) {
	if a == nil || a.manager == nil {
		return 0, fmt.Errorf("local execution environment manager is not configured")
	}
	return a.manager.ReclaimExcept(ids)
}

func (a *RuntimeAdapter) DockerImageCached(ctx context.Context, df domain.DockerfileDefinition) bool {
	return a != nil && a.manager != nil && runtime.DockerImageCached(ctx, df)
}

func (a *RuntimeAdapter) LiveDockerSandbox(workspace string) bool {
	return a != nil && a.manager != nil && a.manager.LiveDockerSandbox(workspace)
}
