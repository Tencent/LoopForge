package runtime

import (
	"context"
	"fmt"

	"agentflow/internal/domain"
)

// TerminalPool keeps the cross-platform API while Windows workspaces use a local IDE.
type TerminalPool struct{}

// Handle rejects PTYs explicitly rather than opening an unisolated Windows shell.
func (p *TerminalPool) Handle(root, scope string, req domain.WorkspaceTerminalRequest, release func()) (TerminalResult, error) {
	return TerminalResult{}, fmt.Errorf("Windows Runner 暂不支持网页交互终端，请在本地 IDE 打开工作区；工作流非交互命令仍可执行")
}

// HandleSandbox rejects Windows PTYs without taking ownership of the caller's release function.
func (p *TerminalPool) HandleSandbox(ctx context.Context, sandbox Sandbox, scope string, req domain.WorkspaceTerminalRequest, release func()) (TerminalResult, error) {
	return p.Handle("", scope, req, release)
}

// HandleSandboxWithGitSSH matches the non-Windows API; Windows has no PTY, so
// the Git SSH identity is unused and the request is rejected like HandleSandbox.
func (p *TerminalPool) HandleSandboxWithGitSSH(ctx context.Context, sandbox Sandbox, scope string, req domain.WorkspaceTerminalRequest, release func(), keys map[string]string) (TerminalResult, error) {
	return p.HandleSandbox(ctx, sandbox, scope, req, release)
}

// CloseScope has no sessions to close because Windows terminal creation is rejected.
func (p *TerminalPool) CloseScope(scope string) {}

// HasScope reports that Windows never owns an interactive terminal lease.
func (p *TerminalPool) HasScope(scope string) bool { return false }

// HasRunScope reports that Windows never owns an interactive terminal lease.
func (p *TerminalPool) HasRunScope(runID string) bool { return false }
