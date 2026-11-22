package httpapi

import (
	"fmt"
	"strings"

	"agentflow/internal/domain"
)

type localSandboxRunners interface {
	Runner(id string) (domain.Runner, bool)
}

// localSandboxOwner fails unless userID owns the execution machine a local
// sandbox is bound to. Non-local profiles pass unchanged.
func localSandboxOwner(runners localSandboxRunners, profile domain.SandboxProfile, userID int64) error {
	if strings.TrimSpace(profile.Driver) != domain.SandboxDriverLocal {
		return nil
	}
	runnerID := strings.TrimSpace(profile.RunnerID)
	if runnerID == "" {
		return fmt.Errorf("local 沙箱 %s 未绑定执行机器，请由机器主人重新配置", profile.ID)
	}
	runner, ok := runners.Runner(runnerID)
	if !ok {
		return fmt.Errorf("local 沙箱 %s 绑定的执行机器 %s 不存在", profile.ID, runnerID)
	}
	if userID == 0 || runner.OwnerUserID != userID {
		return fmt.Errorf("local 沙箱 %s 绑定的是他人的执行机器 %s，只有机器主人可以配置和使用", profile.ID, runner.Name)
	}
	return nil
}

// localSandboxRunner checks that a conversation on runnerID may open the local
// sandbox profile: the machine must be the bound one and support worktrees.
func localSandboxRunner(runners localSandboxRunners, profile domain.SandboxProfile, runnerID string) error {
	if strings.TrimSpace(profile.Driver) != domain.SandboxDriverLocal {
		return nil
	}
	if strings.TrimSpace(runnerID) != strings.TrimSpace(profile.RunnerID) {
		return fmt.Errorf("local 沙箱 %s 只能在它绑定的执行机器上打开，请在该机器上新建对话", profile.ID)
	}
	runner, ok := runners.Runner(runnerID)
	if !ok {
		return fmt.Errorf("local 沙箱 %s 绑定的执行机器 %s 不存在", profile.ID, runnerID)
	}
	if !runner.SupportsLocalWorktree() {
		return fmt.Errorf("local 沙箱需要执行机器升级至 %s 或更新版本，请先更新 Runner", domain.RunnerVersionLocalWorktree)
	}
	return nil
}
