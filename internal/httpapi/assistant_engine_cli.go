package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"agentflow/internal/managedcli"
	"agentflow/internal/runtime"
)

// assistantCLIInvocation is one prepared chat CLI process. translate turns a
// CLI's own stdout line into CodeBuddy-style stream-json lines; nil means the
// CLI already speaks stream-json. sessionRef is the ID the CLI names in
// "No conversation found with session ID: …".
type assistantCLIInvocation struct {
	command     runtime.CommandSpec
	credentials runtime.RunCredentials
	sessionRef  string
	translate   func(string) []string
	// resumeMissing is true when the CLI cannot resume before it even
	// starts (Codex without a recorded thread for the session).
	resumeMissing bool
	// finish runs after the process exits; ok reports a usable result.
	finish func(ok bool)
}

type assistantEngineInvocationOpts struct {
	Bin, Root, Config, Prompt, Model, Effort, SessionID, SystemPrompt string
	ImagePaths                                                        []string
	ImageInput                                                        bool
	Resume, Workspace, RunnerLogin                                    bool
	// ReviewOnly is a one-shot call without MCP servers or session history,
	// such as a title summary.
	ReviewOnly bool
	ReportOnly bool
	User       runtime.ProviderCredential
}

func assistantEngineConfigDir(engine string) string {
	switch engine {
	case assistantEngineCodex:
		return ".codex"
	case assistantEngineClaude:
		return ".claude"
	default:
		return ".codebuddy"
	}
}

// resolveAssistantEngineBin locates the codex / claude executable the same
// way resolveCodeBuddyBin does: explicit override, managed inventory, PATH,
// well-known install locations, then a CLI installed from a failed conversation.
func resolveAssistantEngineBin(engine string) (string, error) {
	if engine == assistantEngineCodeBuddy {
		bin, err := resolveCodeBuddyBin()
		if err != nil && (strings.Contains(err.Error(), "未找到") || strings.Contains(err.Error(), "尚未安装")) {
			return "", assistantCLIMissingError{engine: engine, binary: "codebuddy"}
		}
		return bin, err
	}
	binary, override, managed := "codex", "AGENTFLOW_CODEX_BIN", "codex"
	if engine == assistantEngineClaude {
		binary, override, managed = "claude", "AGENTFLOW_CLAUDE_BIN", "claude-code"
	} else if runtime.IsAgentCLI(engine) {
		binary, override, managed = runtime.AgentCLIBinary(engine), "AGENTFLOW_"+strings.ToUpper(strings.ReplaceAll(engine, "-", "_"))+"_BIN", engine
	}
	if path := strings.TrimSpace(os.Getenv(override)); path != "" && fileExecutable(path) {
		return path, nil
	}
	if root := strings.TrimSpace(os.Getenv("AGENTFLOW_MANAGED_CLI_ROOT")); root != "" && managedcli.Supports(managed) {
		if inventory, err := managedcli.ReadInventory(root); err == nil {
			if path, ok := inventory.BinaryFor(managed); ok {
				return path, nil
			}
		}
	}
	if path, err := exec.LookPath(binary); err == nil {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	if strings.TrimSpace(home) == "" {
		home = "/root"
	}
	candidates := []string{filepath.Join(home, ".local", "bin", binary)}
	for _, dir := range assistantCLIGlobalDirs {
		candidates = append(candidates, filepath.Join(dir, binary))
	}
	for _, candidate := range candidates {
		if fileExecutable(candidate) {
			return candidate, nil
		}
	}
	if path, ok := runtime.OnDemandCLIPath(engine); ok {
		return path, nil
	}
	return "", assistantCLIMissingError{engine: engine, binary: binary}
}

// assistantCLIGlobalDirs are the global install locations checked after PATH
// and ~/.local/bin. Tests that model a host without a CLI clear it, because
// the platform image installs some CLIs (dsh, codex, ...) into /usr/local/bin.
var assistantCLIGlobalDirs = []string{"/usr/local/bin", "/usr/bin"}

var errAssistantCLIMissing = errors.New("assistant CLI not installed")

// assistantCLIMissingError names the CLI the server lacks; it carries no path
// or credential, so callers show it to the user as is.
type assistantCLIMissingError struct{ engine, binary string }

func (e assistantCLIMissingError) Error() string {
	label := assistantEngineLabel(e.engine)
	if runtime.OnDemandInstallable(e.engine) {
		return fmt.Sprintf("%s（%s）没有安装在当前服务器上。安装后即可在对话里使用。", label, e.engine)
	}
	return fmt.Sprintf("服务器没有安装 %s（%s）。请联系管理员安装后再使用。", label, e.binary)
}

func (e assistantCLIMissingError) Is(target error) bool {
	return target == errAssistantCLIMissing
}

// assistantCredentialError is the authorization failure of a CLI other than CodeBuddy;
// it matches errAssistantCredentialRejected so callers stop retrying.
type assistantCredentialError struct{ engine string }

func (e assistantCredentialError) Error() string {
	label := assistantEngineLabel(e.engine)
	return fmt.Sprintf("%s 拒绝了你的 API Key（401/403），请在「设置」里检查 %s API Key 和 Base URL", label, label)
}

func (e assistantCredentialError) Is(target error) bool {
	return target == errAssistantCredentialRejected
}

func assistantRunnerLoginError(engine string) error {
	return fmt.Errorf("执行机器上的 %s 本机登录态无效或已过期，请检查 Runner 账户的 CLI 登录", assistantEngineLabel(engine))
}

var assistantAuthFailureRe = regexp.MustCompile(`(?i)\b(401|403)\b|unauthorized|invalid api key|invalid x-api-key|authentication_error|incorrect api key`)

func assistantAuthFailureText(text string) bool {
	return assistantAuthFailureRe.MatchString(text)
}

// Claude Code adds tools CodeBuddy does not have. Task is its subagent tool,
// so it stays available for project roles.
const claudeAssistantDisallowedTools = "Bash,PowerShell,Monitor,Write,Edit,MultiEdit,NotebookEdit,WebFetch,WebSearch,KillShell,CronCreate,CronList,CronDelete,EnterWorktree,ExitWorktree,Workflow,ScheduleWakeup,DesignSync,RemoteTrigger,PushNotification"

// assistantClaudeSessionID maps a chat CLI session ID onto the UUID Claude
// Code requires. The mapping is stable, so --resume finds the transcript.
func assistantClaudeSessionID(id string) string {
	sum := sha256.Sum256([]byte("agentflow-assistant/" + id))
	sum[6] = sum[6]&0x0f | 0x40
	sum[8] = sum[8]&0x3f | 0x80
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// readAssistantMCPServers reads the session MCP config written by
// writeAgentToolFiles.
func readAssistantMCPServers(path string) (map[string]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return config.MCPServers, nil
}

func prepareAssistantClaudeInvocation(o assistantEngineInvocationOpts) (assistantCLIInvocation, error) {
	servers, err := readAssistantMCPServers(o.Config)
	if err != nil {
		return assistantCLIInvocation{}, err
	}
	// Claude Code rejects CodeBuddy's per-tool defer_loading extension.
	for _, server := range servers {
		delete(server, "tools")
	}
	mcpPath := filepath.Join(o.Root, "claude-mcp.json")
	settingsPath := filepath.Join(o.Root, "claude-settings.json")
	mcp, _ := json.Marshal(map[string]any{"mcpServers": servers})
	settingsDoc := map[string]any{"permissions": map[string]any{"deny": strings.Split(claudeAssistantDisallowedTools, ",")}}
	if o.Workspace {
		settingsDoc = map[string]any{}
	}
	settings, _ := json.Marshal(settingsDoc)
	if err := os.WriteFile(mcpPath, mcp, 0o600); err != nil {
		return assistantCLIInvocation{}, err
	}
	if err := os.WriteFile(settingsPath, settings, 0o600); err != nil {
		return assistantCLIInvocation{}, err
	}
	sessionID := assistantClaudeSessionID(o.SessionID)
	argv := []string{o.Bin, "-p", "--verbose", "--output-format", "stream-json", "--include-partial-messages",
		"--max-turns", nativeAssistantUnlimitedTurns, "--system-prompt", o.SystemPrompt,
		"--mcp-config", mcpPath, "--strict-mcp-config", "--settings", settingsPath, "--setting-sources", "user",
		"--permission-mode", "bypassPermissions"}
	if o.Model != "" {
		argv = append(argv, "--model", o.Model)
	}
	if !o.Workspace {
		argv = append(argv, "--disallowedTools", claudeAssistantDisallowedTools)
	}
	if effort := strings.TrimSpace(o.Effort); effort != "" {
		argv = append(argv, "--effort", effort)
	}
	if o.ReviewOnly || o.ReportOnly {
		argv = append(argv, "--tools", "", "--no-session-persistence")
	}
	if o.Resume {
		argv = append(argv, "--resume", sessionID)
	} else {
		argv = append(argv, "--session-id", sessionID)
	}
	// --disallowedTools is variadic: end option parsing before the prompt.
	argv = append(argv, "--", o.Prompt)
	command := runtime.CommandSpec{Argv: argv, Provider: "claude-code", CredentialBindings: []runtime.CredentialBinding{runtime.BindClaudeCode}}
	if len(o.ImagePaths) > 0 {
		frameLimit := 0
		if o.RunnerLogin {
			frameLimit = runtime.ClaudeLoginMaxInputLine
		}
		command, err = withAssistantStreamImageInput(command, o.Prompt, o.ImagePaths, frameLimit)
		if err != nil {
			return assistantCLIInvocation{}, err
		}
	}
	command = runtime.PromptOnStdin(command, o.Prompt)
	return assistantCLIInvocation{
		command:     command.WithClaudeBypassAccepted(),
		credentials: runtime.RunCredentials{ClaudeCode: o.User},
		sessionRef:  sessionID,
	}, nil
}

func withAssistantStreamImageInput(command runtime.CommandSpec, prompt string, imagePaths []string, frameLimit int) (runtime.CommandSpec, error) {
	input, err := assistantStreamImageInput(prompt, imagePaths)
	if err != nil {
		return runtime.CommandSpec{}, err
	}
	positional := command.Argv
	// Replace the positional prompt while preserving the delimiter.
	command.Argv = appendNativeArgs(positional[:len(positional)-1], "--input-format", "stream-json")
	command.Stdin = input
	if frameLimit == 0 {
		return command, nil
	}
	limited := &assistantLimitedImageInput{
		Buffer: input, limit: frameLimit, prompt: prompt, positional: positional,
	}
	command.Stdin = limited
	return limited.fit(command, "", "")
}

// A managed Claude login bridge reads one NDJSON frame at a time. Keep the
// paths-only fallback until Runner prepare returns its conversation root.
type assistantLimitedImageInput struct {
	*bytes.Buffer
	limit      int
	prompt     string
	positional []string
}

func (input *assistantLimitedImageInput) fit(command runtime.CommandSpec, source, target string) (runtime.CommandSpec, error) {
	delta := len(target) - len(source)
	remappedSize := func(data []byte) int {
		if source == "" || target == "" || delta == 0 {
			return len(data)
		}
		return len(data) + bytes.Count(data, []byte(source))*delta
	}
	if remappedSize(input.Bytes()) < input.limit {
		return command, nil
	}

	// The prompt includes the staged paths, so the CLI can read the images.
	if runtime.PromptFitsArgv(remappedSize([]byte(input.prompt))) {
		command.Argv = input.positional
		command.Stdin = nil
		return command, nil
	}
	// Large prompts still need stream-json on stdin for the login bridge.
	text, err := assistantStreamImageInput(input.prompt, nil)
	if err != nil {
		return runtime.CommandSpec{}, err
	}
	if remappedSize(text.Bytes()) >= input.limit {
		return runtime.CommandSpec{}, fmt.Errorf("Claude Code 输入超过限制")
	}
	command.Stdin = text
	return command, nil
}

// Codex tools that act outside the platform gateway. The chat runs on the
// control plane, so they are switched off together with the shell. Feature
// flags alone do not stop every built-in tool, so the app-server path also
// installs assistantCodexGuard.
var codexAssistantDisabledFeatures = []string{
	"shell_tool", "unified_exec", "apply_patch_freeform", "multi_agent", "multi_agent_v2",
	"js_repl", "code_mode", "apps", "plugins", "memories", "image_generation",
	"browser_use", "browser_use_external", "computer_use", "in_app_browser",
	"tool_suggest", "skill_search", "view_image", "goals", "collab", "sleep_tool", "worktrees",
}

// codexAssistantGuardArgs installs the platform's PreToolUse denial hook.
// codexAppServer activates its trust in each thread's own config.
func codexAssistantGuardArgs(guardPath string) []string {
	command := "python3 " + strconv.Quote(guardPath)
	hook := "hooks.PreToolUse=[{hooks=[{type=\"command\",command=" + strconv.Quote(command) + ",timeout=5}]}]"
	return []string{"-c", "features.hooks=true", "-c", hook}
}

var codexConfigKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func codexAssistantMCPArgs(servers map[string]map[string]any) []string {
	ids := make([]string, 0, len(servers))
	for id := range servers {
		if codexConfigKeyRe.MatchString(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var args []string
	for _, id := range ids {
		server := servers[id]
		prefix := "mcp_servers." + id + "."
		if url, _ := server["url"].(string); strings.TrimSpace(url) != "" {
			args = append(args, "-c", prefix+"url="+strconv.Quote(url))
		} else if command, _ := server["command"].(string); strings.TrimSpace(command) != "" {
			args = append(args, "-c", prefix+"command="+strconv.Quote(command))
			if list, ok := server["args"].([]any); ok && len(list) > 0 {
				parts := make([]string, 0, len(list))
				for _, value := range list {
					parts = append(parts, strconv.Quote(fmt.Sprint(value)))
				}
				args = append(args, "-c", prefix+"args=["+strings.Join(parts, ",")+"]")
			}
		} else {
			continue
		}
		// The session gateway lists upstream tools before answering, which
		// can take longer than Codex's default MCP startup wait.
		// The platform gateway applies the user's approval mode itself; Codex
		// would otherwise refuse every MCP call under approval_policy=never.
		args = append(args, "-c", prefix+"startup_timeout_sec="+strconv.Itoa(int(agentToolSessionMCPStartupWait.Seconds())), "-c", prefix+"tool_timeout_sec=600", "-c", prefix+`default_tools_approval_mode="approve"`)
	}
	return args
}

var codexThreadsMu sync.Mutex

func codexThreadsPath(root string) string { return filepath.Join(root, "codex-threads.json") }

func readCodexThread(root, sessionID string) string {
	codexThreadsMu.Lock()
	defer codexThreadsMu.Unlock()
	threads := map[string]string{}
	if data, err := os.ReadFile(codexThreadsPath(root)); err == nil {
		_ = json.Unmarshal(data, &threads)
	}
	return threads[sessionID]
}

func writeCodexThread(root, sessionID, threadID string) error {
	codexThreadsMu.Lock()
	defer codexThreadsMu.Unlock()
	threads := map[string]string{}
	if data, err := os.ReadFile(codexThreadsPath(root)); err == nil {
		_ = json.Unmarshal(data, &threads)
	}
	threads[sessionID] = threadID
	data, err := json.Marshal(threads)
	if err != nil {
		return err
	}
	return os.WriteFile(codexThreadsPath(root), data, 0o600)
}

var errAssistantEngineUnsupported = errors.New("此调用未启用 CLI 会话或单次模型调用模式")
