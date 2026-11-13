package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
)

// Only a confirmed preparation failure carries this marker. A lost start RPC
// response may hide a running CLI and must never be treated as safe to restart.
var ErrConversationNotStarted = errors.New("conversation process was not started")

// ConversationProcessRequest travels only over the authenticated runner
// control connection. Keys and process input never enter durable task rows.
type ConversationProcessRequest struct {
	Operation  string               `json:"operation"`
	ID         string               `json:"id,omitempty"`
	SessionKey string               `json:"sessionKey"`
	SourceRoot string               `json:"sourceRoot,omitempty"`
	Files      []ConversationFile   `json:"files,omitempty"`
	Command    *ConversationCommand `json:"command,omitempty"`
	Data       string               `json:"data,omitempty"`
	EOF        bool                 `json:"eof,omitempty"`
	After      int64                `json:"after,omitempty"`
	Gateway    string               `json:"gateway,omitempty"`
	Token      string               `json:"token,omitempty"`
}

type ConversationFile struct {
	Rewrite bool   `json:"rewrite,omitempty"`
	Path    string `json:"path"`
	Data    []byte `json:"data"`
	Mode    uint32 `json:"mode"`
}

type ConversationCommand struct {
	Argv        []string
	Provider    string
	Bindings    []CredentialBinding
	Environment map[string]string
	AgentCLI    AgentCLIOptions
	Credentials RunCredentials
	Interactive bool
}

func ConversationCommandFor(command CommandSpec, credentials RunCredentials) *ConversationCommand {
	return &ConversationCommand{Argv: command.Argv, Provider: command.Provider, Bindings: command.CredentialBindings, Environment: CommandEnvironment(command), AgentCLI: command.AgentCLI, Credentials: credentials, Interactive: command.Stdin != nil}
}

type ConversationProcessResult struct {
	Root       string                  `json:"root,omitempty"`
	ID         string                  `json:"id,omitempty"`
	Events     []domain.TelemetryEvent `json:"events,omitempty"`
	Sequence   int64                   `json:"sequence,omitempty"`
	Done       bool                    `json:"done,omitempty"`
	Error      string                  `json:"error,omitempty"`
	NotStarted bool                    `json:"notStarted,omitempty"`
}

type ConversationSandboxFactory func(context.Context, string) (Sandbox, func(), error)

type ConversationToolForward func(context.Context, string, json.RawMessage) (json.RawMessage, error)

type ConversationProcessPool struct {
	assets  map[string]*conversationAssetFence // guarded by mu
	mu      sync.Mutex
	entries map[string]*conversationProcess
	roots   map[string]string
}
type conversationAssetFence struct {
	mu    sync.Mutex
	users int
}

func (p *ConversationProcessPool) lockAssets(ctx context.Context, root string) (func(), error) {
	p.mu.Lock()
	if p.assets == nil {
		p.assets = map[string]*conversationAssetFence{}
	}
	fence := p.assets[root]
	if fence == nil {
		fence = &conversationAssetFence{}
		p.assets[root] = fence
	}
	fence.users++
	p.mu.Unlock()
	releaseRef := func() {
		p.mu.Lock()
		fence.users--
		if fence.users == 0 {
			delete(p.assets, root)
		}
		p.mu.Unlock()
	}
	for {
		if err := ctx.Err(); err != nil {
			releaseRef()
			return nil, err
		}
		if fence.mu.TryLock() {
			return func() { fence.mu.Unlock(); releaseRef() }, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
	}
}

type conversationProcess struct {
	mu         sync.Mutex
	cancel     context.CancelFunc
	input      *os.File
	events     []domain.TelemetryEvent
	base       int64
	done       bool
	err        string
	wake       chan struct{}
	lastPoll   time.Time
	closeRelay func()
	finished   chan struct{}
	root       string
	bytes      int
	notStarted bool
}

// Handle owns the stable native HOME on the executing machine. Only generated
// configuration/assets are synchronized; CLI history stays on that machine.
func (p *ConversationProcessPool) Handle(ctx context.Context, root, workspace string, sandbox *domain.RunnerSandboxSpec, req ConversationProcessRequest, forward ConversationToolForward, factories ...ConversationSandboxFactory) (ConversationProcessResult, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return ConversationProcessResult{}, err
	}
	if req.Operation == "start" {
		return p.start(ctx, root, workspace, sandbox, req, forward, factories...)
	}
	if req.Operation == "prepare" || req.Operation == "sync" || req.Operation == "resetAssets" {
		// Serialize asset writes with reservation of a new process. Checking
		// roots and releasing the fence before a write allows start to race it.
		unlock, lockErr := p.lockAssets(ctx, root)
		if lockErr != nil {
			return ConversationProcessResult{}, lockErr
		}
		defer unlock()
		p.mu.Lock()
		active := p.roots[root] != ""
		p.mu.Unlock()
		if active {
			return ConversationProcessResult{}, fmt.Errorf("会话原生进程仍在运行，不能替换资产或启动新进程")
		}
		if err = ValidateConversationRoot(root); err != nil {
			return ConversationProcessResult{}, err
		}
		if err = os.MkdirAll(filepath.Join(root, "home", "tmp"), 0700); err != nil {
			return ConversationProcessResult{}, err
		}
		if err = os.MkdirAll(filepath.Join(root, "ws"), 0700); err != nil {
			return ConversationProcessResult{}, err
		}
	}
	if req.Operation == "prepare" {
		return ConversationProcessResult{Root: root}, nil
	}
	if req.Operation == "resetAssets" {
		for _, dir := range conversationAssetTrees {
			target := filepath.Join(root, filepath.FromSlash(dir))
			if err := conversationAssetPath(root, target); err != nil {
				return ConversationProcessResult{}, err
			}
			if err := os.RemoveAll(target); err != nil {
				return ConversationProcessResult{}, err
			}
		}
		return ConversationProcessResult{}, nil
	}
	if req.Operation == "sync" {
		total := 0
		for _, file := range req.Files {
			total += len(file.Data)
			allowed, rewrite := ConversationAsset(file.Path)
			if total > 16<<20 || !domain.SafeWorkspaceRelPath(file.Path) || !allowed || file.Rewrite != rewrite {
				return ConversationProcessResult{}, fmt.Errorf("invalid conversation asset")
			}
			target := filepath.Join(root, filepath.FromSlash(file.Path))
			if err := conversationAssetPath(root, target); err != nil {
				return ConversationProcessResult{}, err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return ConversationProcessResult{}, err
			}
			data := file.Data
			if file.Rewrite && req.SourceRoot != "" {
				data = []byte(strings.ReplaceAll(string(data), req.SourceRoot, root))
			}
			if err := os.WriteFile(target, data, os.FileMode(file.Mode)&0777); err != nil {
				return ConversationProcessResult{}, err
			}
			if err := os.Chmod(target, os.FileMode(file.Mode)&0777); err != nil {
				return ConversationProcessResult{}, err
			}
		}
		return ConversationProcessResult{Root: root}, nil
	}
	p.mu.Lock()
	proc := p.entries[req.ID]
	if proc == nil && req.Operation == "stop" {
		// Fence a dispatched start even if its goroutine has not registered yet.
		// Expired IDs cannot start, so the tombstone can be safely reaped.
		expires, idErr := conversationProcessIDExpiry(req.ID)
		if idErr == nil {
			if !time.Now().Before(expires) {
				p.mu.Unlock()
				return ConversationProcessResult{Done: true}, nil
			}
			proc = &conversationProcess{root: root, cancel: func() {}, done: true, wake: make(chan struct{}, 1), finished: make(chan struct{})}
			close(proc.finished)
			if p.entries == nil {
				p.entries = map[string]*conversationProcess{}
			}
			p.entries[req.ID] = proc
			p.reapProcessAfter(req.ID, proc, expires)
		}
	}
	p.mu.Unlock()
	if proc == nil || proc.root != root {
		return ConversationProcessResult{}, fmt.Errorf("原生对话进程已断开，请重试以续接原会话")
	}
	switch req.Operation {
	case "write":
		proc.mu.Lock()
		input := proc.input
		proc.mu.Unlock()
		if input == nil {
			return ConversationProcessResult{}, fmt.Errorf("process has no stdin")
		}
		stopWrite := context.AfterFunc(ctx, func() {
			proc.cancel()
			_ = input.Close()
		})
		defer stopWrite()
		if req.EOF {
			err = input.Close()
		} else {
			_, err = io.WriteString(input, conversationRemap(req.Data, req.SourceRoot, root))
		}
		return ConversationProcessResult{}, err
	case "stop":
		proc.cancel()
		proc.mu.Lock()
		input := proc.input
		proc.mu.Unlock()
		if input != nil {
			_ = input.Close()
		}
		select {
		case <-proc.finished:
			return ConversationProcessResult{Done: true}, nil
		case <-ctx.Done():
			return ConversationProcessResult{}, ctx.Err()
		}
	case "poll":
		if err := ctx.Err(); err != nil {
			return ConversationProcessResult{}, err
		}
		proc.mu.Lock()
		proc.lastPoll = time.Now()
		if len(proc.events) == 0 && !proc.done {
			wake := proc.wake
			proc.mu.Unlock()
			timer := time.NewTimer(time.Second)
			select {
			case <-wake:
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ConversationProcessResult{}, ctx.Err()
			}
			timer.Stop()
			proc.mu.Lock()
		}
		defer proc.mu.Unlock()
		ack := req.After - proc.base
		if ack < 0 || ack > int64(len(proc.events)) {
			return ConversationProcessResult{}, fmt.Errorf("invalid process cursor")
		}
		for _, event := range proc.events[:ack] {
			proc.bytes -= len(event.Message)
		}
		proc.events = proc.events[ack:]
		proc.base = req.After
		n := min(len(proc.events), 256)
		result := ConversationProcessResult{ID: req.ID, Events: append([]domain.TelemetryEvent(nil), proc.events[:n]...), Sequence: proc.base + int64(n), Done: proc.done && n == len(proc.events), Error: proc.err, NotStarted: proc.notStarted}
		return result, nil
	default:
		return ConversationProcessResult{}, fmt.Errorf("invalid conversation process operation")
	}
}

func conversationAssetPath(root, target string) error {
	for path := target; path != root; path = filepath.Dir(path) {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("conversation asset contains symlink")
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(path) == path {
			return fmt.Errorf("conversation asset outside root")
		}
	}
	return nil
}

// IDs are generated before dispatch, so cancellation can address a start whose
// response is lost. Their bounded acceptance window lets us retire cancellation
// tombstones without ever allowing the same delayed start to execute later.
const conversationStartWindow = 5 * time.Minute

func NewConversationProcessID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%016x", time.Now().UnixMilli()) + hex.EncodeToString(random[:]), nil
}

func conversationProcessIDExpiry(id string) (time.Time, error) {
	if len(id) != 48 {
		return time.Time{}, fmt.Errorf("invalid conversation process ID")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return time.Time{}, fmt.Errorf("invalid conversation process ID")
	}
	stamp, err := strconv.ParseInt(id[:16], 16, 64)
	issued := time.UnixMilli(stamp)
	now := time.Now()
	if err != nil || issued.After(now.Add(30*time.Second)) {
		return time.Time{}, fmt.Errorf("expired conversation process ID")
	}
	return issued.Add(conversationStartWindow), nil
}

// Caller holds p.mu. Retain completed IDs throughout their start window.
func (p *ConversationProcessPool) reapProcessAfter(id string, proc *conversationProcess, expires time.Time) {
	time.AfterFunc(max(time.Until(expires), time.Minute), func() {
		p.mu.Lock()
		if p.entries[id] == proc {
			delete(p.entries, id)
		}
		p.mu.Unlock()
	})
}

func (p *ConversationProcessPool) start(ctx context.Context, root, workspace string, spec *domain.RunnerSandboxSpec, req ConversationProcessRequest, forward ConversationToolForward, factories ...ConversationSandboxFactory) (result ConversationProcessResult, err error) {
	if req.Command == nil || len(req.Command.Argv) == 0 {
		return ConversationProcessResult{}, fmt.Errorf("missing conversation command")
	}
	id := req.ID
	if id == "" {
		id, err = NewConversationProcessID()
		if err != nil {
			return ConversationProcessResult{}, err
		}
	}
	expires, err := conversationProcessIDExpiry(id)
	if err != nil {
		return ConversationProcessResult{}, err
	}
	procCtx, cancel := context.WithCancel(ctx)
	proc := &conversationProcess{cancel: cancel, wake: make(chan struct{}, 1), finished: make(chan struct{}), root: root, lastPoll: time.Now()}
	p.mu.Lock()
	// Expiry and reservation share the same lock as stop's expired-ID reply.
	// A start paused across its deadline must not register after stop said Done.
	if !time.Now().Before(expires) {
		p.mu.Unlock()
		cancel()
		return ConversationProcessResult{}, fmt.Errorf("expired conversation process ID")
	}
	if p.entries == nil {
		p.entries = map[string]*conversationProcess{}
	}
	if p.roots == nil {
		p.roots = map[string]string{}
	}
	if p.entries[id] != nil || p.roots[root] != "" {
		p.mu.Unlock()
		cancel()
		return ConversationProcessResult{}, fmt.Errorf("会话原生进程仍在运行或启动已取消")
	}
	p.entries[id] = proc
	p.roots[root] = id
	p.mu.Unlock()
	var sb Sandbox
	var release func()
	var stdin io.Closer
	finish := func(runErr error, notStarted bool) {
		cancel()
		// Cancellation can skip the shell EXIT trap. Clean ephemeral ZCode
		// credentials before publishing completion or releasing this root.
		credentialCleanupErr := cleanupConversationCredentialFiles(root, req.Command.Provider)
		if stdin != nil {
			_ = stdin.Close()
		}
		if proc.closeRelay != nil {
			proc.closeRelay()
		}
		if release != nil {
			release()
		}
		proc.mu.Lock()
		if proc.input != nil {
			_ = proc.input.Close()
		}
		if credentialCleanupErr != nil {
			proc.err = "清理 CLI 临时凭证失败"
			proc.notStarted = notStarted
		}
		if runErr != nil && proc.err == "" {
			proc.err = runErr.Error()
			proc.notStarted = notStarted
		}
		proc.done = true
		proc.mu.Unlock()
		select {
		case proc.wake <- struct{}{}:
		default:
		}
		p.mu.Lock()
		if p.roots[root] == id {
			delete(p.roots, root)
		}
		p.reapProcessAfter(id, proc, expires)
		p.mu.Unlock()
		close(proc.finished)
	}
	started := false
	defer func() {
		if !started {
			finish(err, true)
		}
	}()
	unlock, err := p.lockAssets(procCtx, root)
	if err != nil {
		return ConversationProcessResult{}, err
	}
	defer unlock()
	if err = procCtx.Err(); err != nil {
		return ConversationProcessResult{}, err
	}
	if err = ValidateConversationRoot(root); err != nil {
		return ConversationProcessResult{}, err
	}
	for _, dir := range []string{"home/tmp", "ws"} {
		if err = os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			return ConversationProcessResult{}, err
		}
	}
	if spec != nil {
		if spec.Profile.Driver != "docker" {
			return ConversationProcessResult{}, fmt.Errorf("对话工作区仅支持 Docker")
		}
		if len(factories) > 0 && factories[0] != nil {
			sb, release, err = factories[0](procCtx, root)
		} else {
			resources := SandboxResources{}
			if spec.Dockerfile != nil {
				resources.Dockerfile = *spec.Dockerfile
			}
			if spec.Environment != nil {
				resources.Environment = *spec.Environment
			}
			sb, err = OpenRemoteSandbox(procCtx, "conversation-"+id, workspace, spec.Profile, SandboxRequirements{ConversationRoot: root}, resources)
			if sb != nil {
				release = func() { _ = sb.Close(context.Background()) }
			}
		}
		if err != nil {
			return ConversationProcessResult{}, err
		}
	} else if len(factories) > 0 && factories[0] != nil {
		sb, release, err = factories[0](procCtx, root)
		if err != nil {
			return ConversationProcessResult{}, err
		}
	}
	command := CommandSpec{Argv: append([]string(nil), req.Command.Argv...), Provider: req.Command.Provider, CredentialBindings: req.Command.Bindings, AgentCLI: req.Command.AgentCLI}
	for i, arg := range command.Argv {
		command.Argv[i] = conversationRemap(arg, req.SourceRoot, root)
	}
	env := map[string]string{}
	for key, value := range req.Command.Environment {
		env[key] = conversationRemap(value, req.SourceRoot, root)
	}
	command = WithCommandEnvironment(command, env)
	command = command.WithPrivateHomeDir(filepath.Join(root, "home"))
	command.AgentCLI.SystemPromptFile = conversationRemap(command.AgentCLI.SystemPromptFile, req.SourceRoot, root)
	command.WorkingDir = filepath.Join(root, "ws")
	command.OwnProcessGroup = true
	if spec != nil {
		command.Argv[0] = conversationBinary(command.Provider)
	}
	if forward != nil && req.Token != "" {
		url, closeRelay, err := startConversationRelay(procCtx, req.Token, forward, spec != nil)
		if err != nil {
			return ConversationProcessResult{}, err
		}
		proc.closeRelay = closeRelay
		for i, arg := range command.Argv {
			command.Argv[i] = strings.ReplaceAll(arg, req.Gateway, url)
		}
		for key, value := range command.environment {
			command.environment[key] = strings.ReplaceAll(value, req.Gateway, url)
		}
		if err := replaceConversationGateway(root, req.Gateway, url); err != nil {
			return ConversationProcessResult{}, err
		}
	}
	// Some CLIs continue after a hook interpreter cannot be spawned. Both
	// the platform guard and the native approval hook must fail closed.
	if spec == nil && (command.Provider == AgentCLIKimi || command.Provider == "codex" || command.Provider == AgentCLICursor || command.Provider == AgentCLIAntigravity || command.Provider == AgentCLIZCode) {
		check := exec.CommandContext(procCtx, "python3", "-c", "import json,urllib.request")
		if err = check.Run(); err != nil {
			return ConversationProcessResult{}, fmt.Errorf("原生工具审批需要 Python3：%w", err)
		}
	}
	// These providers load hooks/extensions from persistent HOME in platform
	// mode too. Refresh them on every start so returning from a workspace never
	// runs a stale hook (Pi's codemode wrapper must reach its concrete tools).
	if spec != nil || command.Provider == AgentCLIKimi || command.Provider == AgentCLIPi || command.Provider == AgentCLIOpenCode || command.Provider == AgentCLICursor || command.Provider == AgentCLIAntigravity || command.Provider == AgentCLIZCode {
		command, err = InstallConversationApproval(root, command.Provider, command)
		if err != nil {
			return ConversationProcessResult{}, err
		}
	}
	if spec != nil {
		binary, err := ensureConversationCLI(procCtx, sb, command.Provider, filepath.Join(root, "home"))
		if err != nil {
			return ConversationProcessResult{}, err
		}
		command.Argv[0] = binary
	}
	if sb == nil {
		binary, lookupErr := exec.LookPath(command.Argv[0])
		if lookupErr != nil {
			return ConversationProcessResult{}, fmt.Errorf("执行机器未安装 CLI %s：%w", command.Provider, lookupErr)
		}
		command.Argv[0] = binary
	}
	bindSandbox := sb
	bindWorkspace := filepath.Join(root, "ws")
	if bindSandbox == nil {
		bindSandbox = &localSandbox{id: "conversation", workspace: bindWorkspace}
	} else {
		bindWorkspace = bindSandbox.Workspace()
	}
	nativeArgv := append([]string(nil), command.Argv...)
	command, err = bindCommand(bindSandbox, bindWorkspace, command, req.Command.Credentials)
	if err != nil {
		return ConversationProcessResult{}, err
	}
	if command.Provider == AgentCLIZCode {
		command.Argv = conversationZCodeResumeArgv(root, nativeArgv, command.Argv)
	}
	command.bindingsPrepared = true
	if command.Provider == AgentCLIKimi {
		// Keep the CLI's own session and complete native tool pipeline. Kimi's
		// command-hook API otherwise times out after at most 600s and allows
		// execution on failure. Only our exact hook gets the owned executor.
		command.Argv = append([]string{"node", "--import", filepath.Join(root, "kimi-native-loader.mjs")}, command.Argv...)
	}
	if req.Command.Interactive {
		input, writer, err := os.Pipe()
		if err != nil {
			return ConversationProcessResult{}, err
		}
		proc.mu.Lock()
		proc.input = writer
		proc.mu.Unlock()
		stdin = input
		command.Stdin = input
	}
	if err = procCtx.Err(); err != nil {
		return ConversationProcessResult{}, err
	}
	started = true
	go func() {
		var runErr error
		defer func() { finish(runErr, errors.Is(runErr, errProcessNotStarted)) }()
		watchDone := make(chan struct{})
		defer close(watchDone)
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-watchDone:
					return
				case <-procCtx.Done():
					return
				case <-ticker.C:
					proc.mu.Lock()
					stale := time.Since(proc.lastPoll) > 45*time.Second
					proc.mu.Unlock()
					if stale {
						cancel()
						return
					}
				}
			}
		}()
		sink := func(ev domain.TelemetryEvent) {
			if ev.Name != "process.stdout" && ev.Name != "process.stderr" {
				return
			}
			proc.mu.Lock()
			if proc.bytes+len(ev.Message) <= 8<<20 && len(proc.events) < 32768 {
				proc.events = append(proc.events, ev)
				proc.bytes += len(ev.Message)
			} else {
				proc.err = "原生进程输出超过缓冲限制"
				cancel()
			}
			proc.mu.Unlock()
			select {
			case proc.wake <- struct{}{}:
			default:
			}
		}
		if sb != nil {
			_, runErr = WithSandboxCredentials(sb, req.Command.Credentials).Exec(procCtx, command, sink)
		} else {
			_, runErr = ExecuteLocalCommand(procCtx, filepath.Join(root, "ws"), command, req.Command.Credentials, sink)
		}
	}()
	return ConversationProcessResult{Root: root, ID: id}, nil
}

func cleanupConversationCredentialFiles(root, provider string) error {
	if provider != AgentCLIZCode {
		return nil
	}
	home, err := openAgentCLIHome(AgentCLIHome{Base: root, Rel: "home"})
	if err != nil {
		return err
	}
	defer home.Close()
	dir, err := home.Open(zcodeProviderDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	// This HOME is fenced to one conversation process. Native journals and
	// unrelated CLI configuration are retained; only per-call key files go.
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "provider_config.") && strings.HasSuffix(entry.Name(), ".json") {
			if err := home.Remove(zcodeProviderDir + "/" + entry.Name()); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func conversationBinary(provider string) string {
	switch provider {
	case "codebuddy":
		return "codebuddy"
	case "codex":
		return "codex"
	case "claude-code":
		return "claude"
	default:
		return AgentCLIBinary(provider)
	}
}

func startConversationRelay(ctx context.Context, token string, forward ConversationToolForward, docker bool) (string, func(), error) {
	address := "127.0.0.1:0"
	if docker {
		address = "0.0.0.0:0"
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return "", nil, err
	}
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/agent-tools/"+token+"/mcp" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
		if err != nil || len(data) > 4<<20 {
			http.Error(w, "invalid request", 400)
			return
		}
		result, err := forward(r.Context(), token, data)
		if err != nil {
			http.Error(w, "gateway unavailable", 502)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if len(result) == 0 {
			w.WriteHeader(202)
			return
		}
		_, _ = w.Write(result)
	})}
	go server.Serve(listener)
	stop := func() { _ = server.Close() }
	stopContext := context.AfterFunc(ctx, stop)
	host := "127.0.0.1"
	if docker {
		host, _, err = conversationDockerAccess(ctx)
		if err != nil {
			stopContext()
			stop()
			return "", nil, err
		}
	}
	return "http://" + net.JoinHostPort(host, fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)), func() { stopContext(); stop() }, nil
}

func replaceConversationGateway(root, source, target string) error {
	if source == "" {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "ws" || entry.Name() == "sessions" || entry.Name() == "tmp" || entry.Name() == "projects" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		allowed, rewrite := ConversationAsset(filepath.ToSlash(rel))
		if !allowed || !rewrite {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 4<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), source) {
			return os.WriteFile(path, []byte(strings.ReplaceAll(string(data), source, target)), info.Mode().Perm())
		}
		return nil
	})
}

func conversationRemap(value, source, target string) string {
	if source == "" {
		return value
	}
	return strings.ReplaceAll(value, source, target)
}
