package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestDockerSOPExecInstallsMissingCLIOnce(t *testing.T) {
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", "")
	setupMarker := filepath.Join(t.TempDir(), "installed")
	t.Setenv("LOOPFORGE_TEST_CLI_SETUP", setupMarker)
	probes := filepath.Join(t.TempDir(), "probes")
	t.Setenv("LOOPFORGE_TEST_CLI_PROBES", probes)
	useFakeCommands(t, map[string]string{"docker": `#!/bin/sh
set -eu
case "$*" in
  *loopforge-cli-probe*)
    printf 'probe\n' >> "$LOOPFORGE_TEST_CLI_PROBES"
    test -s "$LOOPFORGE_TEST_CLI_SETUP" || exit 127 ;;
  *"id -u; id -g"*) printf '1000\n1000\n' ;;
  *loopforge-cli-available*) test -s "$LOOPFORGE_TEST_CLI_SETUP" || exit 127 ;;
  *loopforge-cli-setup*)
    for mode do :; done
    test "$mode" = install
    printf 'install\n' >> "$LOOPFORGE_TEST_CLI_SETUP" ;;
  *loopforge-cli-verify*) printf '/opt/loopforge/conversation-cli/.local/bin/agy\n' ;;
  *) for arg do last=$arg; done
     printf '%s\n' "$last" ;;
esac
`})
	sandbox := &dockerSandbox{name: "workspace", started: true}
	for _, command := range []CommandSpec{
		{Provider: AgentCLIAntigravity, Argv: []string{"agy", "first"}},
		{Provider: AgentCLIAntigravity, Argv: []string{"agy", "second"}},
		{Provider: AgentCLIAntigravity, Argv: []string{"/custom/agy", "explicit"}},
		{Provider: "script", Argv: []string{"sh", "-c", "script"}},
	} {
		want := command.Argv[len(command.Argv)-1]
		out, err := sandbox.Exec(context.Background(), command, nil)
		if err != nil || strings.TrimSpace(out) != want {
			t.Fatalf("exec %v: %q %v", command.Argv, out, err)
		}
	}
	if got, err := os.ReadFile(setupMarker); err != nil || string(got) != "install\n" {
		t.Fatalf("expected exactly one setup, got %q: %v", got, err)
	}
	if got, err := os.ReadFile(probes); err != nil || string(got) != "probe\nprobe\n" {
		t.Fatalf("expected probes only for standard CLI invocations, got %q: %v", got, err)
	}
}

// A CLI visible only to the image's root account must not prevent preparation
// of a copy that the task user can execute.
func TestDockerSOPUsesCLIExecutableByTaskUser(t *testing.T) {
	if os.Getenv("AGENTFLOW_TEST_DOCKER") != "1" {
		t.Skip("set AGENTFLOW_TEST_DOCKER=1 with the python:3.13-alpine image")
	}
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	docker := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v: %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	container := docker("", "run", "--pull=never", "-d", "--network", "none", "--user", "1000:1000",
		"--workdir", "/", "--cap-drop=ALL", "--security-opt", "no-new-privileges",
		"python:3.13-alpine", "sleep", "120")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	docker(`set -eu
mkdir -p /workspace /root/private/bin /opt/probe-interpreter
chmod 700 /root
printf '#!/bin/sh\necho root-only\n' > /root/private/bin/agy
printf '[ "$(id -u)" != 0 ] || export PATH=/root/private/bin:$PATH\n' > /etc/profile.d/root-cli.sh
printf '#!/bin/sh\nexec /bin/sh "$@"\n' > /opt/probe-interpreter/probe-interpreter
cat > /etc/profile.d/probe-path.sh <<'PROFILE'
export PATH=/opt/probe-interpreter:$PATH
case "$PWD" in /tmp/loopforge-cli-version.*) echo 9.9.9 ;; esac
PROFILE
cat > /usr/local/bin/curl <<'CURL'
#!/bin/sh
set -eu
while [ "$1" != -o ]; do shift; done
cat > "$2" <<'INSTALL'
#!/bin/sh
set -eu
mkdir -p "$HOME/.local/bin"
cat > "$HOME/.local/bin/agy" <<'AGY'
#!/usr/bin/env probe-interpreter
set -eu
if [ "$1" = "--version" ]; then
  test "$HOME" = "$TMPDIR" && test "$HOME" = "$PWD"
  test -z "${PROBE_FIXTURE_SECRET:-}"
  echo 1.5.13
else
  id -u
fi
AGY
chmod 755 "$HOME/.local/bin/agy"
echo install >> /opt/loopforge/conversation-cli/installs
INSTALL
CURL
printf '#!/bin/sh\nexec /bin/sh "$@"\n' > /usr/local/bin/bash
cat > /usr/local/bin/slow-version-fixture <<'SLOW'
#!/bin/sh
touch "$HOME/scratch" /tmp/version-probe-started
exec sleep 30
SLOW
chmod 755 /root/private/bin/agy /usr/local/bin/curl /usr/local/bin/bash /usr/local/bin/slow-version-fixture /opt/probe-interpreter/probe-interpreter
`, "exec", "-i", "--user", "0:0", container, "sh", "-s")
	if got := docker("", "exec", "--user", "0:0", container, "sh", "-lc", "command -v agy"); got != "/root/private/bin/agy" {
		t.Fatalf("root CLI fixture: %q", got)
	}
	sandbox := &dockerSandbox{name: container, started: true}
	for range 2 {
		out, err := sandbox.Exec(ctx, CommandSpec{Provider: AgentCLIAntigravity, Argv: []string{"agy", "--identify-fixture"}}, nil)
		if err != nil || strings.TrimSpace(out) != "1000" {
			t.Fatalf("task CLI: %q: %v", out, err)
		}
	}
	path, err := ensureConversationCLI(ctx, sandbox, AgentCLIAntigravity, "/workspace/home")
	if err != nil || path != "/opt/loopforge/conversation-cli/.local/bin/agy" {
		t.Fatalf("reusing installed CLI for a conversation: %q: %v", path, err)
	}
	if got := docker("", "exec", container, "cat", "/opt/loopforge/conversation-cli/installs"); got != "install" {
		t.Fatalf("expected CLI to be installed once, got %q", got)
	}
	sandbox.profile.Environment = map[string]string{"PROBE_FIXTURE_SECRET": "workspace-value"}
	if version := sandbox.probeCLIVersion(ctx, AgentCLIAntigravity, path); version != "1.5.13" {
		t.Fatalf("Docker CLI version = %q", version)
	}

	probeCtx, stopProbe := context.WithCancel(ctx)
	defer stopProbe()
	stopped := make(chan string, 1)
	go func() {
		stopped <- sandbox.probeCLIVersion(probeCtx, AgentCLIAntigravity, "/usr/local/bin/slow-version-fixture")
	}()
	docker("", "exec", container, "sh", "-c", `
		for i in $(seq 1 50); do
			[ ! -f /tmp/version-probe-started ] || exit 0
			sleep 0.1
		done
		exit 1`)
	stopProbe()
	select {
	case version := <-stopped:
		if version != "" {
			t.Fatalf("cancelled probe reported version %q", version)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled Docker client did not return")
	}
	docker("", "exec", container, "sh", "-c", `
		for i in $(seq 1 80); do
			set -- /tmp/loopforge-cli-version.*
			[ -e "$1" ] || exit 0
			sleep 0.1
		done
		exit 1`)
}

// A failed on-demand install must stop the sandbox exec instead of silently
// running without the CLI on PATH.
func TestDockerSandboxOnDemandCLIPathReturnsInstallErrors(t *testing.T) {
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "loopforge-platform:tested")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", t.TempDir())
	t.Setenv("NPM_TEST_LOG", filepath.Join(t.TempDir(), "npm.log"))
	t.Setenv("NPM_TEST_BIN", "kimi")
	t.Setenv("NPM_TEST_FAIL", "1")
	sandbox := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}

	useFakeCommands(t, map[string]string{"npm": genericFakeNPM})
	if got, err := sandbox.onDemandCLIPath(context.Background(), CommandSpec{Provider: "kimi", Argv: []string{"kimi"}}); err == nil || got != "" {
		t.Fatalf("npm agent CLI install failure: %q %v", got, err)
	}

	useFakeCommands(t, map[string]string{"npm": fakeNPM})
	if got, err := sandbox.onDemandCLIPath(context.Background(), CommandSpec{Provider: "claude-code", Argv: []string{"claude", "-p"}}); err == nil || got != "" {
		t.Fatalf("Claude Code install failure: %q %v", got, err)
	}
}

// Commands that never need an on-demand install must not touch npm at all.
func TestDockerSandboxOnDemandCLIPathSkipsWithoutInstalling(t *testing.T) {
	log := filepath.Join(t.TempDir(), "npm.log")
	t.Setenv("AGENTFLOW_ISOLATED_IMAGE", "loopforge-platform:tested")
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", t.TempDir())
	t.Setenv("NPM_TEST_LOG", log)
	t.Setenv("NPM_TEST_BIN", "opencode")
	useFakeCommands(t, map[string]string{"npm": genericFakeNPM})
	sandbox := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: IsolatedDockerfile}}
	for _, command := range []CommandSpec{
		{Provider: "opencode"},
		{Provider: "opencode", Argv: []string{}},
		{Provider: "unknown-engine", Argv: []string{"unknown-engine"}},
		{Provider: "", Argv: []string{"opencode"}},
		{Provider: "agy", Argv: []string{"agy"}},
		{Provider: "opencode", Argv: []string{"kimi"}},
	} {
		if got, err := sandbox.onDemandCLIPath(context.Background(), command); err != nil || got != "" {
			t.Fatalf("%q %v: %q %v", command.Provider, command.Argv, got, err)
		}
	}
	custom := &dockerSandbox{dockerfile: domain.DockerfileDefinition{Content: "FROM custom\n"}}
	if got, err := custom.onDemandCLIPath(context.Background(), CommandSpec{Provider: "opencode", Argv: []string{"opencode"}}); err != nil || got != "" {
		t.Fatalf("custom images keep their own agent CLIs: %q %v", got, err)
	}
	t.Setenv("AGENTFLOW_RUNTIME_CLI_DIR", "")
	if got, err := sandbox.onDemandCLIPath(context.Background(), CommandSpec{Provider: "opencode", Argv: []string{"opencode"}}); err != nil || got != "" {
		t.Fatalf("no runtime CLI directory: %q %v", got, err)
	}
	if calls, _ := os.ReadFile(log); len(calls) != 0 {
		t.Fatalf("npm must not run: %s", calls)
	}
}

func TestOnDemandCLICatalogMarksBundledCLIs(t *testing.T) {
	catalog := onDemandCLICatalog()
	for engine, want := range map[string]bool{
		"codex": true, "codebuddy": true, "dsh": true,
		"claude-code": false, "opencode": false, "cursor": false, "agy": false, "kimi": false, "pi": false,
	} {
		if got := catalog[engine].Bundled; got != want {
			t.Fatalf("%s bundled = %v, want %v", engine, got, want)
		}
	}
	if pkg := catalog["dsh"].Package; pkg != "@deepseek-ai/dsh@"+DeepSeekHarnessVersion || !catalog["dsh"].RelaxEngines {
		t.Fatalf("dsh must pin its release and relax engines: %+v", catalog["dsh"])
	}
}

func TestSensitiveProcessEnvironmentKeyMatchesCredentialSuffixes(t *testing.T) {
	for _, key := range []string{
		// keys listed explicitly before the suffix rule
		"CODEBUDDY_API_KEY", "OPENAI_API_KEY", "CODEX_API_KEY", "ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "AGENTFLOW_GIT_TOKEN", "GIT_CONFIG_KEY_0",
		// covered by the suffix rule
		"DEEPSEEK_API_KEY", "CURSOR_AUTH_TOKEN", "WEBHOOK_SECRET", "DB_PASSWORD",
		"deepseek_api_key", "Db_Password", "git_config_key_1",
	} {
		if !sensitiveProcessEnvironmentKey(key) {
			t.Errorf("%s must be redacted", key)
		}
	}
	for _, key := range []string{
		"", "PATH", "HOME", "DSH_HOME", "API_KEY_FILE", "MAX_TOKENS", "TOKEN", "SECRET",
		"GIT_CONFIG_VALUE_0", "GIT_CONFIG_COUNT", "NODE_OPTIONS",
	} {
		if sensitiveProcessEnvironmentKey(key) {
			t.Errorf("%s must not be redacted", key)
		}
	}
}
