package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/mcpclient"
)

const (
	mcpCallConfigEnv = "AGENTFLOW_MCP_CONFIG"
	mcpCallBinEnv    = "AGENTFLOW_BIN"
)

type mcpCallConfigFile struct {
	Servers []mcpCallServer `json:"servers"`
}

type mcpCallServer struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	MarketID    string            `json:"marketId,omitempty"`
	URL         string            `json:"url"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	CWD         string            `json:"cwd,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Transport   string            `json:"transport"`
	AuthType    string            `json:"authType,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	TimeoutSec  int               `json:"timeoutSec,omitempty"`
	Aliases     []string          `json:"aliases,omitempty"`
}

// BindPersonalMCPHeadersOptional injects the responsible user's Taihu PAT into
// Taihu-authenticated MCPs when a token is present. Unlike BindPersonalMCPHeaders
// it does not fail the whole node when the token is missing; the helper errors
// later if a script actually calls that MCP.
func BindPersonalMCPHeadersOptional(assets domain.AgentAssetBundle, taihuPAT string) domain.AgentAssetBundle {
	out := assets
	out.MCPServers = append([]domain.MCPServerDefinition(nil), assets.MCPServers...)
	token := strings.TrimSpace(taihuPAT)
	for i := range out.MCPServers {
		m := &out.MCPServers[i]
		if strings.TrimSpace(m.AuthType) != MCPAuthTaihuPAT {
			continue
		}
		m.Headers = cloneHeaders(m.Headers)
		for key := range m.Headers {
			if strings.EqualFold(strings.TrimSpace(key), "authorization") {
				delete(m.Headers, key)
			}
		}
		if token != "" {
			m.Headers["Authorization"] = "Bearer " + token
		}
	}
	return out
}

func mcpCallHostDir(workspace string) string {
	return filepath.Join(workspace, ".agentflow", "runtime")
}

func mcpCallGuestPath(rel string) string {
	return filepath.ToSlash(path.Join("/workspace", ".agentflow", "runtime", rel))
}

// PrepareMCPCall writes the script-node helper (`agentflow mcp call`) and bound
// MCP server list into the workspace. Tokens stay in a 0600 file; they are not
// exported as environment variables.
func PrepareMCPCall(sandbox Sandbox, workspace string, servers []domain.MCPServerDefinition) (map[string]string, error) {
	hostRoot := mcpCallHostDir(workspace)
	hostBin := filepath.Join(hostRoot, "bin")
	if err := os.MkdirAll(hostBin, 0o700); err != nil {
		return nil, fmt.Errorf("create mcp call dir: %w", err)
	}
	driver := sandboxDriver(sandbox)
	runtimeServers := prepareMCPServersForDriver(servers, driver)
	for i := range runtimeServers {
		if managedMCPProcessRuntime(servers[i], driver) {
			runtimeServers[i].Command = filepath.Join(workspace, filepath.FromSlash(runtimeServers[i].Command))
		}
	}
	cfg := mcpCallConfigFile{Servers: make([]mcpCallServer, 0, len(runtimeServers))}
	for _, server := range runtimeServers {
		transport := strings.ToLower(strings.TrimSpace(server.Transport))
		if transport == "" {
			transport = "stdio"
		}
		if transport == "http" && strings.TrimSpace(server.URL) == "" {
			continue
		}
		if transport == "stdio" && strings.TrimSpace(server.Command) == "" {
			continue
		}
		cfg.Servers = append(cfg.Servers, mcpCallServer{
			ID:        server.ID,
			Name:      server.Name,
			MarketID:  server.MarketID,
			URL:       server.URL,
			Transport: transport,
			Command:   server.Command, Args: append([]string(nil), server.Args...),
			CWD: MCPRuntimeWorkingDirectory(server), Environment: resolveMap(server.Environment),
			AuthType:   server.AuthType,
			Headers:    cloneHeaders(server.Headers),
			TimeoutSec: server.TimeoutSec,
			Aliases:    mcpCallAliases(server),
		})
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	configHost := filepath.Join(hostRoot, "mcp-servers.json")
	if err := os.WriteFile(configHost, raw, 0o600); err != nil {
		return nil, fmt.Errorf("write mcp call config: %w", err)
	}
	helperHost := filepath.Join(hostBin, "agentflow-helper")
	if err := installMCPCallHelper(helperHost); err != nil {
		return nil, err
	}
	wrapperHost := filepath.Join(hostBin, "agentflow")
	self, _ := os.Executable()
	helperExec := helperHost
	configEnv := configHost
	wrapperEnv := wrapperHost
	fallback := strings.TrimSpace(self)
	if sandboxDriver(sandbox) == "docker" {
		// The container only bind-mounts the workspace. The host platform
		// binary (AGENTFLOW_BIN on the CVM) is not on that filesystem.
		helperExec = mcpCallGuestPath("bin/agentflow-helper")
		configEnv = mcpCallGuestPath("mcp-servers.json")
		wrapperEnv = mcpCallGuestPath("bin/agentflow")
		fallback = ""
	}
	if err := os.WriteFile(wrapperHost, []byte(mcpCallWrapperScript(helperExec, fallback)), 0o700); err != nil {
		return nil, fmt.Errorf("write mcp call wrapper: %w", err)
	}
	if err := os.Chmod(wrapperHost, 0o700); err != nil {
		return nil, err
	}
	return map[string]string{
		mcpCallConfigEnv: configEnv,
		mcpCallBinEnv:    wrapperEnv,
	}, nil
}

func mcpCallWrapperScript(helperPath, fallback string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	if strings.TrimSpace(helperPath) != "" {
		b.WriteString("if [ -x " + shellSingleQuote(helperPath) + " ]; then\n")
		b.WriteString("  exec " + shellSingleQuote(helperPath) + " \"$@\"\n")
		b.WriteString("fi\n")
	}
	if strings.TrimSpace(fallback) != "" {
		b.WriteString("if [ -x " + shellSingleQuote(fallback) + " ]; then\n")
		b.WriteString("  exec " + shellSingleQuote(fallback) + " \"$@\"\n")
		b.WriteString("fi\n")
	}
	b.WriteString("echo \"agentflow: mcp helper is not available in this sandbox\" >&2\n")
	b.WriteString("exit 127\n")
	return b.String()
}

func installMCPCallHelper(dest string) error {
	src, err := os.Executable()
	if err != nil {
		return nil
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		return nil
	}
	if dstInfo, err := os.Stat(dest); err == nil {
		same, linkErr := sameFile(src, dest)
		if linkErr == nil && same {
			return nil
		}
		if dstInfo.Size() == srcInfo.Size() && !srcInfo.ModTime().After(dstInfo.ModTime()) {
			return nil
		}
		_ = os.Remove(dest)
	}
	if err := os.Link(src, dest); err == nil {
		return nil
	}
	if goruntime.GOOS != "linux" {
		// Local macOS/Windows sandboxes re-exec AGENTFLOW_BIN. Linux docker
		// sandboxes cannot see the host executable path, so they need a copy.
		return nil
	}
	return copyFileMode(src, dest, 0o755)
}

func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}

func copyFileMode(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return os.Chmod(dest, mode)
}

func mcpCallAliases(server domain.MCPServerDefinition) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[strings.ToLower(v)] {
			return
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	add(server.ID)
	add(server.MarketID)
	add(server.Name)
	if parsed, err := url.Parse(server.URL); err == nil {
		add(path.Base(strings.TrimRight(parsed.Path, "/")))
	}
	if strings.HasPrefix(server.ID, "mcp-") {
		parts := strings.Split(server.ID, "-")
		if len(parts) >= 2 {
			add(parts[1])
		}
	}
	return out
}

func bindMCPCallEnvironment(sandbox Sandbox, workspace string, environment map[string]string, servers []domain.MCPServerDefinition) error {
	prepared, err := PrepareMCPCall(sandbox, workspace, servers)
	if err != nil {
		return err
	}
	for key, value := range prepared {
		if key == "PATH" {
			continue
		}
		environment[key] = value
	}
	return nil
}

func sandboxIsolatesProcess(sb Sandbox) bool {
	switch sandboxDriver(sb) {
	case "docker", "bwrap", "seatbelt":
		return true
	default:
		return false
	}
}

const localMCPCallPreamble = `agentflow() {
  if [ -z "${AGENTFLOW_BIN-}" ] || [ ! -x "$AGENTFLOW_BIN" ]; then
    echo "agentflow: mcp helper is not available in this sandbox" >&2
    return 127
  fi
  command "$AGENTFLOW_BIN" "$@"
}
`

const isolatedMCPCallPreamble = `if [ -n "${AGENTFLOW_BIN-}" ] && [ -x "$AGENTFLOW_BIN" ]; then
  PATH="$(dirname "$AGENTFLOW_BIN"):$PATH"
  export PATH
fi
`

// BindMCPCallArgv points script/gate shells at the workspace `agentflow` bin.
// Isolated sandboxes prepend that directory after login-shell PATH reset.
// Local unisolated processes never rewrite PATH (the user's execution machine
// would inherit it); they get a shell function instead.
func BindMCPCallArgv(sandbox Sandbox, argv []string) []string {
	if sandboxDriver(sandbox) == "docker" {
		return argv
	}
	if sandboxIsolatesProcess(sandbox) {
		if out, ok := injectShellPreamble(argv, isolatedMCPCallPreamble); ok {
			return out
		}
		return wrapIsolatedMCPCallExec(argv)
	}
	if out, ok := injectShellPreamble(argv, localMCPCallPreamble); ok {
		return out
	}
	return argv
}

func injectShellPreamble(argv []string, preamble string) ([]string, bool) {
	if len(argv) < 3 || strings.TrimSpace(preamble) == "" {
		return argv, false
	}
	// -c also means configuration in Codex and source code in Python. Only
	// shell command strings can receive shell functions; touching a provider's
	// -c value can silently discard its model gateway/credential configuration.
	switch filepath.Base(argv[0]) {
	case "sh", "bash", "zsh", "dash", "ksh", "ash":
	default:
		return argv, false
	}
	out := append([]string{}, argv...)
	for i := 1; i < len(out)-1; i++ {
		switch out[i] {
		case "-c", "-lc", "-cl":
			out[i+1] = preamble + out[i+1]
			return out, true
		}
	}
	return argv, false
}

func wrapIsolatedMCPCallExec(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	return append([]string{"sh", "-c", isolatedMCPCallPreamble + `exec "$0" "$@"`}, argv...)
}

// ScriptMCPEnvironment materializes agentflow mcp call and returns AGENTFLOW_BIN
// plus the config path. It does not rewrite PATH for local/unisolated processes.
func ScriptMCPEnvironment(sandbox Sandbox, workspace string, servers []domain.MCPServerDefinition) (map[string]string, error) {
	env := map[string]string{}
	if err := bindMCPCallEnvironment(sandbox, workspace, env, servers); err != nil {
		return nil, err
	}
	return env, nil
}

func loadMCPCallConfig() (mcpCallConfigFile, error) {
	path := strings.TrimSpace(os.Getenv(mcpCallConfigEnv))
	if path == "" {
		return mcpCallConfigFile{}, fmt.Errorf("未注入 MCP：agentflow mcp call 只在脚本、门禁或 Hook 里可用")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return mcpCallConfigFile{}, fmt.Errorf("read mcp config: %w", err)
	}
	var cfg mcpCallConfigFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return mcpCallConfigFile{}, fmt.Errorf("decode mcp config: %w", err)
	}
	return cfg, nil
}

func matchMCPCallServer(servers []mcpCallServer, query string) (mcpCallServer, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return mcpCallServer{}, fmt.Errorf("请指定要调用的 MCP")
	}
	lower := strings.ToLower(query)
	var matches []mcpCallServer
	for _, server := range servers {
		if mcpCallServerMatches(server, lower) {
			matches = append(matches, server)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, server := range matches {
			names = append(names, server.Name+" ("+server.ID+")")
		}
		return mcpCallServer{}, fmt.Errorf("MCP %q 匹配到多个：%s", query, strings.Join(names, "、"))
	}
	return mcpCallServer{}, fmt.Errorf("当前任务没有 MCP %q", query)
}

func mcpCallServerMatches(server mcpCallServer, lower string) bool {
	if strings.ToLower(server.ID) == lower || strings.ToLower(server.MarketID) == lower || strings.ToLower(server.Name) == lower {
		return true
	}
	for _, alias := range server.Aliases {
		if strings.ToLower(strings.TrimSpace(alias)) == lower {
			return true
		}
	}
	return false
}

func authorizationHeader(headers map[string]string) string {
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), "authorization") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// RunMCPCallCLI handles `agentflow mcp …` (and the same argv on agentflow-runner).
// It returns false when the process should continue with its normal entrypoint.
func RunMCPCallCLI(args []string) bool {
	if len(args) == 0 || args[0] != "mcp" {
		return false
	}
	os.Exit(mcpCallMain(args[1:]))
	return true
}

func mcpCallMain(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, mcpCallUsage)
		return 2
	}
	switch args[0] {
	case "call":
		if err := mcpCallInvoke(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0
	case "tools":
		if err := mcpCallTools(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0
	case "list":
		if err := mcpCallList(); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, mcpCallUsage)
		return 2
	}
}

const mcpCallUsage = `用法:
  agentflow mcp call <server> <tool> [json-args]
  agentflow mcp tools <server>
  agentflow mcp list`

func mcpCallInvoke(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%s", mcpCallUsage)
	}
	cfg, err := loadMCPCallConfig()
	if err != nil {
		return err
	}
	server, err := matchMCPCallServer(cfg.Servers, args[0])
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(server.Transport), "http") && strings.EqualFold(strings.TrimSpace(server.AuthType), MCPAuthTaihuPAT) && authorizationHeader(server.Headers) == "" {
		return fmt.Errorf("MCP %s 需要太湖个人令牌：请先在个人凭证中配置", server.Name)
	}
	tool := strings.TrimSpace(args[1])
	if tool == "" {
		return fmt.Errorf("请指定 MCP 工具名")
	}
	rawArgs := "{}"
	if len(args) >= 3 {
		rawArgs = strings.TrimSpace(args[2])
	}
	if rawArgs == "" {
		rawArgs = "{}"
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &parsed); err != nil {
		return fmt.Errorf("工具参数必须是 JSON 对象: %w", err)
	}
	var text string
	if strings.EqualFold(strings.TrimSpace(server.Transport), "http") {
		client := mcpclient.New(server.URL, server.Headers)
		if server.TimeoutSec > 0 {
			client = client.WithTimeout(time.Duration(server.TimeoutSec) * time.Second)
		}
		text, err = client.CallTool(tool, parsed)
	} else {
		text, err = stdioMCPCall(server, tool, parsed)
	}
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(os.Stdout, text)
	return nil
}

func mcpCallList() error {
	cfg, err := loadMCPCallConfig()
	if err != nil {
		return err
	}
	type row struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		MarketID  string   `json:"marketId,omitempty"`
		Transport string   `json:"transport"`
		URL       string   `json:"url,omitempty"`
		Command   string   `json:"command,omitempty"`
		Aliases   []string `json:"aliases,omitempty"`
	}
	out := make([]row, 0, len(cfg.Servers))
	for _, server := range cfg.Servers {
		out = append(out, row{ID: server.ID, Name: server.Name, MarketID: server.MarketID, Transport: server.Transport, URL: server.URL, Command: server.Command, Aliases: server.Aliases})
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(os.Stdout, string(raw))
	return nil
}

func mcpCallTools(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", mcpCallUsage)
	}
	cfg, err := loadMCPCallConfig()
	if err != nil {
		return err
	}
	server, err := matchMCPCallServer(cfg.Servers, args[0])
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(server.Transport), "http") && strings.EqualFold(strings.TrimSpace(server.AuthType), MCPAuthTaihuPAT) && authorizationHeader(server.Headers) == "" {
		return fmt.Errorf("MCP %s 需要太湖个人令牌：请先在个人凭证中配置", server.Name)
	}
	var names []string
	if strings.EqualFold(strings.TrimSpace(server.Transport), "http") {
		client := mcpclient.New(server.URL, server.Headers)
		if server.TimeoutSec > 0 {
			client = client.WithTimeout(time.Duration(server.TimeoutSec) * time.Second)
		}
		tools, listErr := client.ListTools()
		err = listErr
		for _, tool := range tools {
			if strings.TrimSpace(tool.Name) != "" {
				names = append(names, tool.Name)
			}
		}
	} else {
		names, err = stdioMCPTools(server)
	}
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(names, "", "  ")
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(os.Stdout, string(raw))
	return nil
}

type stdioRPCResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func stdioMCPCall(server mcpCallServer, tool string, args map[string]any) (string, error) {
	raw, err := runStdioMCP(server, "tools/call", map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return "", err
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return strings.TrimSpace(string(raw)), nil
	}
	var blocks []string
	for _, b := range result.Content {
		if b.Type == "text" && b.Text != "" {
			blocks = append(blocks, b.Text)
		}
	}
	if result.IsError {
		return "", fmt.Errorf("mcp tool %s: %s", tool, strings.Join(blocks, "\n"))
	}
	return strings.Join(blocks, "\n"), nil
}

func stdioMCPTools(server mcpCallServer) ([]string, error) {
	raw, err := runStdioMCP(server, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		if strings.TrimSpace(tool.Name) != "" {
			out = append(out, tool.Name)
		}
	}
	return out, nil
}

func runStdioMCP(server mcpCallServer, method string, params any) (json.RawMessage, error) {
	timeout := time.Duration(server.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, server.Command, server.Args...)
	if server.CWD != "" {
		root := filepath.Dir(filepath.Dir(filepath.Dir(strings.TrimSpace(os.Getenv(mcpCallConfigEnv)))))
		cmd.Dir = filepath.Join(root, filepath.FromSlash(server.CWD))
	}
	cmd.Env = appendEnvironment(os.Environ(), server.Environment)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start stdio MCP %s: %w", server.Name, err)
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	write := func(value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdin, "%s\n", raw)
		return err
	}
	read := func(id int64) (json.RawMessage, error) {
		for scanner.Scan() {
			var resp stdioRPCResponse
			if json.Unmarshal(scanner.Bytes(), &resp) != nil || resp.ID != id {
				continue
			}
			if resp.Error != nil {
				return nil, fmt.Errorf("mcp error [%d]: %s", resp.Error.Code, resp.Error.Message)
			}
			return resp.Result, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("stdio MCP exited before response: %s", strings.TrimSpace(stderr.String()))
	}
	if err := write(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "agentflow-script", "version": "1"}}}); err != nil {
		return nil, err
	}
	if _, err := read(1); err != nil {
		return nil, fmt.Errorf("mcp init: %w", err)
	}
	if err := write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	if err := write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": method, "params": params}); err != nil {
		return nil, err
	}
	return read(2)
}
