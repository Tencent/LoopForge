package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestMain(m *testing.M) {
	if RunMCPCallCLI(os.Args[1:]) {
		return
	}
	os.Exit(m.Run())
}

func TestBindPersonalMCPHeadersOptionalSkipsMissingPAT(t *testing.T) {
	assets := domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{
		{ID: "iwiki", Name: "iWiki", Transport: "http", URL: "https://example.com", AuthType: MCPAuthTaihuPAT},
	}}
	bound := BindPersonalMCPHeadersOptional(assets, "")
	if _, ok := bound.MCPServers[0].Headers["Authorization"]; ok {
		t.Fatal("missing PAT should not invent an Authorization header")
	}
	bound = BindPersonalMCPHeadersOptional(assets, "tok")
	if bound.MCPServers[0].Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("authorization = %q", bound.MCPServers[0].Headers["Authorization"])
	}
}

func TestMCPCallAliasesMatchURLAndInstallID(t *testing.T) {
	server := mcpCallServer{
		ID:       "mcp-2214-project-dns",
		Name:     "七彩石（Rainbow） MCP",
		MarketID: "2214",
		URL:      "https://mcpgw.knot.example.com/rainbow",
		Aliases: mcpCallAliases(domain.MCPServerDefinition{
			ID: "mcp-2214-project-dns", Name: "七彩石（Rainbow） MCP", MarketID: "2214", URL: "https://mcpgw.knot.example.com/rainbow",
		}),
	}
	got, err := matchMCPCallServer([]mcpCallServer{server}, "rainbow")
	if err != nil || got.ID != server.ID {
		t.Fatalf("rainbow = %+v err=%v aliases=%v", got, err, server.Aliases)
	}
	got, err = matchMCPCallServer([]mcpCallServer{server}, "2214")
	if err != nil || got.ID != server.ID {
		t.Fatalf("2214 = %+v err=%v", got, err)
	}
}

func TestMCPCallCLIInvokesBoundHTTPServer(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tai_pat" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"env-ok"}]}}`))
		default:
			t.Fatalf("method %s", req.Method)
		}
	}))
	t.Cleanup(gateway.Close)

	workspace := t.TempDir()
	sb := &localSandbox{workspace: workspace}
	assets := BindPersonalMCPHeadersOptional(domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{{
		ID: "mcp-2214-project-dns", Name: "七彩石（Rainbow） MCP", MarketID: "2214", Transport: "http",
		URL: gateway.URL, AuthType: MCPAuthTaihuPAT,
	}}}, "tai_pat")
	env, err := ScriptMCPEnvironment(sb, workspace, assets.MCPServers)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	stdout, stderr, code := captureMCPCall(t, []string{"call", "2214", "get_config", `{"group":"dnspod_intl.dnspod_api"}`})
	if code != 0 {
		t.Fatalf("exit %d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if strings.TrimSpace(stdout) != "env-ok" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestMCPCallRequiresTaihuPATAtCallTime(t *testing.T) {
	workspace := t.TempDir()
	sb := &localSandbox{workspace: workspace}
	assets := BindPersonalMCPHeadersOptional(domain.AgentAssetBundle{MCPServers: []domain.MCPServerDefinition{{
		ID: "iwiki", Name: "iWiki", Transport: "http", URL: "https://example.com/mcp", AuthType: MCPAuthTaihuPAT,
	}}}, "")
	env, err := ScriptMCPEnvironment(sb, workspace, assets.MCPServers)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	_, stderr, code := captureMCPCall(t, []string{"call", "iwiki", "search"})
	if code != 1 || !strings.Contains(stderr, "太湖个人令牌") {
		t.Fatalf("exit %d stderr = %q", code, stderr)
	}
}

func TestBindMCPWritesWorkspaceBinWithoutPATH(t *testing.T) {
	workspace := t.TempDir()
	sb := &localSandbox{workspace: workspace, environment: map[string]string{"PATH": "/usr/bin"}}
	command := CommandSpec{Argv: []string{"sh", "-lc", "agentflow mcp list"}, CredentialBindings: []CredentialBinding{BindMCP}}
	bound, err := bindCommand(sb, workspace, command, RunCredentials{MCPServers: []domain.MCPServerDefinition{{
		ID: "tapd", Name: "TAPD", Transport: "http", URL: "https://example.com/tapd",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bound.environment["PATH"]; ok {
		t.Fatalf("local unisolated MCP bind must not rewrite PATH, got %q", bound.environment["PATH"])
	}
	helper := filepath.Join(workspace, ".agentflow", "runtime", "bin", "agentflow")
	if _, err := os.Stat(helper); err != nil {
		t.Fatal(err)
	}
	if bound.environment[mcpCallBinEnv] != helper {
		t.Fatalf("AGENTFLOW_BIN = %q want %q", bound.environment[mcpCallBinEnv], helper)
	}
	if bound.environment[mcpCallConfigEnv] == "" {
		t.Fatal("missing MCP config env")
	}
	if len(bound.Argv) < 3 || !strings.Contains(bound.Argv[2], "agentflow()") {
		t.Fatalf("local shell should define agentflow function, argv=%v", bound.Argv)
	}
	raw, err := os.ReadFile(bound.environment[mcpCallConfigEnv])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Authorization") && strings.Contains(string(raw), "Bearer") {
		t.Fatal("unbound custom MCP should not carry a Taihu bearer")
	}
}

func TestDockerMCPCallPointsAtWorkspaceBin(t *testing.T) {
	workspace := t.TempDir()
	sb := &localSandbox{workspace: workspace, driver: "docker"}
	env, err := ScriptMCPEnvironment(sb, workspace, []domain.MCPServerDefinition{{
		ID: "tapd", Name: "TAPD", Transport: "http", URL: "https://example.com/tapd",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["PATH"]; ok {
		t.Fatalf("docker MCP env must not set PATH (login shell would replace it with the host PATH), got %q", env["PATH"])
	}
	want := "/workspace/.agentflow/runtime/bin/agentflow"
	if env[mcpCallBinEnv] != want {
		t.Fatalf("AGENTFLOW_BIN = %q want %q", env[mcpCallBinEnv], want)
	}
	if env[mcpCallConfigEnv] != "/workspace/.agentflow/runtime/mcp-servers.json" {
		t.Fatalf("config = %q", env[mcpCallConfigEnv])
	}
	wrapper, err := os.ReadFile(filepath.Join(workspace, ".agentflow", "runtime", "bin", "agentflow"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(wrapper)
	if !strings.Contains(text, "/workspace/.agentflow/runtime/bin/agentflow-helper") {
		t.Fatalf("docker wrapper should exec the workspace helper, got %s", text)
	}
	if strings.Contains(text, "/data/app/skillhub-agent") {
		t.Fatal("docker wrapper must not exec the host platform binary")
	}
	argv := BindMCPCallArgv(sb, []string{"sh", "-lc", "agentflow mcp list"})
	if argv[1] != "-lc" || strings.Contains(argv[2], "agentflow()") {
		t.Fatalf("docker argv is rewritten by dockerWorkspaceExecArgv, BindMCPCallArgv should leave it, got %v", argv)
	}
}

func TestProcessSandboxMCPCallUsesAbsoluteManagedLauncher(t *testing.T) {
	workspace := t.TempDir()
	sb := &localSandbox{workspace: workspace, driver: "bwrap"}
	env, err := PrepareMCPCall(sb, workspace, []domain.MCPServerDefinition{{
		ID: "replace-batch", Name: "Replace Batch", Transport: "stdio", Command: "python3", Args: []string{"server.py"}, FileRefs: []domain.MCPFileRef{{Path: "server.py"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(env[mcpCallConfigEnv])
	if err != nil {
		t.Fatal(err)
	}
	var cfg mcpCallConfigFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(workspace, ".agentflow", "mcp-runtime", "replace-batch", "launch")
	if len(cfg.Servers) != 1 || cfg.Servers[0].Command != want || cfg.Servers[0].CWD != "" || len(cfg.Servers[0].Args) != 0 {
		t.Fatalf("managed stdio config = %#v, want command %q", cfg.Servers, want)
	}
}

func captureMCPCall(t *testing.T, args []string) (string, string, int) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	code := mcpCallMain(args)
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	stdout, err := io.ReadAll(outR)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := io.ReadAll(errR)
	if err != nil {
		t.Fatal(err)
	}
	_ = outR.Close()
	_ = errR.Close()
	return string(stdout), string(stderr), code
}

func TestHookScriptCanCallMCP(t *testing.T) {
	workspace := t.TempDir()
	configRoot := filepath.Join(workspace, ".agentflow", "config", "reviewer")
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	hook := domain.AgentHook{ID: "audit", Event: "Stop", Language: "shell", Script: "agentflow mcp list > hook-mcp.json\n"}
	compiled, err := materializeAgentHooks(workspace, configRoot, ".agentflow/reviewer", "codebuddy", []domain.AgentHook{hook})
	if err != nil || len(compiled) != 1 {
		t.Fatalf("compile hook: %v %#v", err, compiled)
	}
	sb := &localSandbox{workspace: workspace}
	command := CommandSpec{Argv: []string{"sh", "-lc", compiled[0].Command}, CredentialBindings: []CredentialBinding{BindMCP}}
	bound, err := bindCommand(sb, workspace, command, RunCredentials{MCPServers: []domain.MCPServerDefinition{{
		ID: "tapd", Name: "TAPD", MarketID: "70", Transport: "http", URL: "https://example.com/tapd",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Exec(context.Background(), bound, nil); err != nil {
		t.Fatalf("hook exec: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "hook-mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "tapd") {
		t.Fatalf("hook mcp list = %s", raw)
	}
}

func TestMCPCallPreambleDoesNotRewriteProviderOrPythonConfig(t *testing.T) {
	for _, argv := range [][]string{
		{"codex", "exec", "-c", `model_provider="actor_gateway"`, "prompt"},
		{"/usr/bin/python3", "-c", "print('ok')"},
	} {
		got, injected := injectShellPreamble(argv, localMCPCallPreamble)
		if injected || strings.Join(got, "\x00") != strings.Join(argv, "\x00") {
			t.Fatalf("non-shell arguments modified: %v", got)
		}
	}
	got, injected := injectShellPreamble([]string{"/bin/bash", "-lc", "agentflow mcp list"}, localMCPCallPreamble)
	if !injected || !strings.Contains(got[2], "agentflow()") {
		t.Fatal("shell helper was not installed")
	}
}
