package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ConfigureConversationSandbox adds the durable native HOME/cwd mounts to the
// run's existing container. The owner holds its execution fence while doing
// this, and reruns preparation afterwards because a mount change recreates the
// container. Browser and delivery operations subsequently use this same sandbox.
func ConfigureConversationSandbox(ctx context.Context, sandbox Sandbox, root string) error {
	s, ok := unwrapSandbox(sandbox).(*dockerSandbox)
	if !ok {
		return fmt.Errorf("对话工作区仅支持 Docker")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(root, "ws"), 0700); err != nil {
		return err
	}
	s.execGate.Lock()
	defer s.execGate.Unlock()
	if s.requirements.ConversationRoot == root {
		return nil
	}
	s.requirements.ConversationRoot = root
	return s.Recreate(ctx)
}

// Keep security configuration mounted at fixed paths. Anchoring every parent
// directory prevents a native rename of its parent from replacing an ro file.
var conversationGuardFiles = []string{
	"home/.cursor/hooks.json", "home/.gemini/config/hooks.json",
	"home/.kimi-code/config.toml", "home/.config/opencode/opencode.json",
	"home/.config/opencode/plugins/loopforge-approval.js",
	"home/.pi/agent/extensions/loopforge-approval.ts",
	"home/.zcode/cli/config.json",
}

func conversationGuardMounts(root string) ([]string, error) {
	parents := map[string]bool{}
	for _, rel := range conversationGuardFiles {
		file := filepath.Join(root, filepath.FromSlash(rel))
		for parent := filepath.Dir(file); parent != filepath.Join(root, "home"); parent = filepath.Dir(parent) {
			parents[parent] = true
		}
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return nil, err
		}
		if _, err := os.Stat(file); os.IsNotExist(err) {
			if err = os.WriteFile(file, []byte("{}"), 0600); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
	}
	ordered := make([]string, 0, len(parents))
	for parent := range parents {
		ordered = append(ordered, parent)
	}
	sort.Strings(ordered)
	var mounts []string
	for _, parent := range ordered {
		mounts = append(mounts, "-v", parent+":"+parent)
	}
	for _, rel := range conversationGuardFiles {
		file := filepath.Join(root, filepath.FromSlash(rel))
		mounts = append(mounts, "-v", file+":"+file+":ro")
	}
	return mounts, nil
}

// Reuse an image's CLI when available. Generic workspace images predate native
// conversations, so provision the selected official CLI into this container on
// first use. The native HOME remains separate from installer state.
func ensureConversationCLI(ctx context.Context, sandbox Sandbox, provider string) (string, error) {
	s, ok := unwrapSandbox(sandbox).(*dockerSandbox)
	if !ok {
		return "", fmt.Errorf("对话工作区仅支持 Docker")
	}
	binary := conversationBinary(provider)
	setup, err := conversationCLISetup(provider, binary)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// Provision outside the private workspace as root even when the container
	// runs as the service UID. Native CLI execution keeps the container's user.
	output, err := exec.CommandContext(ctx, "docker", "exec", "--user", "0:0", "--workdir", "/", s.name, "sh", "-lc", setup).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("准备 %s 原生CLI失败（工作区镜像需支持 Node.js/npm、Python3 和 setsid）：%w: %s", provider, err, conversationSetupOutput(output))
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	path := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n\x00") {
		return "", fmt.Errorf("CLI安装未返回可执行文件路径")
	}
	return path, nil
}

func conversationCLISetup(provider, binary string) (string, error) {
	packages := map[string]string{
		"codebuddy": "@tencent-ai/codebuddy-code", "codex": "@openai/codex", "claude-code": "@anthropic-ai/claude-code",
		AgentCLIOpenCode: "opencode-ai", AgentCLIKimi: "@moonshot-ai/kimi-code", AgentCLIPi: "@earendil-works/pi-coding-agent", AgentCLIDeepSeek: "@deepseek-ai/dsh",
	}
	installer := ""
	if pkg := packages[provider]; pkg != "" {
		installer = "npm install --no-audit --no-fund --prefix /opt/loopforge/conversation-cli " + shellSingleQuote(pkg) + " >&2\nln -sf ../node_modules/.bin/" + shellSingleQuote(binary) + " /opt/loopforge/conversation-cli/bin/" + shellSingleQuote(binary)
	} else {
		switch provider {
		case AgentCLIZCode:
			// The official CLI is shipped with ZCode or built from its source;
			// it has no standalone npm package. Never install a namesake.
			installer = "echo 'ZCode CLI 未安装；请在工作区镜像中预装官方 ZCode CLI 并将 zcode 加入 PATH' >&2; exit 1"
		case AgentCLICursor:
			installer = "curl -fsSL https://cursor.com/install | HOME=/opt/loopforge/conversation-cli bash >&2"
		case AgentCLIAntigravity:
			installer = "curl -fsSL https://antigravity.google/cli/install.sh | HOME=/opt/loopforge/conversation-cli bash >&2"
		default:
			return "", fmt.Errorf("不支持的会话CLI %s", provider)
		}
	}
	return "set -eu\n" +
		`PATH=/opt/loopforge/conversation-cli/bin:/opt/loopforge/conversation-cli/.local/bin:$PATH
export PATH
if ! command -v python3 >/dev/null 2>&1 || ! command -v setsid >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then apt-get update >&2 && apt-get install -y python3 util-linux >&2
  elif command -v apk >/dev/null 2>&1; then apk add --no-cache python3 util-linux >&2
  else echo 'Python3 and setsid are required for native CLI hooks' >&2; exit 1; fi
fi
` + "if ! command -v " + shellSingleQuote(binary) + " >/dev/null 2>&1; then\nmkdir -p /opt/loopforge/conversation-cli/bin\n" + installer + "\nfi\n" +
		"python3 -c 'import json,urllib.request'\ncommand -v " + shellSingleQuote(binary) + "\n", nil
}

func conversationSetupOutput(output []byte) string {
	if len(output) > 3000 {
		output = output[len(output)-3000:]
	}
	return strings.TrimSpace(string(output))
}
