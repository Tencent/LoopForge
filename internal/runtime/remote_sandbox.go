package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"

	"agentflow/internal/domain"
)

// OpenRemoteSandbox creates a sandbox at an existing runner task workspace.
// Unlike Manager.AcquireRun it does not choose a control-plane workspace:
// the caller owns the exact path and lifecycle on the execution machine.
func OpenRemoteSandbox(ctx context.Context, id, workspace string, profile domain.SandboxProfile, requirements SandboxRequirements, resources SandboxResources) (Sandbox, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, fmt.Errorf("runner sandbox workspace is required")
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return nil, err
	}
	activated, err := prepareSandboxEnvironment(workspace, profile, resources.Environment)
	if err != nil {
		return nil, err
	}
	if len(activated.Overlay) > 0 {
		path := activated.Overlay["PATH"]
		profile.Environment = mergeEnvironmentMaps(activated.Overlay, profile.Environment)
		if path != "" {
			profile.Environment["PATH"] = path
		}
	}
	var sandbox Sandbox
	switch strings.TrimSpace(profile.Driver) {
	case "", "local":
		sandbox = &localSandbox{id: id, workspace: workspace, environment: profile.Environment}
	case "seatbelt":
		sandbox, err = createSeatbelt(id, workspace, profile, requirements)
	case "bwrap":
		sandbox, err = createBwrap(id, workspace, profile, requirements)
	case "docker":
		sandbox, err = createDocker(ctx, id, workspace, profile, requirements, resources.Dockerfile)
	default:
		return nil, fmt.Errorf("unsupported runner sandbox driver %q", profile.Driver)
	}
	if err != nil {
		return nil, err
	}
	if err := runEnvironmentInstall(ctx, sandbox, activated); err != nil {
		_ = sandbox.Close(ctx)
		return nil, err
	}
	return sandbox, nil
}

// WithCommandEnvironment adds task-scoped environment values to a prepared
// command without exposing CommandSpec's internal execution fields.
func WithCommandEnvironment(command CommandSpec, values map[string]string) CommandSpec {
	bound := command
	bound.environment = mergeEnvironmentMaps(command.environment, values)
	return bound
}

// CommandEnvironment returns a copy of the closed task-scoped environment for
// runner daemons that spawn a prepared remote command without a Sandbox.Exec
// binding layer.
func CommandEnvironment(command CommandSpec) map[string]string {
	return mergeEnvironmentMaps(nil, command.environment)
}

// WithSandboxCredentials binds only explicitly requested command credentials to
// an already-open execution environment. The caller retains sandbox ownership;
// preparation helpers must not Close this wrapper independently of that owner.
func WithSandboxCredentials(sandbox Sandbox, credentials RunCredentials) Sandbox {
	if sandbox == nil {
		return nil
	}
	if managed, ok := sandbox.(*managedSandbox); ok {
		managed.SetCredentials(credentials)
		return managed
	}
	return &managedSandbox{Sandbox: sandbox, workspace: sandbox.Workspace(), credentials: credentials, slots: make(chan struct{}, 1)}
}
