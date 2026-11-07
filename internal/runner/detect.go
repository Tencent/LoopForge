package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/managedcli"
)

// providerSpec maps a platform executor kind to the local CLI binary the
// runner must discover. Credentials for these CLIs stay on this machine; the
// control plane never receives them.
type providerSpec struct {
	Kind   string
	Binary string
}

// defaultProviders lists the CLIs this runner drives in non-interactive
// mode. Codex and Claude Code authenticate with the machine's own login
// state; CodeBuddy cannot, so its tasks carry the responsible user's
// platform API key (injected as CODEBUDDY_API_KEY by the daemon). Additional
// providers can be added here without any protocol change: the server only
// matches on the provider name.
var defaultProviders = []providerSpec{
	{Kind: "claude-code", Binary: "claude"},
	{Kind: "codex", Binary: "codex"},
	{Kind: "codebuddy", Binary: "codebuddy"},
}

// terminalProviderKind is the built-in remote-terminal capability: the
// platform can dispatch shell commands (verification gates, and later
// sandbox-side operations) to this machine.
const terminalProviderKind = "terminal"

// mockProviderKind is served in-process (no binary) so an end-to-end test can
// exercise register/claim/execute/complete without any real Agent CLI.
const mockProviderKind = "mock"

// extraSearchPaths covers CLIs installed in user-local directories that a
// launchd/systemd service environment does not include in PATH (the daemon
// often runs with a minimal /usr/bin:/bin PATH even though the user's shell
// finds ~/.local/bin, Homebrew, or nvm-installed CLIs).
func extraSearchPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	paths := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	}
	// nvm installs Node-based CLIs (codex) under versioned directories.
	if entries, err := os.ReadDir(filepath.Join(home, ".nvm", "versions", "node")); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				paths = append(paths, filepath.Join(home, ".nvm", "versions", "node", entry.Name(), "bin"))
			}
		}
	}
	return paths
}

// lookupCLI resolves a binary name against PATH first, then the common
// user-local installation directories.
func lookupCLI(binary string) (string, bool) {
	if path, err := exec.LookPath(binary); err == nil && strings.TrimSpace(path) != "" {
		return path, true
	}
	for _, dir := range extraSearchPaths() {
		candidate := filepath.Join(dir, binary)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, true
		}
	}
	return "", false
}

// DetectRuntimes scans PATH for the known Agent CLIs and records their
// versions. A missing CLI is simply absent from the report; it is not an
// error, and the platform will not dispatch that provider to this machine.
func DetectRuntimes(ctx context.Context, extraBinaries map[string]string) []domain.RunnerRuntime {
	return detectRuntimes(ctx, extraBinaries, nil)
}

func detectRuntimes(ctx context.Context, extraBinaries map[string]string, inventory managedcli.Inventory) []domain.RunnerRuntime {
	specs := make([]providerSpec, 0, len(defaultProviders)+len(extraBinaries)+1)
	seen := map[string]bool{}
	for _, spec := range defaultProviders {
		specs = append(specs, spec)
		seen[spec.Kind] = true
	}
	kinds := make([]string, 0, len(extraBinaries))
	for kind := range extraBinaries {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		if !seen[kind] {
			specs = append(specs, providerSpec{Kind: kind, Binary: extraBinaries[kind]})
			seen[kind] = true
		}
	}
	out := make([]domain.RunnerRuntime, 0, len(specs)+2)
	out = append(out, domain.RunnerRuntime{
		Provider:     mockProviderKind,
		BinaryPath:   "(builtin)",
		Capabilities: []string{"non_interactive"},
		Status:       domain.RuntimeStatusOnline,
	})
	// The terminal capability lets the platform run verification gates and
	// sandbox-side commands on this machine.
	out = append(out, domain.RunnerRuntime{
		Provider:     terminalProviderKind,
		BinaryPath:   "(builtin)",
		Capabilities: []string{"terminal"},
		Status:       domain.RuntimeStatusOnline,
	})
	for _, spec := range specs {
		binary, managed := inventory.BinaryFor(spec.Kind)
		if !managed {
			binary = spec.Binary
		}
		if explicit := strings.TrimSpace(extraBinaries[spec.Kind]); explicit != "" {
			binary, managed = explicit, false
		}
		path, ok := lookupCLI(binary)
		if !ok {
			continue
		}
		var env map[string]string
		if managed {
			env = managedCLIEnvironment(spec.Kind)
		}
		version := probeVersion(ctx, path, env)
		out = append(out, domain.RunnerRuntime{
			Provider:     spec.Kind,
			BinaryPath:   path,
			Version:      version,
			Capabilities: []string{"non_interactive"},
			Status:       domain.RuntimeStatusOnline,
		})
	}
	return out
}

func probeVersion(ctx context.Context, path string, environments ...map[string]string) string {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, path, "--version")
	if len(environments) > 0 && len(environments[0]) > 0 {
		cmd.Env = os.Environ()
		for key, value := range environments[0] {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(out))
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	if len(line) > 128 {
		line = line[:128]
	}
	return line
}

// providerBinaryPath resolves the binary path for a claimed task from the
// runner's own runtime inventory.
func providerBinaryPath(runtimes []domain.RunnerRuntime, provider string) (string, bool) {
	for _, rt := range runtimes {
		if strings.EqualFold(rt.Provider, provider) && rt.Status != domain.RuntimeStatusDisabled {
			return rt.BinaryPath, true
		}
	}
	return "", false
}

// providerArgv builds the non-interactive CLI invocation. task.Args lets a
// task pass additional flags (for example a model override); the prompt is
// always the final positional argument so it can never be swallowed as a flag
// value.
func providerArgv(provider, binary string, prompt string, extraArgs []string) ([]string, error) {
	args := append([]string{}, extraArgs...)
	switch strings.TrimSpace(provider) {
	case "claude-code":
		return append([]string{binary}, append(args, "-p", "--output-format", "text", prompt)...), nil
	case "codex":
		return append([]string{binary}, append(args, "exec", "--skip-git-repo-check", prompt)...), nil
	case "codebuddy":
		// Keep CodeBuddy unattended: bypass permission prompts and terminate
		// option parsing before the positional prompt.
		return append([]string{binary}, append(args, "-y", "--permission-mode", "bypassPermissions", "--setting-sources", "user", "--", prompt)...), nil
	default:
		return nil, fmt.Errorf("provider %q is not supported by this runner (%s/%s)", provider, goruntime.GOOS, goruntime.GOARCH)
	}
}
