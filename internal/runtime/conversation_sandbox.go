package runtime

import (
	"agentflow/internal/managedcli"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ConfigureConversationSandbox adds the durable native HOME/cwd mounts to the
// run's existing container. The owner holds its execution fence while doing
// this, and reruns preparation afterwards because a mount change recreates the
// container. Browser and delivery operations subsequently use this same sandbox.
func ConfigureConversationSandbox(ctx context.Context, sandbox Sandbox, root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(root, "ws"), 0700); err != nil {
		return err
	}
	if p, ok := unwrapSandbox(sandbox).(*processSandbox); ok {
		return configureProcessConversationSandbox(p, root)
	}
	s, ok := unwrapSandbox(sandbox).(*dockerSandbox)
	if !ok {
		return fmt.Errorf("对话工作区只支持 Docker 或 native 沙箱")
	}
	s.execGate.Lock()
	defer s.execGate.Unlock()
	if s.requirements.ConversationRoot == root {
		return nil
	}
	previousRoot := s.requirements.ConversationRoot
	s.requirements.ConversationRoot = root
	if err := s.Recreate(ctx); err != nil {
		s.requirements.ConversationRoot = previousRoot
		return err
	}
	return nil
}

// configureProcessConversationSandbox enables a native process workspace
// (Linux bwrap or macOS seatbelt). On bwrap it binds <root>/home and maps
// <root>/ws onto the run workspace with host paths read-only and the host
// network/Docker socket reachable. On seatbelt macOS lacks mount namespaces, so
// the CLI keeps cwd on the run workspace while seatbelt grants writes only to
// the run workspace, temps, and <root>/home with approval hooks write-protected.
func configureProcessConversationSandbox(s *processSandbox, root string) error {
	if s.driver != "bwrap" && s.driver != "seatbelt" {
		return fmt.Errorf("对话工作区只支持 Docker 或 native 沙箱")
	}
	if err := os.MkdirAll(filepath.Join(root, "home"), 0700); err != nil {
		return err
	}
	s.requirements.ConversationRoot = root
	return nil
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
	parents, files, err := conversationGuardPaths(root)
	if err != nil {
		return nil, err
	}
	var mounts []string
	for _, parent := range parents {
		mounts = append(mounts, "-v", parent+":"+parent)
	}
	for _, file := range files {
		mounts = append(mounts, "-v", file+":"+file+":ro")
	}
	return mounts, nil
}

// conversationGuardPaths creates the guarded files under root and returns the
// parent directories to anchor and the files to mount read-only.
func conversationGuardPaths(root string) (parents, files []string, err error) {
	anchors := map[string]bool{}
	for _, rel := range conversationGuardFiles {
		file := filepath.Join(root, filepath.FromSlash(rel))
		for parent := filepath.Dir(file); parent != filepath.Join(root, "home"); parent = filepath.Dir(parent) {
			anchors[parent] = true
		}
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return nil, nil, err
		}
		if _, err := os.Stat(file); os.IsNotExist(err) {
			if err = os.WriteFile(file, []byte("{}"), 0600); err != nil {
				return nil, nil, err
			}
		} else if err != nil {
			return nil, nil, err
		}
		files = append(files, file)
	}
	for parent := range anchors {
		parents = append(parents, parent)
	}
	sort.Strings(parents)
	return parents, files, nil
}

// Reuse an image's CLI when available. Generic workspace images predate native
// conversations, so provision the selected official CLI into this container on
// first use. The native HOME remains separate from installer state.
func resolveNativeConversationBinary(provider string) (string, error) {
	binary := conversationBinary(provider)
	if path := explicitNativeCLIPath(provider); path != "" {
		return path, nil
	}
	if root := strings.TrimSpace(os.Getenv("AGENTFLOW_MANAGED_CLI_ROOT")); root != "" {
		if inventory, err := managedcli.ReadInventory(root); err == nil {
			if path, ok := inventory.BinaryFor(provider); ok {
				return path, nil
			}
		}
	}
	path, err := exec.LookPath(binary)
	if err == nil {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	if strings.TrimSpace(home) != "" {
		if candidate := filepath.Join(home, ".local", "bin", binary); fileIsExecutable(candidate) {
			return candidate, nil
		}
	}
	for _, candidate := range []string{"/usr/local/bin/" + binary, "/usr/bin/" + binary} {
		if fileIsExecutable(candidate) {
			return candidate, nil
		}
	}
	if onDemand, ok := OnDemandCLIPath(provider); ok {
		return onDemand, nil
	}
	return "", fmt.Errorf("执行机器未安装 CLI %s：%w", provider, err)
}

func ensureConversationCLI(ctx context.Context, sandbox Sandbox, provider, home string) (string, error) {
	if _, native := unwrapSandbox(sandbox).(*processSandbox); native {
		// A native workspace runs the execution machine's own CLI, matching
		// how the platform mode resolves host binaries.
		return resolveNativeConversationBinary(provider)
	}
	s, ok := unwrapSandbox(sandbox).(*dockerSandbox)
	if !ok {
		return "", fmt.Errorf("对话工作区只支持 Docker 或 native 沙箱")
	}
	s.execGate.RLock()
	defer s.execGate.RUnlock()
	return s.ensureDockerCLI(ctx, provider, home)
}

const dockerCLISharedPath = "/opt/loopforge/conversation-cli/bin:/opt/loopforge/conversation-cli/.local/bin"
const dockerCLISharedPathSetup = "PATH=" + dockerCLISharedPath + ":$PATH\nexport PATH\n"

const dockerCLIProbe = `cli_path=$(command -v "$1") || exit 127
[ -f "$cli_path" ] && [ -x "$cli_path" ] || exit 127
printf '%s\n' "$cli_path"
`

// ensureDockerCLI prepares a provider in an existing container. SOP and chat
// callers hold the execution gate so preparation cannot race recreation.
func (s *dockerSandbox) ensureDockerCLI(ctx context.Context, provider, home string) (string, error) {
	binary := conversationBinary(provider)
	setup, err := dockerCLISetup(provider, binary)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// npm providers share the same install prefix. Serialize preparation even
	// when different SOP nodes start different CLIs in the same container.
	s.cliSetupOnce.Do(func() { s.cliSetupGate = make(chan struct{}, 1) })
	select {
	case s.cliSetupGate <- struct{}{}:
		defer func() { <-s.cliSetupGate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	// Probe the actual execution user, including numeric Docker USER values.
	// Root preparation must preserve that identity when the native CLI starts.
	identity, err := exec.CommandContext(ctx, "docker", "exec", "--workdir", "/", s.name, "sh", "-c", "id -u; id -g").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("读取工作区 CLI 用户失败: %w: %s", err, conversationSetupOutput(identity))
	}
	ids := strings.Fields(string(identity))
	if len(ids) != 2 {
		return "", fmt.Errorf("工作区 CLI 用户未返回 UID/GID")
	}
	for _, id := range ids {
		if value, err := strconv.ParseUint(id, 10, 32); err != nil || value == 1<<32-1 {
			return "", fmt.Errorf("工作区 CLI 用户返回了无效 UID/GID")
		}
	}
	// Provision outside the private workspace as root even when the container
	// runs as the service UID. Native CLI execution keeps the container's user.
	toolchain, err := s.toolchainCLIExecEnv(binary)
	if err != nil {
		return "", err
	}
	// Only the task user's PATH and permissions determine whether to install.
	// The root login shell may see a private copy the task cannot execute.
	taskArgs := append(append([]string{"exec", "--workdir", "/"}, toolchain...), s.name, "sh", "-lc")
	taskPathSetup := dockerCLISharedPathSetup + toolchainPathSetup
	probe, err := exec.CommandContext(ctx, "docker", append(taskArgs, taskPathSetup+dockerCLIProbe, "loopforge-cli-available", binary)...).CombinedOutput()
	installMode := "reuse"
	if err != nil {
		if !isDockerCLIMissing(err) {
			return "", fmt.Errorf("探测工作区 CLI %s 失败：%w: %s", binary, err, conversationSetupOutput(probe))
		}
		installMode = "install"
	}

	setupArgs := []string{"exec", "--user", "0:0", "--workdir", "/"}
	// Preparation uses a separate Docker exec as root. Carry the sandbox's
	// configured network settings into that exec as well as normal CLI turns.
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		if value, ok := s.profile.Environment[key]; ok {
			setupArgs = append(setupArgs, "-e", key+"="+value)
		}
	}
	setupArgs = append(append(setupArgs, toolchain...), s.name, "sh", "-lc", setup, "loopforge-cli-setup", ids[0], ids[1], home, installMode)
	output, err := exec.CommandContext(ctx, "docker", setupArgs...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("准备 %s 原生CLI失败（工作区镜像需支持 Node.js/npm 和 Python3）：%w: %s", provider, err, conversationSetupOutput(output))
	}
	check := taskPathSetup + "python3 -c 'import os,pwd; pwd.getpwuid(os.geteuid())' || exit\n" + dockerCLIProbe
	checkArgs := append(taskArgs, check, "loopforge-cli-verify", binary)
	output, err = exec.CommandContext(ctx, "docker", checkArgs...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("验证工作区执行用户可使用 CLI %s 失败：%w: %s", binary, err, conversationSetupOutput(output))
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	path := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n\x00") {
		return "", fmt.Errorf("工作区执行用户未返回 CLI %s 的可执行文件路径", binary)
	}
	return path, nil
}

func dockerCLISetup(provider, binary string) (string, error) {
	spec := onDemandCLICatalog()[provider]
	installer := ""
	if spec.Package != "" {
		flags := ""
		if spec.RelaxEngines {
			flags = "--engine-strict=false "
		}
		installer = "npm install --no-audit --no-fund " + flags + "--prefix /opt/loopforge/conversation-cli " + shellSingleQuote(spec.Package) + " >&2\nln -sf ../node_modules/.bin/" + shellSingleQuote(binary) + " /opt/loopforge/conversation-cli/bin/" + shellSingleQuote(binary)
	} else {
		switch provider {
		case AgentCLIZCode:
			// The official CLI is shipped with ZCode or built from its source;
			// it has no standalone npm package. Never install a namesake.
			installer = "echo 'ZCode CLI 未安装；请在工作区镜像中预装官方 ZCode CLI 并将 zcode 加入 PATH' >&2; exit 1"
		case AgentCLICursor:
			installer = conversationScriptInstaller(cursorInstallerURL)
		case AgentCLIAntigravity:
			installer = conversationScriptInstaller(agyInstallerURL)
		default:
			return "", fmt.Errorf("不支持的会话CLI %s", provider)
		}
	}
	// Setup runs as root in a --cap-drop=ALL container, where GNU tar's default
	// of restoring archive owners fails with EPERM.
	return "set -eu\n" + dockerCLISharedPathSetup + toolchainPathSetup + `export TAR_OPTIONS=--no-same-owner
if ! command -v python3 >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then apt-get update >&2 && apt-get install -y python3 >&2
  elif command -v apk >/dev/null 2>&1; then apk add --no-cache python3 >&2
  else echo 'Python3 is required for native CLI hooks and process supervision' >&2; exit 1; fi
fi
` + conversationCLIIdentitySetup + "if [ \"$4\" = install ]; then\nmkdir -p /opt/loopforge/conversation-cli/bin\n" + installer + "\nfi\n" +
		"python3 -c 'import json,urllib.request'\n", nil
}

// Download the complete HTTPS installer before executing it. Retry the transfer
// once because a TLS connection failure can be transient.
func conversationScriptInstaller(url string) string {
	return "installer_url=" + shellSingleQuote(url) + "\n" + `installer_file=$(mktemp)
trap 'rm -f "$installer_file"' EXIT
download_installer() {
  curl -fsSL --proto '=https' --tlsv1.2 --connect-timeout 10 --max-time 45 -o "$installer_file" "$installer_url"
}
if ! download_installer; then
  sleep 1
  download_installer
fi
HOME=/opt/loopforge/conversation-cli bash "$installer_file" >&2`
}

// Node-based CLIs call getpwuid even when HOME is already set. Numeric
// container users need an NSS identity; do not change an existing image user.
const conversationCLIIdentitySetup = `python3 - "$1" "$2" "$3" <<'LOOPFORGE_CLI_IDENTITY'
import fcntl, pwd, sys
uid, gid = map(int, sys.argv[1:3])
home = sys.argv[3]
try:
    pwd.getpwuid(uid)
except KeyError:
    if not home.startswith("/") or any(c in home for c in "\0:\r\n"):
        sys.exit("Invalid native CLI HOME for passwd identity")
    try:
        with open("/etc/passwd", "a+", encoding="utf-8") as passwd:
            fcntl.flock(passwd, fcntl.LOCK_EX)
            try:
                pwd.getpwuid(uid)
            except KeyError:
                name = "loopforge-cli-" + str(uid)
                names = {entry.pw_name for entry in pwd.getpwall()}
                while name in names:
                    name += "_"
                passwd.seek(0)
                previous = passwd.read()
                prefix = "" if not previous or previous.endswith("\n") else "\n"
                passwd.write(prefix + f"{name}:!:{uid}:{gid}:LoopForge CLI:{home}:/bin/false\n")
                passwd.flush()
    except OSError as err:
        sys.exit(f"Native CLI UID {uid} has no passwd entry; add it to the workspace image or allow /etc/passwd writes during preparation: {err}")
    pwd.getpwuid(uid)
LOOPFORGE_CLI_IDENTITY
`

func conversationSetupOutput(output []byte) string {
	if len(output) > 3000 {
		output = output[len(output)-3000:]
	}
	return strings.TrimSpace(string(output))
}
