package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const genericFakeNPM = `#!/bin/sh
printf '%s\n' "$*" >> "$NPM_TEST_LOG"
[ "$NPM_TEST_FAIL" = 1 ] && { echo 'npm ERR! registry unreachable'; exit 1; }
prefix=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--prefix" ]; then prefix=$arg; fi
  prev=$arg
done
[ -n "$prefix" ] || { echo 'npm ERR! missing prefix'; exit 1; }
mkdir -p "$prefix/node_modules/.bin"
printf '#!/bin/sh\necho ok\n' > "$prefix/node_modules/.bin/$NPM_TEST_BIN"
chmod 755 "$prefix/node_modules/.bin/$NPM_TEST_BIN"
`

func TestInstallOnDemandCLIInstallsNPMPackageOnce(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "npm.log")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("NPM_TEST_LOG", log)
	t.Setenv("NPM_TEST_BIN", "opencode")
	useFakeCommands(t, map[string]string{"npm": genericFakeNPM})

	first, err := InstallOnDemandCLI(context.Background(), "opencode")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "opencode", "current", "node_modules", ".bin", "opencode")
	if first != want {
		t.Fatalf("binary %q", first)
	}
	if got, ok := OnDemandCLIPath("opencode"); !ok || got != want {
		t.Fatalf("installed path %q %v", got, ok)
	}
	second, err := InstallOnDemandCLI(context.Background(), "opencode")
	if err != nil || second != want {
		t.Fatalf("reuse %q %v", second, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "\n") != 1 || !strings.Contains(string(calls), "opencode-ai") || !strings.Contains(string(calls), "--engine-strict") || strings.Contains(string(calls), "--engine-strict=false") {
		t.Fatalf("npm calls: %s", calls)
	}
	if staged, _ := filepath.Glob(filepath.Join(root, "opencode", ".install-*")); len(staged) != 0 {
		t.Fatalf("staging directories left behind: %v", staged)
	}
}

func TestInstallOnDemandCLIKeepsDeepSeekEngineRelaxation(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "npm.log")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("NPM_TEST_LOG", log)
	t.Setenv("NPM_TEST_BIN", "dsh")
	useFakeCommands(t, map[string]string{"npm": genericFakeNPM})
	if _, err := InstallOnDemandCLI(context.Background(), "dsh"); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "@deepseek-ai/dsh") || !strings.Contains(string(calls), "--engine-strict=false") {
		t.Fatalf("npm calls: %s", calls)
	}
}

func TestInstallOnDemandCLIReportsNPMFailureWithoutActivating(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("NPM_TEST_LOG", filepath.Join(t.TempDir(), "npm.log"))
	t.Setenv("NPM_TEST_FAIL", "1")
	t.Setenv("NPM_TEST_BIN", "kimi")
	useFakeCommands(t, map[string]string{"npm": genericFakeNPM})
	if _, err := InstallOnDemandCLI(context.Background(), "kimi"); err == nil || !strings.Contains(err.Error(), "registry unreachable") {
		t.Fatalf("error %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "kimi", "current")); !os.IsNotExist(err) {
		t.Fatalf("failed install was activated: %v", err)
	}
}

func TestInstallOnDemandCLIInstallsCursorWithOfficialInstaller(t *testing.T) {
	home := t.TempDir()
	log := filepath.Join(t.TempDir(), "bash.log")
	t.Setenv("HOME", home)
	t.Setenv("BASH_TEST_LOG", log)
	useFakeCommands(t, map[string]string{"bash": `#!/bin/sh
printf '%s\n' "$*" >> "$BASH_TEST_LOG"
mkdir -p "$HOME/.local/bin"
printf '#!/bin/sh\necho cursor\n' > "$HOME/.local/bin/cursor-agent"
chmod 755 "$HOME/.local/bin/cursor-agent"
`})
	path, err := InstallOnDemandCLI(context.Background(), "cursor")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, ".local", "bin", "cursor-agent") {
		t.Fatalf("binary %q", path)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "https://cursor.com/install") {
		t.Fatalf("installer: %s", calls)
	}
	if _, err := InstallOnDemandCLI(context.Background(), "cursor"); err != nil {
		t.Fatal(err)
	}
	calls, _ = os.ReadFile(log)
	if strings.Count(string(calls), "\n") != 1 {
		t.Fatalf("installer ran twice: %s", calls)
	}
}

func TestInstallOnDemandCLIRejectsUnknownEngineAndEmptyDirectory(t *testing.T) {
	if OnDemandInstallable("not-a-cli") {
		t.Fatal("unknown engine must not be installable")
	}
	if _, err := InstallOnDemandCLI(context.Background(), "not-a-cli"); err == nil || !strings.Contains(err.Error(), "不能从对话安装") {
		t.Fatalf("error %v", err)
	}
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", "")
	useFakeCommands(t, map[string]string{"npm": genericFakeNPM})
	if _, err := InstallOnDemandCLI(context.Background(), "pi"); err == nil || !strings.Contains(err.Error(), "没有配置 CLI 安装目录") {
		t.Fatalf("error %v", err)
	}
}

func TestInstallOnDemandClaudeCodeUsesPinnedRelease(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "npm.log")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("NPM_TEST_LOG", log)
	useFakeCommands(t, map[string]string{"npm": fakeNPM})
	path, err := InstallOnDemandCLI(context.Background(), "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if path != claudeCodeBinary(root) {
		t.Fatalf("binary %q", path)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "@anthropic-ai/claude-code@"+ClaudeCodeVersion) {
		t.Fatalf("npm calls: %s", calls)
	}
}
