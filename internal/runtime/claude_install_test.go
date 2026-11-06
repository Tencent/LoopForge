package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// fakeNPM lays out the package the way npm and Claude Code's postinstall do.
const fakeNPM = `#!/bin/sh
printf '%s\n' "$*" >> "$NPM_TEST_LOG"
[ "$NPM_TEST_FAIL" = 1 ] && { echo 'npm ERR! registry unreachable'; exit 1; }
prefix=$3
mkdir -p "$prefix/node_modules/@anthropic-ai/claude-code/bin" "$prefix/node_modules/.bin"
printf '#!/bin/sh\necho 2.1.300\n' > "$prefix/node_modules/@anthropic-ai/claude-code/bin/claude.exe"
chmod 755 "$prefix/node_modules/@anthropic-ai/claude-code/bin/claude.exe"
ln -s ../@anthropic-ai/claude-code/bin/claude.exe "$prefix/node_modules/.bin/claude"
`

func useFakeCommands(t *testing.T, scripts map[string]string) {
	t.Helper()
	bin := t.TempDir()
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
}

func TestEnsureClaudeCodeInstallsPinnedReleaseOnce(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "npm.log")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("NPM_TEST_LOG", log)
	useFakeCommands(t, map[string]string{"npm": fakeNPM})
	for range 2 {
		binary, err := EnsureClaudeCode(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if binary != claudeCodeBinary(root) {
			t.Fatalf("binary %q", binary)
		}
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(string(calls), "\n") != 1 || !strings.Contains(string(calls), "@anthropic-ai/claude-code@"+ClaudeCodeVersion) {
		t.Fatalf("npm calls: %s", calls)
	}
	if target, err := os.Readlink(filepath.Join(ClaudeCodeBinDir(), "claude")); err != nil || !strings.HasSuffix(target, "claude.exe") {
		t.Fatalf("bin link %q %v", target, err)
	}
	if info, err := os.Stat(filepath.Join(root, "claude-code", ClaudeCodeVersion)); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("sandboxes cannot read the installation: %v %v", info, err)
	}
	if staged, _ := filepath.Glob(filepath.Join(root, "claude-code", ".install-*")); len(staged) != 0 {
		t.Fatalf("staging directories left behind: %v", staged)
	}
}

func TestEnsureClaudeCodeReportsInstallFailureWithoutActivating(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("NPM_TEST_LOG", filepath.Join(t.TempDir(), "npm.log"))
	t.Setenv("NPM_TEST_FAIL", "1")
	useFakeCommands(t, map[string]string{"npm": fakeNPM})
	if _, err := EnsureClaudeCode(context.Background()); err == nil || !strings.Contains(err.Error(), "registry unreachable") {
		t.Fatalf("error %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "claude-code", ClaudeCodeVersion)); !os.IsNotExist(err) {
		t.Fatalf("failed install was activated: %v", err)
	}
}

func TestResolveClaudeCodePrefersPathAndRequiresInstallDir(t *testing.T) {
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", "")
	useFakeCommands(t, nil)
	if _, err := ResolveClaudeCode(context.Background()); err == nil {
		t.Fatal("missing CLI without an install directory must fail")
	}
	useFakeCommands(t, map[string]string{"claude": "#!/bin/sh\n"})
	if path, err := ResolveClaudeCode(context.Background()); err != nil || filepath.Base(path) != "claude" {
		t.Fatalf("PATH claude: %q %v", path, err)
	}
}

func TestDockerSandboxRunsOnDemandClaudeCodeFromMountedDirectory(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "docker.log")
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "loopforge-platform:tested")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", root)
	t.Setenv("NPM_TEST_LOG", filepath.Join(t.TempDir(), "npm.log"))
	t.Setenv("DOCKER_TEST_LOG", log)
	useFakeCommands(t, map[string]string{"npm": fakeNPM, "docker": `#!/bin/sh
printf '%s\n' "$*" >> "$DOCKER_TEST_LOG"
case "$*" in
  "image inspect loopforge-platform:tested") exit 0 ;;
  "inspect "*) exit 1 ;;
  "exec "*) echo /workspace ;;
esac
`})
	sandbox := &dockerSandbox{id: "claude-test", name: "agentflow-claude-test", workspace: t.TempDir(), dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	if err := sandbox.ensureStarted(context.Background()); err != nil {
		t.Fatal(err)
	}
	commands, _ := os.ReadFile(log)
	if !strings.Contains(string(commands), "--mount type=bind,src="+root+",dst="+root+",readonly") {
		t.Fatalf("runtime CLI directory not mounted read-only: %s", commands)
	}
	for _, test := range []struct {
		command CommandSpec
		want    string
	}{
		{CommandSpec{Provider: "claude-code", Argv: []string{"claude", "-p", "hi"}}, ClaudeCodeBinDir()},
		{CommandSpec{Provider: "claude-code", Argv: []string{"/custom/claude", "-p"}}, ""},
		{CommandSpec{Provider: "codex", Argv: []string{"codex", "exec"}}, ""},
	} {
		got, err := sandbox.onDemandCLIPath(context.Background(), test.command)
		if err != nil || got != test.want {
			t.Fatalf("%v: %q %v", test.command.Argv, got, err)
		}
	}
	if !fileIsExecutable(claudeCodeBinary(root)) {
		t.Fatal("Claude Code was not installed before the sandbox exec")
	}
	custom := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: "FROM custom\n"}}
	if got, _ := custom.onDemandCLIPath(context.Background(), CommandSpec{Provider: "claude-code", Argv: []string{"claude"}}); got != "" {
		t.Fatalf("custom images keep their own Claude Code: %q", got)
	}
	if !strings.Contains(dockerMCPCallLaunch, "PATH=$PATH:$AGENTFLOW_RUNTIME_CLI_PATH") {
		t.Fatal("on-demand CLIs must be appended after the image PATH")
	}
}
