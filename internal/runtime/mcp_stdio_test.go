package runtime

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestScriptMCPEnvironmentIncludesStdioAndCallsTool(t *testing.T) {
	workspace := t.TempDir()
	sb := &localSandbox{workspace: workspace}
	serverScript := `read init
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"test","version":"1"}}}'
read initialized
read request
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"stdio-ok"}]}}'`
	servers := []domain.MCPServerDefinition{{ID: "local", Name: "Local", Transport: "stdio", Command: "sh", Args: []string{"-c", serverScript}, TimeoutSec: 5}}
	env, err := ScriptMCPEnvironment(sb, workspace, servers)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	stdout, stderr, code := captureMCPCall(t, []string{"call", "local", "ping", "{}"})
	if code != 0 || strings.TrimSpace(stdout) != "stdio-ok" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

type provisionSandbox struct {
	workspace string
	commands  int
	last      CommandSpec
}

func (s *provisionSandbox) ID() string                  { return "run-1" }
func (s *provisionSandbox) Workspace() string           { return s.workspace }
func (s *provisionSandbox) Driver() string              { return "bwrap" }
func (s *provisionSandbox) Close(context.Context) error { return nil }
func (s *provisionSandbox) Exec(_ context.Context, command CommandSpec, _ EventSink) (string, error) {
	s.commands++
	s.last = command
	if len(command.Argv) < 3 || !strings.Contains(command.Argv[2], "npm ci") {
		return "", os.ErrInvalid
	}
	return "", nil
}

func TestProvisionMCPAssetsMaterializesFilesAndRunsInstallInSandbox(t *testing.T) {
	workspace := t.TempDir()
	sb := &provisionSandbox{workspace: workspace}
	server := domain.MCPServerDefinition{ID: "node-mcp", Name: "Node MCP", Transport: "stdio", Command: "node", InstallScript: "npm ci", PathEntries: []string{"tools/bin"}, FileRefs: []domain.MCPFileRef{{Path: "src/server.js", Mode: 0o644, Size: 2}}}
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{server}, MCPFiles: map[string]map[string][]byte{"node-mcp": {"src/server.js": []byte("ok")}}}
	if err := ProvisionMCPAssets(context.Background(), sb, workspace, assets, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".agentflow", "mcp", "node-mcp", "src", "server.js"))
	if err != nil || string(raw) != "ok" {
		t.Fatalf("materialized=%q err=%v", raw, err)
	}
	if sb.commands != 1 {
		t.Fatalf("commands=%d", sb.commands)
	}
	runtimeRoot := filepath.Join(workspace, ".agentflow", "mcp-runtime", "node-mcp")
	launcher, err := os.ReadFile(filepath.Join(runtimeRoot, "launch"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(launcher)
	for _, want := range []string{`export HOME="$PREFIX/home"`, `NPM_CONFIG_PREFIX="$PREFIX"`, `PIP_USER=1`, filepath.Join(runtimeRoot, "tools", "bin"), filepath.Join(runtimeRoot, "cargo", "bin"), filepath.Join(runtimeRoot, "home", ".local", "bin"), filepath.Join(workspace, ".agentflow", "mcp", "node-mcp", "node_modules", ".bin"), `cd "$WORKSPACE"/'.agentflow/mcp/node-mcp'`, `exec 'node'`} {
		if !strings.Contains(text, want) {
			t.Fatalf("launcher missing %q:\n%s", want, text)
		}
	}
	env := CommandEnvironment(sb.last)
	if env["PREFIX"] != runtimeRoot || env["HOME"] != filepath.Join(runtimeRoot, "home") || env["NPM_CONFIG_PREFIX"] != runtimeRoot || env["PIP_USER"] != "1" {
		t.Fatalf("managed MCP environment = %#v", env)
	}
	for _, want := range []string{filepath.Join(runtimeRoot, "bin"), filepath.Join(runtimeRoot, "cargo", "bin"), filepath.Join(runtimeRoot, "tools", "bin")} {
		if !strings.Contains(sb.last.Argv[2], want) {
			t.Fatalf("install command PATH missing %q: %s", want, sb.last.Argv[2])
		}
	}
}

func TestMCPRuntimeWorkingDirectoryTreatsUploadedCWDAsSourceRelative(t *testing.T) {
	server := domain.MCPServerDefinition{ID: "replace-batch", WorkingDirectory: "packages/mcp", FileRefs: []domain.MCPFileRef{{Path: "packages/mcp/server.py"}}}
	if got, want := MCPRuntimeWorkingDirectory(server), ".agentflow/mcp/replace-batch/packages/mcp"; got != want {
		t.Fatalf("cwd=%q want %q", got, want)
	}
	server.FileRefs = nil
	if got, want := MCPRuntimeWorkingDirectory(server), "packages/mcp"; got != want {
		t.Fatalf("workspace cwd=%q want %q", got, want)
	}
}

func TestManagedMCPRuntimeOnlyAppliesToProcessSandboxes(t *testing.T) {
	server := domain.MCPServerDefinition{ID: "replace-batch", Transport: "stdio", Command: "python3", Args: []string{"server.py"}, FileRefs: []domain.MCPFileRef{{Path: "server.py"}}}
	for _, driver := range []string{"seatbelt", "bwrap"} {
		prepared := prepareMCPServersForDriver([]domain.MCPServerDefinition{server}, driver)[0]
		if prepared.Command != ".agentflow/mcp-runtime/replace-batch/launch" || len(prepared.Args) != 0 || prepared.WorkingDirectory != "" || len(prepared.FileRefs) != 0 {
			t.Fatalf("%s prepared server = %#v", driver, prepared)
		}
	}
	for _, driver := range []string{"local", "docker", "runner"} {
		prepared := prepareMCPServersForDriver([]domain.MCPServerDefinition{server}, driver)[0]
		if prepared.Command != "python3" || len(prepared.Args) != 1 || len(prepared.FileRefs) != 1 {
			t.Fatalf("%s must stay unchanged: %#v", driver, prepared)
		}
	}
}

func TestManagedMCPRuntimeInstallsAndLaunchesInAvailableProcessSandbox(t *testing.T) {
	if os.Getenv("AGENTFLOW_SANDBOX_SMOKE") != "1" {
		t.Skip("set AGENTFLOW_SANDBOX_SMOKE=1 on a host that permits seatbelt/bwrap")
	}
	workspace := t.TempDir()
	var (
		sb  Sandbox
		err error
	)
	switch goruntime.GOOS {
	case "darwin":
		sb, err = createSeatbelt("mcp-prefix", workspace, domain.SandboxProfile{Driver: "seatbelt"}, SandboxRequirements{})
	case "linux":
		sb, err = createBwrap("mcp-prefix", workspace, domain.SandboxProfile{Driver: "bwrap"}, SandboxRequirements{})
	default:
		t.Skip("seatbelt/bwrap unavailable on this OS")
	}
	if err != nil {
		t.Skipf("process sandbox unavailable: %v", err)
	}
	server := domain.MCPServerDefinition{
		ID: "prefix-probe", Name: "Prefix Probe", Transport: "stdio", Command: "zg",
		InstallScript: `mkdir -p "$CARGO_HOME/bin"
cat > "$CARGO_HOME/bin/zg" <<'EOF'
#!/bin/sh
printf '%s|%s|%s' "$PREFIX" "$HOME" "$PWD"
EOF
chmod +x "$CARGO_HOME/bin/zg"`,
	}
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{server}}
	sink := func(event domain.TelemetryEvent) { t.Logf("%s: %s", event.Name, event.Message) }
	if err := ProvisionMCPAssets(context.Background(), sb, workspace, assets, sink); err != nil {
		t.Fatal(err)
	}
	prepared := prepareMCPServersForDriver([]domain.MCPServerDefinition{server}, sandboxDriver(sb))[0]
	out, err := sb.Exec(context.Background(), CommandSpec{Argv: []string{prepared.Command}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(resolvedWorkspace, ".agentflow", "mcp-runtime", "prefix-probe")
	want := strings.Join([]string{prefix, filepath.Join(prefix, "home"), resolvedWorkspace}, "|")
	if strings.TrimSpace(out) != want {
		t.Fatalf("launcher output=%q want %q", out, want)
	}
}

func TestProvisionMCPInstallRejectsLocalDriver(t *testing.T) {
	workspace := t.TempDir()
	sb := &localSandbox{id: "run", workspace: workspace}
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{{ID: "unsafe", Name: "Unsafe", Transport: "stdio", Command: "x", InstallScript: "touch host"}}}
	if err := ProvisionMCPAssets(context.Background(), sb, workspace, assets, nil); err == nil || !strings.Contains(err.Error(), "隔离沙箱") {
		t.Fatalf("err=%v", err)
	}
}

func TestProvisionMCPRejectsUnsafeRuntimeID(t *testing.T) {
	workspace := t.TempDir()
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{{ID: "!!!", Name: "Unsafe", Transport: "stdio", Command: "x", FileRefs: []domain.MCPFileRef{{Path: "server.js", Size: 1}}}}}
	if err := ProvisionMCPAssets(context.Background(), &provisionSandbox{workspace: workspace}, workspace, assets, nil); err == nil || !strings.Contains(err.Error(), "runtime path") {
		t.Fatalf("err=%v", err)
	}
}
