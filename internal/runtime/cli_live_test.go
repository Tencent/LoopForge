//go:build clilive

package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

// Opt-in live check. It runs inside a Linux container that already has the
// CLIs; it does not install anything on the developer machine.
func TestLiveSOPSandboxes(t *testing.T) {
	if os.Getenv("CLI_LIVE") != "1" {
		t.Skip("set CLI_LIVE=1 to run CLI sandbox conversations")
	}
	cfg := loadLiveConfig(t)
	root := os.Getenv("CLI_LIVE_WORKSPACE")
	if root == "" {
		t.Fatal("CLI_LIVE_WORKSPACE is required so a docker sandbox can mount a host path")
	}
	for _, kind := range liveCLIOrder {
		item, ok := cfg[kind]
		if !ok || strings.TrimSpace(item.Key) == "" {
			t.Run(kind, func(t *testing.T) { t.Skip("no key") })
			continue
		}
		for _, driver := range []string{"local", "native", "docker"} {
			t.Run(kind+"/"+driver, func(t *testing.T) {
				runLiveSOP(t, root, kind, driver, item)
			})
		}
	}
}

func runLiveSOP(t *testing.T, root, kind, driver string, item liveCLIConfig) {
	t.Helper()
	binary := AgentCLIBinary(kind)
	if kind == "claude-code" {
		binary = "claude"
	}
	bin, err := exec.LookPath(binary)
	if err != nil {
		t.Fatalf("CLI 不在镜像 PATH 中: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout())
	defer cancel()
	id := "live-" + strings.ReplaceAll(kind, "/", "-") + "-" + driver
	mgr := NewManager(filepath.Join(root, "sop"))
	_ = mgr.DestroyRun(id)
	profile := domain.SandboxProfile{Driver: driver}
	var resources SandboxResources
	if driver == "docker" {
		resources.Dockerfile = domain.DockerfileDefinition{ID: "isolated", Content: IsolatedDockerfile}
	}
	creds := liveRunCredentials(kind, item)
	sb, err := mgr.AcquireRun(ctx, id, profile, SandboxRequirements{}, creds, resources)
	if err != nil {
		t.Fatalf("打开沙箱: %v", err)
	}
	defer func() {
		_ = sb.Close(context.Background())
		_ = mgr.DestroyRun(id)
	}()
	workspace := sb.Workspace()
	agentRoot := ".agentflow/test"
	if err := os.MkdirAll(filepath.Join(workspace, agentRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	system := "Reply with exactly the single word pong. Do not use tools."
	if err := os.WriteFile(filepath.Join(workspace, agentRoot, "agent-system.md"), []byte(system), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, _ := domain.ResolveSandboxDriver(driver, "linux")
	ex := domain.ExecutorDefinition{Kind: kind, Binary: bin, Model: item.Model, Args: liveExecutorArgs(kind)}
	command, err := providerForKind(kind).Prepare(providerInvocation{
		Executor: ex,
		Compiled: compiledConfig{AgentRoot: agentRoot, AgentID: "live", SystemPrompt: system},
		Prompt:   "Reply with exactly the single word pong.",
		Driver:   resolved,
	})
	if err != nil {
		t.Fatalf("准备命令: %v", err)
	}
	// Exec 自己绑定凭证。这里再绑一次会把已经包进 sh -c 的 DeepSeek
	// 参数再次拆开，--patch 会变成 $0。
	out, err := sb.Exec(ctx, command, nil)
	out = redactLive(out)
	if err != nil {
		shown := command.Argv
		if bound, berr := bindCommand(sb, workspace, command, creds); berr == nil {
			shown = bound.Argv
		}
		t.Fatalf("执行失败: %s\nargv=%q\n%s", redactLive(err.Error()), shown, clipLive(out))
	}
	if !strings.Contains(strings.ToLower(out), "pong") {
		t.Fatalf("回复里没有 pong\n%s", clipLive(out))
	}
	t.Logf("ok %s", clipLive(out))
}

func liveRunCredentials(kind string, item liveCLIConfig) RunCredentials {
	cred := ProviderCredential{APIKey: item.Key, BaseURL: item.BaseURL, Protocol: item.Protocol}
	creds := RunCredentials{AgentCLI: map[string]ProviderCredential{}}
	switch kind {
	case "codebuddy":
		creds.CodeBuddy = CodeBuddyCredential{APIKey: item.Key, InternetEnvironment: item.Environment}
	case "codex":
		creds.Codex = cred
	case "claude-code":
		creds.ClaudeCode = cred
	default:
		creds.AgentCLI[kind] = cred
	}
	return creds
}
