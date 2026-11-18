package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestConfigureProcessConversationSandboxAllowsSeatbeltAndBwrap(t *testing.T) {
	ctx := context.Background()
	for _, driver := range []string{"bwrap", "seatbelt"} {
		root := t.TempDir()
		workspace := t.TempDir()
		sb := &processSandbox{id: "conv-" + driver, driver: driver, workspace: workspace}
		if err := ConfigureConversationSandbox(ctx, sb, root); err != nil {
			t.Fatalf("driver=%s ConfigureConversationSandbox err=%v", driver, err)
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			t.Fatal(err)
		}
		if sb.requirements.ConversationRoot != absRoot {
			t.Fatalf("driver=%s ConversationRoot=%q, want %q", driver, sb.requirements.ConversationRoot, absRoot)
		}
		for _, sub := range []string{"ws", "home"} {
			if info, err := os.Stat(filepath.Join(root, sub)); err != nil || !info.IsDir() {
				t.Fatalf("driver=%s missing %s dir: %v", driver, sub, err)
			}
		}
	}
	if err := ConfigureConversationSandbox(ctx, &processSandbox{driver: "other", workspace: t.TempDir()}, t.TempDir()); err == nil {
		t.Fatal("unexpectedly accepted unsupported process driver")
	}
}

func TestSeatbeltProfileConversationHomeAndGuardOrder(t *testing.T) {
	workspace := t.TempDir()
	root := t.TempDir()
	profile, err := seatbeltProfileCommand(workspace, SandboxRequirements{ConversationRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	absHome, err := filepath.Abs(filepath.Join(root, "home"))
	if err != nil {
		t.Fatal(err)
	}
	homeRule := "(allow file-write* (subpath "
	homeIdx := strings.Index(profile, homeRule+`"`+absHome)
	if homeIdx < 0 {
		t.Fatalf("missing conversation home write allowance for %q:\n%s", absHome, profile)
	}
	parents, files, err := conversationGuardPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, guarded := range append(parents, files...) {
		for _, variant := range seatbeltPathVariants(guarded) {
			denyRule := `(deny file-write* (literal "` + variant + `"))`
			idx := strings.Index(profile, denyRule)
			if idx < 0 {
				t.Fatalf("missing guard write denial %s:\n%s", denyRule, profile)
			}
			if idx < homeIdx {
				t.Fatalf("guard denial %s appeared before conversation home allow (seatbelt is last-match-wins):\n%s", denyRule, profile)
			}
		}
	}
}

func TestEnsureConversationCLIResolvesNativeBinaryOverride(t *testing.T) {
	binDir := t.TempDir()
	fakeCodeBuddy := filepath.Join(binDir, "custom-codebuddy")
	if err := os.WriteFile(fakeCodeBuddy, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", fakeCodeBuddy)
	t.Setenv("PATH", t.TempDir())
	sb := &processSandbox{driver: "seatbelt", workspace: t.TempDir()}
	got, err := ensureConversationCLI(context.Background(), sb, "codebuddy", t.TempDir())
	if err != nil || got != fakeCodeBuddy {
		t.Fatalf("ensureConversationCLI=%q err=%v, want %q", got, err, fakeCodeBuddy)
	}
}

func TestConversationProcessSeatbeltExecution(t *testing.T) {
	if goruntime.GOOS != "darwin" {
		t.Skip("seatbelt execution test requires macOS")
	}
	if _, err := exec.LookPath("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("sandbox-exec unavailable")
	}
	probe := exec.Command("/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true")
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("sandbox-exec unavailable in current environment: %v (%s)", err, string(out))
	}

	root := t.TempDir()
	workspace := t.TempDir()
	binDir := t.TempDir()
	cliScript := filepath.Join(binDir, "fake-codebuddy")
	// Exercise cwd, HOME, workspace writes, sibling writes inside home/.cursor,
	// read-only guard file/parent protection, and host write denial under sandbox-exec.
	script := `#!/bin/sh
set -u
echo "PWD=$(pwd -P)"
echo "HOME=$HOME"
printf 'ok\n' > created.txt || { echo "ERR:workspace_write"; exit 2; }
mkdir -p "$HOME/.cursor" && printf '{"mcpServers":{}}\n' > "$HOME/.cursor/mcp.json" || { echo "ERR:sibling_write"; exit 3; }
if (printf 'tamper\n' > "$HOME/.cursor/hooks.json") 2>/dev/null; then
  echo "ERR:guard_file_writable"
  exit 4
fi
if mv "$HOME/.cursor" "$HOME/.cursor.bak" 2>/dev/null; then
  echo "ERR:guard_parent_movable"
  exit 5
fi
if rm -f "$HOME/.cursor/hooks.json" 2>/dev/null; then
  echo "ERR:guard_file_removable"
  exit 6
fi
if (printf 'tamper\n' > /etc/agentflow-seatbelt-probe) 2>/dev/null; then
  echo "ERR:host_writable"
  exit 7
fi
echo '{"type":"result","result":"seatbelt-ok"}'
`
	if err := os.WriteFile(cliScript, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", cliScript)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	factory := func(_ context.Context, nativeRoot string) (Sandbox, func(), error) {
		sb := &processSandbox{
			id:        "conv-seatbelt-live",
			driver:    domain.SandboxDriverSeatbelt,
			binary:    "/usr/bin/sandbox-exec",
			workspace: workspace,
		}
		if err := ConfigureConversationSandbox(ctx, sb, nativeRoot); err != nil {
			return nil, nil, err
		}
		return WithSandboxCredentials(sb, RunCredentials{}), func() {}, nil
	}

	pool := &ConversationProcessPool{}
	spec := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "native", Driver: domain.SandboxDriverNative}}
	start, err := pool.Handle(ctx, root, workspace, spec, ConversationProcessRequest{
		Operation: "start",
		Command:   &ConversationCommand{Provider: "codebuddy", Argv: []string{"codebuddy"}},
	}, nil, factory)
	if err != nil {
		t.Fatalf("start seatbelt conversation failed: %v", err)
	}

	got := conversationPoll(t, pool, ctx, root, start.ID)

	absHome, err := filepath.Abs(filepath.Join(root, "home"))
	if err != nil {
		t.Fatal(err)
	}
	resolvedWS, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		resolvedWS = workspace
	}
	if !strings.Contains(got, "PWD="+resolvedWS) {
		t.Fatalf("seatbelt cwd mismatch: want PWD=%s in:\n%s", resolvedWS, got)
	}
	if !strings.Contains(got, "HOME="+absHome) {
		t.Fatalf("seatbelt HOME mismatch: want HOME=%s in:\n%s", absHome, got)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "created.txt")); err != nil || string(data) != "ok\n" {
		t.Fatalf("workspace file not created: data=%q err=%v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "home", ".cursor", "mcp.json")); err != nil || !strings.Contains(string(data), "mcpServers") {
		t.Fatalf("sibling home file not created: data=%q err=%v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "home", ".cursor", "hooks.json")); err != nil || string(data) != "{}" {
		t.Fatalf("guard file tampered: data=%q err=%v", string(data), err)
	}
}

func TestConversationProcessSeatbeltDockerDaemon(t *testing.T) {
	if goruntime.GOOS != "darwin" {
		t.Skip("seatbelt execution test requires macOS")
	}
	if _, err := exec.LookPath("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("sandbox-exec unavailable")
	}
	if err := exec.Command("/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true").Run(); err != nil {
		t.Skip("sandbox-exec unavailable in current environment")
	}
	if err := exec.Command("docker", "ps", "-q").Run(); err != nil {
		t.Skip("host Docker daemon unavailable")
	}

	root := t.TempDir()
	workspace := t.TempDir()
	binDir := t.TempDir()
	cliScript := filepath.Join(binDir, "docker-probe-codebuddy")
	script := `#!/bin/sh
set -eu
docker ps -q >/dev/null
docker run --rm alpine true
echo "SEATBELT_DOCKER_DAEMON_OK"
`
	if err := os.WriteFile(cliScript, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", cliScript)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONFIG", "")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	factory := func(_ context.Context, nativeRoot string) (Sandbox, func(), error) {
		sb := &processSandbox{
			id:        "conv-seatbelt-docker",
			driver:    domain.SandboxDriverSeatbelt,
			binary:    "/usr/bin/sandbox-exec",
			workspace: workspace,
		}
		if err := ConfigureConversationSandbox(ctx, sb, nativeRoot); err != nil {
			return nil, nil, err
		}
		return sb, func() {}, nil
	}

	pool := &ConversationProcessPool{}
	spec := &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "native", Driver: domain.SandboxDriverNative}}
	start, err := pool.Handle(ctx, root, workspace, spec, ConversationProcessRequest{
		Operation: "start",
		Command:   &ConversationCommand{Provider: "codebuddy", Argv: []string{"codebuddy"}},
	}, nil, factory)
	if err != nil {
		t.Fatalf("start seatbelt docker probe failed: %v", err)
	}
	got := conversationPoll(t, pool, ctx, root, start.ID)
	if !strings.Contains(got, "SEATBELT_DOCKER_DAEMON_OK") {
		t.Fatalf("missing docker daemon confirmation in seatbelt output:\n%s", got)
	}
}

func TestProcessSandboxResolvesHostDockerContextWhenHomeIsolated(t *testing.T) {
	hostHome := t.TempDir()
	dockerDir := filepath.Join(hostHome, ".docker")
	sum := sha256.Sum256([]byte("colima"))
	metaDir := filepath.Join(dockerDir, "contexts", "meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(metaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dockerDir, "config.json"), []byte(`{"currentContext":"colima"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "meta.json"), []byte(`{"Name":"colima","Endpoints":{"docker":{"Host":"unix:///tmp/test-colima.sock"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", hostHome)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONFIG", "")
	t.Setenv("DOCKER_CONTEXT", "")

	sb := &processSandbox{id: "p", workspace: t.TempDir(), driver: "bwrap", binary: "/bin/echo"}
	isolated := CommandSpec{Argv: []string{"echo"}}.WithPrivateHomeDir(filepath.Join(t.TempDir(), "home"))
	cmd, err := sb.command(context.Background(), isolated, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupEnvSlice(cmd.Env, "DOCKER_HOST"); got != "unix:///tmp/test-colima.sock" {
		t.Fatalf("isolated HOME DOCKER_HOST=%q, want unix:///tmp/test-colima.sock", got)
	}

	// Explicit DOCKER_HOST must win over host context resolution.
	explicit := CommandSpec{Argv: []string{"echo"}, environment: map[string]string{"HOME": filepath.Join(t.TempDir(), "home"), "DOCKER_HOST": "unix:///custom.sock"}}
	cmd, err = sb.command(context.Background(), explicit, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupEnvSlice(cmd.Env, "DOCKER_HOST"); got != "unix:///custom.sock" {
		t.Fatalf("explicit DOCKER_HOST=%q, want unix:///custom.sock", got)
	}

	// Same HOME keeps default Docker resolution untouched.
	sameHome, err := sb.command(context.Background(), CommandSpec{Argv: []string{"echo"}}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupEnvSlice(sameHome.Env, "DOCKER_HOST"); got != "" {
		t.Fatalf("unchanged HOME unexpectedly set DOCKER_HOST=%q", got)
	}

	// Explicit DOCKER_CONFIG keeps user-provided Docker config dir.
	withCfg := CommandSpec{Argv: []string{"echo"}, environment: map[string]string{"HOME": filepath.Join(t.TempDir(), "home"), "DOCKER_CONFIG": "/custom/docker"}}
	cmd, err = sb.command(context.Background(), withCfg, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupEnvSlice(cmd.Env, "DOCKER_HOST"); got != "" {
		t.Fatalf("DOCKER_CONFIG unexpectedly set DOCKER_HOST=%q", got)
	}
}
