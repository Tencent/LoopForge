package runner

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"agentflow/internal/domain"
)

func cliListsUpdate(ctx context.Context, binary, subcommand string) bool {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--help")
	cmd.Env, cmd.Dir, cmd.WaitDelay = cliUpdateEnvironment(binary), os.TempDir(), time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	inCommands := false
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		switch strings.ToLower(trimmed) {
		case "commands:", "available commands:", "available subcommands:":
			inCommands = true
			continue
		case "":
			continue
		}
		if line == trimmed {
			inCommands = false
		}
		if !inCommands {
			continue
		}
		fields := strings.Fields(trimmed)
		name := fields[0]
		if name == filepath.Base(binary) && len(fields) > 1 {
			name = fields[1]
		}
		if slices.Contains(strings.Split(name, "|"), subcommand) {
			return true
		}
	}
	return false
}

func cliUpdateEnvironment(binary string) []string {
	paths := append([]string{filepath.Dir(binary)}, extraSearchPaths()...)
	paths = append(paths, os.Getenv("PATH"))
	env := []string{"PATH=" + strings.Join(paths, string(os.PathListSeparator)), "CI=1", "NO_COLOR=1"}
	for _, key := range []string{
		"HOME", "USERPROFILE", "USER", "LOGNAME", "LANG", "SHELL", "TMPDIR", "LOCALAPPDATA", "APPDATA",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func cliUpdateManualResult() domain.RunnerMaintenanceResult {
	return domain.RunnerMaintenanceResult{Status: "manual", Message: "请在执行机器上使用该 CLI 原来的安装方式更新"}
}

func (d *Daemon) applyNativeCLIUpdate(ctx context.Context, rt domain.RunnerRuntime) domain.RunnerMaintenanceResult {
	index := slices.IndexFunc(defaultProviders, func(spec providerSpec) bool { return spec.Kind == rt.Provider })
	if index < 0 || defaultProviders[index].Update == "" || rt.BinaryPath == "" {
		return cliUpdateManualResult()
	}
	spec := defaultProviders[index]
	if !cliListsUpdate(ctx, rt.BinaryPath, spec.Update) {
		return cliUpdateManualResult()
	}
	if !d.updateMu.TryLock() {
		return domain.RunnerMaintenanceResult{Status: "failed", Message: "机器上有运行中的工作，请空闲后重试"}
	}
	defer d.updateMu.Unlock()
	if d.updateReady.Load() || d.runnerExecutionActive() {
		return domain.RunnerMaintenanceResult{Status: "failed", Message: "机器上有运行中的工作，请空闲后重试"}
	}
	dir, err := os.MkdirTemp("", "loopforge-cli-update-")
	if err != nil {
		return domain.RunnerMaintenanceResult{Status: "failed", Message: "无法创建 CLI 更新目录"}
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, rt.BinaryPath, spec.Update)
	cmd.Env, cmd.Dir, cmd.WaitDelay = cliUpdateEnvironment(rt.BinaryPath), dir, time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() == nil && cliUpdateRequiresManual(string(out)) {
		return cliUpdateManualResult()
	}
	if err != nil {
		log.Printf("native CLI update (%s): %v", rt.Provider, err)
		return domain.RunnerMaintenanceResult{Status: "failed", Message: "CLI 自更新失败，请检查执行机器的网络和安装状态"}
	}
	d.refreshRuntimes(ctx)
	select {
	case d.heartbeatWake <- struct{}{}:
	default:
	}
	for _, updated := range d.currentRuntimes() {
		if updated.Provider != rt.Provider || updated.Version == "" {
			continue
		}
		status := "updated"
		if updated.Version == rt.Version {
			status = "current"
		}
		return domain.RunnerMaintenanceResult{Status: status, Version: updated.Version}
	}
	return domain.RunnerMaintenanceResult{Status: "failed", Message: "CLI 更新后无法读取版本，请在执行机器上检查安装"}
}

func cliUpdateRequiresManual(output string) bool {
	output = strings.ToLower(output)
	for _, message := range []string{
		"update manually", "upgrade manually", "manual update", "manual upgrade",
		"update through the package manager", "use your package manager",
		"not supported for this installation", "not installed via", "cannot self-update",
	} {
		if strings.Contains(output, message) {
			return true
		}
	}
	return false
}
