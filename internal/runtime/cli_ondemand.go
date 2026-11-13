package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// CLIs the published platform image does not guarantee. Codex and CodeBuddy
// are baked into that image; the rest are installed only after a person asks
// from a failed conversation. Package names and installer URLs are fixed:
// the request may choose an engine, never a URL or a package.
const (
	cursorInstallScript = "curl -fsSL --proto '=https' --tlsv1.2 https://cursor.com/install | bash"
	agyInstallScript    = "curl -fsSL --proto '=https' --tlsv1.2 https://antigravity.google/cli/install.sh | bash"
)

// DeepSeekHarnessVersion must be named explicitly: every dsh release is a
// prerelease requiring a newer Node than the image ships, so a bare package
// name resolves to no version at all.
const DeepSeekHarnessVersion = "0.2.2-rc.1"

type onDemandCLI struct {
	Engine       string
	Binary       string
	Package      string
	Script       string
	RelaxEngines bool
	// Bundled CLIs ship in the published image, so its sandboxes never
	// install them.
	Bundled bool
}

var onDemandInstallMu sync.Mutex

func onDemandCLICatalog() map[string]onDemandCLI {
	return map[string]onDemandCLI{
		"codex":       {Engine: "codex", Binary: "codex", Package: "@openai/codex", Bundled: true},
		"codebuddy":   {Engine: "codebuddy", Binary: "codebuddy", Package: "@tencent-ai/codebuddy-code", Bundled: true},
		"claude-code": {Engine: "claude-code", Binary: "claude"},
		"opencode":    {Engine: "opencode", Binary: "opencode", Package: "opencode-ai"},
		"cursor":      {Engine: "cursor", Binary: "cursor-agent", Script: cursorInstallScript},
		"agy":         {Engine: "agy", Binary: "agy", Script: agyInstallScript},
		"kimi":        {Engine: "kimi", Binary: "kimi", Package: "@moonshot-ai/kimi-code"},
		"pi":          {Engine: "pi", Binary: "pi", Package: "@earendil-works/pi-coding-agent"},
		"dsh":         {Engine: "dsh", Binary: "dsh", Package: "@deepseek-ai/dsh@" + DeepSeekHarnessVersion, RelaxEngines: true},
	}
}

// OnDemandInstallable reports whether a failed conversation may offer to
// install this CLI. The set is the chat executors, not a caller-supplied name.
func OnDemandInstallable(engine string) bool {
	_, ok := onDemandCLICatalog()[engine]
	return ok
}

// OnDemandCLIPath returns the executable installed for engine, when one is
// already on disk. It does not download anything.
func OnDemandCLIPath(engine string) (string, bool) {
	spec, ok := onDemandCLICatalog()[engine]
	if !ok {
		return "", false
	}
	path := onDemandBinary(spec)
	if path == "" || !fileIsExecutable(path) {
		return "", false
	}
	return path, true
}

func onDemandBinary(spec onDemandCLI) string {
	if spec.Script != "" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return ""
		}
		return filepath.Join(home, ".local", "bin", spec.Binary)
	}
	root := RuntimeCLIDir()
	if root == "" {
		return ""
	}
	if spec.Engine == "claude-code" {
		return claudeCodeBinary(root)
	}
	return filepath.Join(root, spec.Engine, "current", "node_modules", ".bin", spec.Binary)
}

// InstallOnDemandCLI installs one allowlisted CLI and returns its executable
// path. A second call reuses the first installation.
func InstallOnDemandCLI(ctx context.Context, engine string) (string, error) {
	spec, ok := onDemandCLICatalog()[engine]
	if !ok {
		return "", fmt.Errorf("不能从对话安装 %s", engine)
	}
	onDemandInstallMu.Lock()
	defer onDemandInstallMu.Unlock()
	if path, ok := OnDemandCLIPath(engine); ok {
		return path, nil
	}
	var path string
	var err error
	switch {
	case spec.Engine == "claude-code":
		path, err = EnsureClaudeCode(ctx)
	case spec.Script != "":
		path, err = installScriptCLI(ctx, spec)
	default:
		path, err = installNPMCLI(ctx, spec)
	}
	if err != nil {
		return "", fmt.Errorf("安装 %s 失败：%w", onDemandCLILabel(spec.Engine), err)
	}
	return path, nil
}

func onDemandCLILabel(engine string) string {
	switch engine {
	case "codex":
		return "Codex"
	case "codebuddy":
		return "CodeBuddy"
	case "claude-code":
		return "Claude Code"
	default:
		return AgentCLILabel(engine)
	}
}

func installNPMCLI(ctx context.Context, spec onDemandCLI) (string, error) {
	root := RuntimeCLIDir()
	if root == "" {
		return "", errors.New("没有配置 CLI 安装目录")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return "", errors.New("服务器上没有 npm，无法安装")
	}
	parent := filepath.Join(root, spec.Engine)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("准备安装目录失败：%w", err)
	}
	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return "", fmt.Errorf("准备安装目录失败：%w", err)
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o755); err != nil {
		return "", err
	}
	args := []string{"install", "--prefix", stage, "--no-audit", "--no-fund", "--omit=dev", "--save-exact"}
	if spec.RelaxEngines {
		args = append(args, "--engine-strict=false")
	} else {
		args = append(args, "--engine-strict")
	}
	args = append(args, spec.Package)
	cmd := exec.CommandContext(ctx, "npm", args...)
	cmd.Dir = stage
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", errors.New(installCommandError(ctx, err, out))
	}
	staged := filepath.Join(stage, "node_modules", ".bin", spec.Binary)
	if !fileIsExecutable(staged) {
		return "", fmt.Errorf("安装后没有找到可执行文件 %s", spec.Binary)
	}
	target := filepath.Join(parent, "current")
	if err := os.RemoveAll(target); err != nil {
		return "", err
	}
	if err := os.Rename(stage, target); err != nil {
		return "", fmt.Errorf("启用安装结果失败：%w", err)
	}
	return filepath.Join(target, "node_modules", ".bin", spec.Binary), nil
}

func installScriptCLI(ctx context.Context, spec onDemandCLI) (string, error) {
	if _, err := exec.LookPath("bash"); err != nil {
		return "", errors.New("服务器上没有 bash，无法安装")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("无法确定安装用户的主目录")
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", spec.Script)
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", errors.New(installCommandError(ctx, err, out))
	}
	path := filepath.Join(home, ".local", "bin", spec.Binary)
	if !fileIsExecutable(path) {
		return "", fmt.Errorf("安装后没有找到可执行文件 %s", spec.Binary)
	}
	return path, nil
}

func installCommandError(ctx context.Context, err error, out []byte) string {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "安装超时或已取消"
	}
	text := strings.Join(strings.Fields(tailOutput(out)), " ")
	if text == "" {
		text = err.Error()
	}
	if len(text) > 180 {
		text = text[:180] + "…"
	}
	return text
}
