package runtime

import (
	"bufio"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"agentflow/internal/domain"
)

//go:embed browser_assets/Dockerfile browser_assets/entrypoint.sh browser_assets/cdp-relay.mjs
var browserAssets embed.FS

const (
	BrowserMCPServerID   = "agentflow-browser"
	playwrightMCPPackage = "@playwright/mcp@0.0.83"
	browserMCPPort       = 8931
	browserRunLabel      = "agentflow.browser.run"
	browserOwnerLabel    = "agentflow.browser.owner"
)

// BrowserEnabled lets operators switch the per-run agent browser off with
// AGENTFLOW_BROWSER=off; it is on by default.
func BrowserEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AGENTFLOW_BROWSER"))) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}

// BrowserWantedForExecutor skips the in-process mock, and every executor
// under go test, unless operators force browsers on (AGENTFLOW_BROWSER=always).
func BrowserWantedForExecutor(kind string) bool {
	if !BrowserEnabled() {
		return false
	}
	if _, mock := providerForKind(kind).(mockProvider); mock || testing.Testing() {
		return strings.EqualFold(strings.TrimSpace(os.Getenv("AGENTFLOW_BROWSER")), "always")
	}
	return true
}

type browserEndpoint struct {
	Kind      string
	Container string
	WebSocket string
}

func (e browserEndpoint) dial(ctx context.Context) (cdpTransport, error) {
	if e.Kind == "docker" {
		return dialDockerCDP(ctx, e.Container)
	}
	return dialWebSocketCDP(ctx, e.WebSocket)
}

// BrowserRunning reports whether this host already serves the run browser.
func BrowserRunning(ctx context.Context, runID string) bool {
	_, ok := lookupBrowserEndpoint(ctx, runID)
	return ok
}

// EnsureSandboxBrowser gives the invocation its run browser and returns the
// MCP server the agent drives it through. A docker sandbox gets a sidecar
// sharing its network namespace whatever image the user picked; host drivers
// get a host Chromium. Failures only warn: the node still runs without it.
func EnsureSandboxBrowser(ctx context.Context, sb Sandbox, workspace, runID string, sink EventSink) *domain.MCPServerDefinition {
	if !BrowserEnabled() || strings.TrimSpace(runID) == "" {
		return nil
	}
	var (
		server domain.MCPServerDefinition
		err    error
	)
	switch inner := unwrapSandbox(sb).(type) {
	case *dockerSandbox:
		server, err = inner.ensureBrowser(ctx, runID, sink)
	case nil:
		server, err = ensureHostBrowser(ctx, workspace, runID)
	case *localSandbox:
		server, err = ensureHostBrowser(ctx, inner.workspace, runID)
	case *processSandbox:
		server, err = ensureHostBrowser(ctx, inner.workspace, runID)
	default:
		return nil
	}
	if err != nil {
		if sink != nil {
			sink(domain.TelemetryEvent{Category: "sandbox", Name: "browser.unavailable", Level: "warn", Message: "Agent 浏览器未就绪，本节点不提供浏览器工具：" + err.Error()})
		}
		return nil
	}
	return &server
}

func unwrapSandbox(sb Sandbox) Sandbox {
	for {
		managed, ok := sb.(*managedSandbox)
		if !ok || managed == nil {
			return sb
		}
		sb = managed.Sandbox
	}
}

func browserUsageHint(servers []domain.MCPServerDefinition) string {
	for _, m := range servers {
		if m.ID == BrowserMCPServerID {
			return "\n### Agent 浏览器\n本运行自带一个真实 Chromium（mcp__" + BrowserMCPServerID + "__*，Playwright MCP）。打开网页、点击、填表、截图都用它，不要自己安装或启动浏览器。用户能在任务面板实时看到并接管同一个浏览器；看到页面被用户改动时以当前快照为准。截图等输出写在 `.agentflow/browser/`。\n"
		}
	}
	return ""
}

func dockerBrowserContainerName(base string) string { return base + "-browser" }

var browserImageMu sync.Mutex

func browserImageName() (string, error) {
	if image := strings.TrimSpace(os.Getenv("AGENTFLOW_BROWSER_IMAGE")); image != "" {
		return image, nil
	}
	h := sha256.New()
	err := fs.WalkDir(browserAssets, "browser_assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := browserAssets.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", path, len(raw))
		h.Write(raw)
		return nil
	})
	if err != nil {
		return "", err
	}
	return "agentflow-browser-" + hex.EncodeToString(h.Sum(nil))[:16], nil
}

// EnsureBrowserImage builds the sidecar image once per content hash. It is
// exported for ensure-chromium style pre-warming.
func EnsureBrowserImage(ctx context.Context) (string, error) {
	image, err := browserImageName()
	if err != nil {
		return "", err
	}
	browserImageMu.Lock()
	defer browserImageMu.Unlock()
	if dockerImageExists(ctx, image) {
		return image, nil
	}
	if os.Getenv("AGENTFLOW_BROWSER_IMAGE") != "" {
		if out, err := exec.CommandContext(ctx, "docker", "pull", image).CombinedOutput(); err != nil {
			return "", fmt.Errorf("docker pull %s: %w: %s", image, err, tailOutput(out))
		}
		return image, nil
	}
	dir, err := os.MkdirTemp("", "agentflow-browser-build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	for _, name := range []string{"Dockerfile", "entrypoint.sh", "cdp-relay.mjs"} {
		raw, err := browserAssets.ReadFile("browser_assets/" + name)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			return "", err
		}
	}
	args := []string{"build", "-t", image}
	if registry := strings.TrimSpace(os.Getenv("AGENTFLOW_BROWSER_NPM_REGISTRY")); registry != "" {
		args = append(args, "--build-arg", "NPM_REGISTRY="+registry)
	}
	if out, err := exec.CommandContext(ctx, "docker", append(args, dir)...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker build browser image: %w: %s", err, tailOutput(out))
	}
	return image, nil
}

func tailOutput(out []byte) string {
	text := strings.TrimSpace(string(out))
	if len(text) > 2000 {
		text = "…" + text[len(text)-2000:]
	}
	return text
}

func (s *dockerSandbox) ensureBrowser(ctx context.Context, runID string, sink EventSink) (domain.MCPServerDefinition, error) {
	if err := s.ensureStarted(ctx); err != nil {
		return domain.MCPServerDefinition{}, err
	}
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	image, err := browserImageName()
	if err != nil {
		return domain.MCPServerDefinition{}, err
	}
	if !dockerImageExists(ctx, image) && sink != nil {
		sink(domain.TelemetryEvent{Category: "sandbox", Name: "browser.prepare", Phase: "start", Message: "首次准备 Agent 浏览器镜像（Chromium + Playwright MCP），可能需要几分钟"})
	}
	buildCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	image, err = EnsureBrowserImage(buildCtx)
	cancel()
	if err != nil {
		return domain.MCPServerDefinition{}, err
	}
	name := dockerBrowserContainerName(s.name)
	out, inspectErr := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}|{{.Config.Image}}|{{index .Config.Labels \""+browserRunLabel+"\"}}", name).Output()
	if inspectErr != nil || strings.TrimSpace(string(out)) != "true|"+image+"|"+runID {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
		args := []string{"run", "-d", "--name", name, "--init",
			"--label", browserRunLabel + "=" + runID, "--label", browserOwnerLabel + "=" + s.name,
			"--network", "container:" + s.name, "--cap-drop=ALL", "--security-opt", "no-new-privileges",
			"--shm-size", "512m", "-v", s.workspace + ":/workspace", "-w", "/workspace",
			"-e", "AGENTFLOW_BROWSER_OUTPUT=/workspace/.agentflow/browser", image}
		if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
			return domain.MCPServerDefinition{}, fmt.Errorf("启动浏览器容器失败: %w: %s", err, tailOutput(out))
		}
	}
	if err := waitDockerBrowserReady(ctx, name); err != nil {
		logs, _ := exec.Command("docker", "logs", "--tail", "20", name).CombinedOutput()
		return domain.MCPServerDefinition{}, fmt.Errorf("%w: %s", err, tailOutput(logs))
	}
	return domain.MCPServerDefinition{ID: BrowserMCPServerID, Name: "Agent 浏览器", Transport: "http", URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", browserMCPPort)}, nil
}

func waitDockerBrowserReady(ctx context.Context, name string) error {
	probe := fmt.Sprintf("fetch('http://127.0.0.1:%d/mcp').then(()=>process.exit(0),()=>process.exit(1))", browserMCPPort)
	deadline := time.Now().Add(60 * time.Second)
	for {
		if exec.CommandContext(ctx, "docker", "exec", name, "node", "-e", probe).Run() == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("浏览器 MCP 未在 60s 内就绪")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func removeDockerBrowser(ctx context.Context, base string) {
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", dockerBrowserContainerName(base)).Run()
}

// hostBrowser is a Chromium child of this process for drivers that run the
// agent on the host (local, seatbelt, bwrap, plain runner).
type hostBrowser struct {
	cmd       *exec.Cmd
	port      int
	workspace string
	dir       string
	exited    chan struct{}
}

type hostBrowserRecord struct {
	PID       int    `json:"pid"`
	Owner     int    `json:"owner,omitempty"`
	WebSocket string `json:"webSocket"`
	Port      int    `json:"port"`
	Workspace string `json:"workspace"`
}

var hostBrowsers = struct {
	sync.Mutex
	items  map[string]*hostBrowser
	reaper sync.Once
}{items: map[string]*hostBrowser{}}

var unsafeBrowserKey = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func hostBrowserDir(runID string) string {
	return filepath.Join(os.TempDir(), "agentflow-browser", unsafeBrowserKey.ReplaceAllString(runID, "_"))
}

func ensureHostBrowser(ctx context.Context, workspace, runID string) (domain.MCPServerDefinition, error) {
	hostBrowsers.Lock()
	defer hostBrowsers.Unlock()
	hostBrowsers.reaper.Do(func() { go reapHostBrowsers() })
	b := hostBrowsers.items[runID]
	if b != nil {
		select {
		case <-b.exited:
			b = nil
		default:
		}
	}
	if b == nil {
		binary, err := FindChromium()
		if err != nil {
			return domain.MCPServerDefinition{}, err
		}
		dir := hostBrowserDir(runID)
		profile := filepath.Join(dir, "profile")
		if err := os.MkdirAll(profile, 0o700); err != nil {
			return domain.MCPServerDefinition{}, err
		}
		_ = os.Remove(filepath.Join(profile, "DevToolsActivePort"))
		args := []string{"--headless=new", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
			"--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check", "--disable-background-networking",
			"--window-size=1280,800", "--lang=zh-CN"}
		if goruntime.GOOS == "linux" && os.Geteuid() == 0 {
			args = append(args, "--no-sandbox", "--disable-dev-shm-usage")
		}
		cmd := exec.Command(binary, append(args, "about:blank")...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return domain.MCPServerDefinition{}, fmt.Errorf("启动 Chromium 失败: %w", err)
		}
		b = &hostBrowser{cmd: cmd, workspace: workspace, dir: dir, exited: make(chan struct{})}
		go func() { _ = cmd.Wait(); close(b.exited) }()
		port, path, err := waitDevToolsActivePort(ctx, profile, b.exited)
		if err != nil {
			b.stop()
			return domain.MCPServerDefinition{}, err
		}
		b.port = port
		record, _ := json.Marshal(hostBrowserRecord{PID: cmd.Process.Pid, Owner: os.Getpid(), WebSocket: fmt.Sprintf("ws://127.0.0.1:%d%s", port, path), Port: port, Workspace: workspace})
		if err := os.WriteFile(filepath.Join(dir, "endpoint.json"), record, 0o600); err != nil {
			b.stop()
			return domain.MCPServerDefinition{}, err
		}
		hostBrowsers.items[runID] = b
	}
	command, args := "npx", []string{"-y", playwrightMCPPackage}
	if bin, err := exec.LookPath("playwright-mcp"); err == nil {
		command, args = bin, nil
	}
	args = append(args, "--cdp-endpoint", fmt.Sprintf("http://127.0.0.1:%d", b.port), "--shared-browser-context", "--output-dir", filepath.Join(workspace, ".agentflow", "browser"))
	return domain.MCPServerDefinition{ID: BrowserMCPServerID, Name: "Agent 浏览器", Transport: "stdio", Command: command, Args: args}, nil
}

func waitDevToolsActivePort(ctx context.Context, profile string, exited <-chan struct{}) (int, string, error) {
	deadline := time.Now().Add(20 * time.Second)
	for {
		if raw, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); err == nil {
			scanner := bufio.NewScanner(strings.NewReader(string(raw)))
			var lines []string
			for scanner.Scan() {
				lines = append(lines, strings.TrimSpace(scanner.Text()))
			}
			if len(lines) >= 2 {
				if port, err := strconv.Atoi(lines[0]); err == nil && port > 0 {
					return port, lines[1], nil
				}
			}
		}
		if time.Now().After(deadline) {
			return 0, "", fmt.Errorf("Chromium 未在 20s 内开放调试端口")
		}
		select {
		case <-exited:
			return 0, "", fmt.Errorf("Chromium 启动后立即退出")
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (b *hostBrowser) stop() {
	if b.cmd.Process != nil {
		_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-b.exited:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	_ = os.RemoveAll(b.dir)
}

// stopHostBrowsers ends host browsers that served the given workspace.
func stopHostBrowsers(workspace string) {
	hostBrowsers.Lock()
	var stopping []*hostBrowser
	for runID, b := range hostBrowsers.items {
		if b.workspace == workspace {
			stopping = append(stopping, b)
			delete(hostBrowsers.items, runID)
		}
	}
	hostBrowsers.Unlock()
	for _, b := range stopping {
		b.stop()
	}
}

// reapHostBrowsers catches drivers without a Close hook (plain runner tasks):
// a browser whose workspace is gone has no run left to serve. Chromium runs in
// its own process group, so browsers of a crashed or restarted owner are
// reaped from their endpoint records.
func reapHostBrowsers() {
	reapOrphanHostBrowsers()
	for range time.Tick(time.Minute) {
		reapOrphanHostBrowsers()
		hostBrowsers.Lock()
		var stopping []*hostBrowser
		for runID, b := range hostBrowsers.items {
			_, err := os.Stat(b.workspace)
			exited := false
			select {
			case <-b.exited:
				exited = true
			default:
			}
			if exited || os.IsNotExist(err) {
				stopping = append(stopping, b)
				delete(hostBrowsers.items, runID)
			}
		}
		hostBrowsers.Unlock()
		for _, b := range stopping {
			b.stop()
		}
	}
}

func reapOrphanHostBrowsers() {
	root := filepath.Join(os.TempDir(), "agentflow-browser")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "endpoint.json"))
		if err != nil {
			continue
		}
		var record hostBrowserRecord
		if json.Unmarshal(raw, &record) != nil || record.Owner <= 0 || record.Owner == os.Getpid() || processAlive(record.Owner) {
			continue
		}
		if record.PID > 0 && processAlive(record.PID) {
			_ = syscall.Kill(-record.PID, syscall.SIGKILL)
		}
		_ = os.RemoveAll(dir)
	}
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// FindChromium locates a Chromium-family binary for host drivers.
func FindChromium() (string, error) {
	if path := strings.TrimSpace(os.Getenv("AGENTFLOW_CHROMIUM")); path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("AGENTFLOW_CHROMIUM=%s 不可用: %w", path, err)
		}
		return path, nil
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome-stable", "google-chrome", "chrome", "microsoft-edge"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, pattern := range []string{".cache/ms-playwright/chromium-*/chrome-linux*/chrome", "Library/Caches/ms-playwright/chromium-*/chrome-mac*/Chromium.app/Contents/MacOS/Chromium", "Library/Caches/ms-playwright/chromium-*/chrome-mac*/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"} {
			matches, _ := filepath.Glob(filepath.Join(home, pattern))
			candidates = append(candidates, matches...)
		}
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("本机未找到 Chromium/Chrome；运行 .ci/ensure-chromium.sh 安装，或设置 AGENTFLOW_CHROMIUM")
}

// lookupBrowserEndpoint finds the run browser on this host regardless of
// which local process started it: host browsers leave an endpoint record,
// docker sidecars carry the run label.
func lookupBrowserEndpoint(ctx context.Context, runID string) (browserEndpoint, bool) {
	if raw, err := os.ReadFile(filepath.Join(hostBrowserDir(runID), "endpoint.json")); err == nil {
		var record hostBrowserRecord
		if json.Unmarshal(raw, &record) == nil && record.PID > 0 && syscall.Kill(record.PID, 0) == nil && hostDevToolsAlive(ctx, record.Port) {
			return browserEndpoint{Kind: "local", WebSocket: record.WebSocket}, true
		}
	}
	if !dockerAvailable() {
		return browserEndpoint{}, false
	}
	out, err := exec.CommandContext(ctx, "docker", "ps", "--filter", "label="+browserRunLabel+"="+runID, "--format", "{{.Names}}").Output()
	if err != nil {
		return browserEndpoint{}, false
	}
	for _, name := range strings.Fields(string(out)) {
		return browserEndpoint{Kind: "docker", Container: name}, true
	}
	return browserEndpoint{}, false
}

func hostDevToolsAlive(ctx context.Context, port int) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/json/version", port), nil)
	if err != nil {
		return false
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == 200
}
