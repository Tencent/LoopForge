package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/gitwork"
	"agentflow/internal/runtime"
)

// Daemon is the long-running runner process on a user machine or CVM. It
// only makes outbound connections: HTTPS for durable task data and one WSS
// control channel for low-latency server push (cli-connect.md).
type Daemon struct {
	cfg      Config
	client   *Client
	executor *runtime.ExecutorRunner

	mu                   sync.Mutex
	state                daemonState
	runtimes             []domain.RunnerRuntime
	running              int32
	registerMu           sync.Mutex
	controlMu            sync.RWMutex
	controls             map[string]chan string
	pendingControls      map[string]string
	wsOnline             atomic.Bool
	updateMu             sync.RWMutex
	updateReady          atomic.Bool
	runSandboxMu         sync.Mutex
	runSandboxes         map[string]*runSandbox
	workspacePreparing   sync.Map // run ID -> active environment preflight, not historical file stamp
	workspacePreparation sync.Map
	workspaceTerminals   runtime.TerminalPool
	workspaceGates       sync.Map // run ID -> *sync.RWMutex; fences manual writes locally
	managedTasks         map[string]*managedTask
}

type Config struct {
	ServerURL      string
	BootstrapToken string
	StatePath      string
	Name           string
	Labels         []string
	MaxConcurrency int
	// DefaultWorkspaceRoot is where per-task workspaces are created when a
	// task does not carry its own absolute workspace path.
	DefaultWorkspaceRoot string
	HeartbeatInterval    time.Duration
	LongPollTimeout      time.Duration
	DisableAutoUpdate    bool
	// DisableRunSandboxReuse gives every task its own sandbox instead of one
	// per run and sandbox definition.
	DisableRunSandboxReuse bool
	// ExtraBinaries maps a provider kind to a non-default binary name.
	ExtraBinaries map[string]string
	// ManagedCLIRoot explicitly opts platform runners into managed CLI coordination.
	// Empty leaves ordinary user runners unchanged; it is never inferred from PATH.
	ManagedCLIRoot string
}

type daemonState struct {
	RunnerID     string    `json:"runnerId"`
	SessionToken string    `json:"sessionToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

func DefaultStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agentflow-runner", "state.json"), nil
}

func New(cfg Config) (*Daemon, error) {
	cfg.ManagedCLIRoot = strings.TrimSpace(cfg.ManagedCLIRoot)
	cfg.ServerURL = trimTrailingSlash(cfg.ServerURL)
	if cfg.ServerURL == "" {
		return nil, errors.New("server URL is required")
	}
	if cfg.BootstrapToken == "" {
		return nil, errors.New("bootstrap token is required")
	}
	if cfg.StatePath == "" {
		path, err := DefaultStatePath()
		if err != nil {
			return nil, err
		}
		cfg.StatePath = path
	}
	if cfg.MaxConcurrency < 1 {
		cfg.MaxConcurrency = 1
	}
	if cfg.MaxConcurrency > 32 {
		cfg.MaxConcurrency = 32
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.LongPollTimeout <= 0 {
		cfg.LongPollTimeout = 35 * time.Second
	}
	if cfg.DefaultWorkspaceRoot == "" {
		home, _ := os.UserHomeDir()
		cfg.DefaultWorkspaceRoot = filepath.Join(home, ".agentflow-runner", "workspace")
	}
	if cfg.Name == "" {
		host, _ := os.Hostname()
		cfg.Name = host
	}
	return &Daemon{cfg: cfg, client: NewClient(cfg.ServerURL), executor: runtime.NewExecutorRunner(), controls: map[string]chan string{}, pendingControls: map[string]string{}}, nil
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// Run blocks until ctx is canceled. It registers (or re-registers) the
// machine, starts heartbeat/control loops, and then long-polls for tasks.
func (d *Daemon) Run(ctx context.Context) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	if err := d.ensureRegistered(ctx); err != nil {
		return err
	}
	d.refreshRuntimes(ctx)
	go d.rescanLoop(ctx)
	go d.heartbeatLoop(ctx)
	controlDone := make(chan struct{})
	go func() { defer close(controlDone); d.controlLoop(ctx) }()
	err := d.claimLoop(ctx)
	// Workspace preparation runs on the control connection. Cancel and join it
	// before closing shared sandboxes, including on an automatic-update exit.
	stop()
	<-controlDone
	closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	d.closeAllRunSandboxes(closeCtx)
	cancel()
	d.disconnect(context.Background())
	return err
}

// disconnect updates presence immediately on graceful shutdown but retains
// identity/session/default selection so launchd or systemd restarts do not
// require a fresh one-time bind code.
func (d *Daemon) disconnect(ctx context.Context) {
	d.mu.Lock()
	hasSession := d.state.SessionToken != ""
	d.mu.Unlock()
	if !hasSession {
		return
	}
	deregCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := d.client.Disconnect(deregCtx); err != nil {
		log.Printf("disconnect (server will time out the heartbeat instead): %v", err)
	}
}

// ---------- state persistence ----------

func (d *Daemon) loadState() (daemonState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state.RunnerID != "" {
		return d.state, nil
	}
	raw, err := os.ReadFile(d.cfg.StatePath)
	if err != nil {
		return daemonState{}, err
	}
	var st daemonState
	if err := json.Unmarshal(raw, &st); err != nil {
		return daemonState{}, err
	}
	d.state = st
	return st, nil
}

func (d *Daemon) saveState(st daemonState) error {
	dir := filepath.Dir(d.cfg.StatePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), d.cfg.StatePath); err != nil {
		return err
	}
	d.mu.Lock()
	d.state = st
	d.mu.Unlock()
	return nil
}

// Persist a renewed session well before its old deadline, but avoid rewriting
// state.json on every 30-second heartbeat. Registration uses the same lock so
// a renewal cannot overwrite a newly issued token.
func (d *Daemon) refreshSessionExpiry(expires time.Time) {
	if expires.IsZero() {
		return // older servers do not include the renewal field
	}
	d.registerMu.Lock()
	defer d.registerMu.Unlock()
	d.mu.Lock()
	st := d.state
	d.mu.Unlock()
	if st.SessionToken == "" || !st.ExpiresAt.Before(time.Now().Add(7*24*time.Hour)) {
		return
	}
	st.ExpiresAt = expires
	if err := d.saveState(st); err != nil {
		log.Printf("warning: persist renewed runner session: %v", err)
	}
}

// ---------- registration ----------

func (d *Daemon) ensureRegistered(ctx context.Context) error {
	d.registerMu.Lock()
	defer d.registerMu.Unlock()
	if d.clientSession() != "" {
		return nil
	}
	st, _ := d.loadState()
	if st.SessionToken != "" && (st.ExpiresAt.IsZero() || st.ExpiresAt.After(time.Now().Add(5*time.Minute))) {
		d.client.SetSession(st.SessionToken)
		return nil
	}
	return d.registerLocked(ctx, st.RunnerID)
}

func (d *Daemon) registerLocked(ctx context.Context, runnerID string) error {
	d.refreshRuntimes(ctx)
	name := d.cfg.Name
	if name == "" {
		name = "runner"
	}
	resp, err := d.client.Register(ctx, d.cfg.BootstrapToken, RegisterRequest{
		RunnerID:       runnerID,
		Name:           name,
		OS:             goruntime.GOOS,
		Arch:           goruntime.GOARCH,
		Version:        runnerVersion,
		Labels:         d.cfg.Labels,
		MaxConcurrency: d.cfg.MaxConcurrency,
		Runtimes:       d.currentRuntimes(),
	})
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	d.client.SetSession(resp.SessionToken)
	if err := d.saveState(daemonState{RunnerID: resp.RunnerID, SessionToken: resp.SessionToken, ExpiresAt: resp.ExpiresAt}); err != nil {
		log.Printf("warning: persist runner state: %v", err)
	}
	log.Printf("registered as %s (session expires %s)", resp.RunnerID, resp.ExpiresAt.Format(time.RFC3339))
	return nil
}

func (d *Daemon) clientSession() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state.SessionToken
}

func (d *Daemon) runnerID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state.RunnerID
}

// reregisterWithLock is the recovery path when the server rejects a session.
func (d *Daemon) reregister(ctx context.Context) error {
	d.registerMu.Lock()
	defer d.registerMu.Unlock()
	d.mu.Lock()
	runnerID := d.state.RunnerID
	d.mu.Unlock()
	return d.registerLocked(ctx, runnerID)
}

// ---------- runtime discovery ----------

func (d *Daemon) refreshRuntimes(ctx context.Context) {
	var runtimes []domain.RunnerRuntime
	if d.cfg.ManagedCLIRoot == "" {
		runtimes = DetectRuntimes(ctx, d.cfg.ExtraBinaries)
	} else {
		var err error
		runtimes, err = d.detectManagedRuntimes(ctx)
		if err != nil {
			log.Printf("managed CLI discovery: %v", err)
			return
		}
	}
	d.mu.Lock()
	d.runtimes = runtimes
	d.mu.Unlock()
}

func (d *Daemon) currentRuntimes() []domain.RunnerRuntime {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]domain.RunnerRuntime(nil), d.runtimes...)
}

func (d *Daemon) availableProviders() []string {
	providers := make([]string, 0)
	for _, rt := range d.currentRuntimes() {
		if rt.Status != domain.RuntimeStatusDisabled {
			providers = append(providers, rt.Provider)
		}
	}
	return providers
}

func (d *Daemon) rescanLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refreshRuntimes(ctx)
			d.reapIdleRunSandboxes(ctx, runSandboxIdleTTL)
		}
	}
}

// ---------- heartbeat ----------

func (d *Daemon) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			req := HeartbeatRequest{
				Status:       "idle",
				RunningTasks: int(atomic.LoadInt32(&d.running)),
				Version:      runnerVersion,
				Runtimes:     d.currentRuntimes(),
			}
			if req.RunningTasks > 0 {
				req.Status = "busy"
			}
			hbCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			response, err := d.client.Heartbeat(hbCtx, req)
			cancel()
			if err == ErrUnauthorized {
				if regErr := d.reregister(ctx); regErr != nil {
					log.Printf("re-register failed: %v", regErr)
				}
				continue
			}
			if err != nil {
				log.Printf("heartbeat: %v", err)
			} else if response != nil {
				d.refreshSessionExpiry(response.ExpiresAt)
			}
		}
	}
}

// ---------- claim loop ----------

func (d *Daemon) claimLoop(ctx context.Context) error {
	for {
		if d.updateReady.Load() {
			return ErrUpdateReady
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		providers := d.availableProviders()
		if len(providers) == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(10 * time.Second):
			}
			continue
		}
		capacity := d.cfg.MaxConcurrency - int(atomic.LoadInt32(&d.running))
		if capacity <= 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		task, found, err := d.claimTask(ctx, providers, capacity)
		if errors.Is(err, ErrUpdateReady) {
			return err
		}
		if err == ErrUnauthorized {
			if regErr := d.reregister(ctx); regErr != nil {
				log.Printf("re-register failed: %v", regErr)
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(5 * time.Second):
				}
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("claim: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
			continue
		}
		if !found || task == nil {
			continue
		}
		go d.executeTask(ctx, *task)
	}
}

// Wait for real work before checking artifacts. The follow-up claim is
// non-blocking: if another runner takes that task, wait for a fresh signal
// before claiming a different task.
func (d *Daemon) claimTaskUncoordinated(ctx context.Context, providers []string, capacity int) (*domain.RunnerTask, bool, error) {
	if !d.cfg.DisableAutoUpdate {
		waitCtx, cancel := context.WithTimeout(ctx, d.cfg.LongPollTimeout+15*time.Second)
		ready, legacyTask, err := d.client.WaitForTask(waitCtx, ClaimRequest{AvailableRuntimes: providers, Capacity: capacity})
		cancel()
		if err != nil || !ready {
			return nil, false, err
		}
		if legacyTask != nil {
			atomic.AddInt32(&d.running, 1)
			return legacyTask, true, nil
		}
		d.checkUpdatesBeforeClaim(ctx)
		if d.updateReady.Load() {
			return nil, false, ErrUpdateReady
		}
	}
	d.updateMu.RLock()
	defer d.updateMu.RUnlock()
	if d.updateReady.Load() {
		return nil, false, ErrUpdateReady
	}
	claimCtx, cancel := context.WithTimeout(ctx, d.cfg.LongPollTimeout+15*time.Second)
	defer cancel()
	task, found, err := d.client.Claim(claimCtx, ClaimRequest{AvailableRuntimes: providers, Capacity: capacity, NoWait: !d.cfg.DisableAutoUpdate})
	if found && task != nil && err == nil {
		atomic.AddInt32(&d.running, 1)
	}
	return task, found, err
}

// ---------- task execution ----------

func (d *Daemon) executeTask(ctx context.Context, task domain.RunnerTask) {
	defer atomic.AddInt32(&d.running, -1)
	defer d.releaseManagedTask(task.ID)
	if task.RunID != "" {
		gate := d.workspaceGate(task.RunID)
		gate.RLock()
		defer gate.RUnlock()
	}
	d.registerTaskControl(task.ID)
	defer d.unregisterTaskControl(task.ID)
	log.Printf("task %s claimed (provider=%s attempt=%d)", task.ID, task.Provider, task.Attempt)

	var result execResult
	var artifacts map[string]string
	managed := d.managedTaskState(task.ID)
	switch {
	case managed != nil && managed.err != nil:
		result = execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("pin managed CLI inventory: %v", managed.err)}
	case task.Action == domain.RunnerTaskActionCleanupRun:
		result = d.executeRunCleanup(ctx, task)
	case task.Action == domain.RunnerTaskActionArchivePreview:
		result = d.executePreviewArchive(ctx, task)
	case task.Action == domain.RunnerTaskActionPullWorkBranch, task.Action == domain.RunnerTaskActionPushWorkBranch:
		result = d.executeWorkBranchSync(ctx, task)
	case task.Action == domain.RunnerTaskActionIntegrate:
		result = d.executeIntegrate(ctx, task)
	case task.Action == domain.RunnerTaskActionAlignBaseline:
		result = d.executeAlignBaseline(ctx, task)
	case task.Action != "":
		// Never fall through to the shell/Agent paths for an action this
		// build does not know.
		result = execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("unsupported runner action %q; update the runner", task.Action)}
	case task.Command != "":
		// Terminal task (verification gate / sandbox-side command).
		result = d.executeTerminal(ctx, task)
		if result.status == domain.RunnerTaskSuccess {
			artifacts = collectRunnerInvocationArtifacts(d.taskWorkspace(task), task.Invocation)
		}
	case task.Invocation != nil:
		// Workflow-dispatched Agent invocation: reconstruct the same
		// materialized asset view the control plane builds locally, then run
		// the Agent CLI in the workflow workspace on this machine.
		result, artifacts = d.executeInvocation(ctx, task)
	case task.Provider == mockProviderKind:
		result = d.executeMock(ctx, task)
	default:
		binary, ok := d.taskProviderBinary(task)
		if !ok {
			result = execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("provider %s is no longer available on this runner", task.Provider)}
		} else {
			argv, err := providerArgv(task.Provider, binary, task.Prompt, task.Args)
			if err != nil {
				result = execResult{status: domain.RunnerTaskFailed, errMsg: err.Error()}
			} else {
				result = d.executeProcess(ctx, task, argv, d.taskManagedEnvironment(task))
			}
		}
	}

	completeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := CompleteRequest{Status: result.status, Result: result.result, Error: result.errMsg, Artifacts: artifacts, Outputs: result.outputs}
	if result.status == domain.RunnerTaskFailed || result.status == domain.RunnerTaskSuccess {
		exit := result.exitCode
		req.ExitCode = &exit
	}
	if err := d.client.Complete(completeCtx, task.ID, req); err != nil {
		log.Printf("task %s complete report: %v", task.ID, err)
	}
	log.Printf("task %s finished (%s)", task.ID, result.status)
}

func (d *Daemon) executeRunCleanup(ctx context.Context, task domain.RunnerTask) execResult {
	if err := d.client.StartTask(ctx, task.ID); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("start cleanup: %v", err)}
	}
	root, err := filepath.Abs(d.cfg.DefaultWorkspaceRoot)
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("resolve workspace root: %v", err)}
	}
	target, err := filepath.Abs(d.taskWorkspace(task))
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("resolve cleanup workspace: %v", err)}
	}
	rel, err := filepath.Rel(root, target)
	expected := filepath.Join("runs", task.RunID)
	if err != nil || task.RunID == "" || filepath.Clean(rel) != filepath.Clean(expected) || rel == "." || strings.HasPrefix(rel, "..") {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("refusing unsafe runner workspace cleanup %q", target)}
	}
	d.closeRunSandboxes(ctx, task.RunID)
	if task.Sandbox != nil && strings.EqualFold(task.Sandbox.Profile.Driver, "docker") {
		if err := cleanupDockerWorkspace(ctx, target); err != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("cleanup docker sandbox: %v", err)}
		}
	}
	if err := os.RemoveAll(target); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("remove runner workspace: %v", err)}
	}
	_ = d.client.SendEvents(ctx, task.ID, []domain.RunnerTaskEvent{{Seq: 1, Type: "system", Data: "run sandbox and workspace removed", Ts: time.Now().UTC()}})
	return execResult{status: domain.RunnerTaskSuccess, exitCode: 0, result: "run sandbox and workspace removed"}
}

func cleanupDockerWorkspace(ctx context.Context, workspace string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil
	}
	// A task container mounts runs/<run>/<node>, while this cleanup targets
	// runs/<run>. Enumerate only runner-owned containers, then match their bind
	// mount sources against the run directory so crash-orphaned node containers
	// are removed as well.
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "name=^/agentflow-rt-").CombinedOutput()
	if err != nil {
		return fmt.Errorf("list containers: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, id := range strings.Fields(string(out)) {
		mountOut, mountErr := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{range .Mounts}}{{println .Source}}{{end}}", id).CombinedOutput()
		if mountErr != nil {
			// The container can exit between ps and inspect: closeRunSandboxes
			// removes it, or the daemon already reaped it. A missing container
			// has nothing left to clean.
			if dockerObjectGone(string(mountOut), mountErr) {
				continue
			}
			return fmt.Errorf("inspect container %s mounts: %w: %s", id, mountErr, strings.TrimSpace(string(mountOut)))
		}
		belongsToRun := false
		for _, source := range strings.Fields(string(mountOut)) {
			if workspaceContainsPath(workspace, source) {
				belongsToRun = true
				break
			}
		}
		if !belongsToRun {
			continue
		}
		imageOut, _ := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.Config.Image}}", id).Output()
		image := strings.TrimSpace(string(imageOut))
		if removeOut, removeErr := exec.CommandContext(ctx, "docker", "rm", "-f", id).CombinedOutput(); removeErr != nil {
			if dockerObjectGone(string(removeOut), removeErr) {
				continue
			}
			return fmt.Errorf("remove container %s: %w: %s", id, removeErr, strings.TrimSpace(string(removeOut)))
		}
		if strings.HasPrefix(image, "agentflow-tmp-rt-") {
			_ = exec.CommandContext(ctx, "docker", "rmi", "-f", image).Run()
		}
	}
	return nil
}

// dockerObjectGone reports that id disappeared after docker ps listed it.
func dockerObjectGone(output string, err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(output + "\n" + err.Error())
	return strings.Contains(msg, "no such object") ||
		strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "container not found")
}

func workspaceContainsPath(workspace, candidate string) bool {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return false
	}
	path, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// taskWorkspace resolves the task's workspace. Workflow tasks carry a
// relative path shared by the node's CLI runs and gate commands; manual
// tasks fall back to a per-task directory.
func (d *Daemon) taskWorkspace(task domain.RunnerTask) string {
	ws := strings.TrimSpace(task.Workspace)
	if ws == "" {
		return d.defaultWorkspace(task.ID)
	}
	if filepath.IsAbs(ws) {
		return ws
	}
	return filepath.Join(d.cfg.DefaultWorkspaceRoot, filepath.FromSlash(ws))
}

// executeTerminal runs a command task (`sh -lc <command>`) in the task
// workspace. This is the remote-terminal capability used by workflow scripts
// and verification gates.
func (d *Daemon) executeTerminal(ctx context.Context, task domain.RunnerTask) execResult {
	workspace := d.taskWorkspace(task)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("workspace %s: %v", workspace, err)}
	}
	if task.Invocation != nil {
		if err := checkoutInvocationRepositories(ctx, workspace, task.Invocation, runnerTaskSandboxDriver(task)); err != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("repository checkout: %v", err)}
		}
		if err := writeRunnerHumanContext(workspace, task.Invocation.HumanContext); err != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("write human context: %v", err)}
		}
		if err := prepareInvocationWorkspace(workspace, task.Invocation); err != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: err.Error()}
		}
	}
	command := runtime.CommandSpec{Argv: []string{"sh", "-lc", task.Command}, Provider: "script"}
	var sandbox runtime.Sandbox
	if task.Sandbox != nil {
		var release func()
		var err error
		sandbox, release, err = d.acquireTaskSandbox(ctx, task)
		if err != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("create runner sandbox: %v", err)}
		}
		defer release()
	}
	if task.Invocation != nil {
		servers := task.Invocation.MCPCallServers
		if len(servers) == 0 {
			servers = task.Invocation.Assets.MCPServers
		}
		bound, _, _ := runtime.PrepareMCPServers(domain.AgentAssetBundle{MCPServers: servers, MCPFiles: task.Invocation.Assets.MCPFiles}, d.mcpTarget(task.Invocation.TaihuPAT, false, invocationGitToken(task.Invocation)))
		if err := runtime.ProvisionMCPAssets(ctx, sandbox, workspace, bound, nil); err != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("provision MCP: %v", err)}
		}
		env, err := runtime.ScriptMCPEnvironment(sandbox, workspace, bound.MCPServers)
		if err != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("prepare mcp call: %v", err)}
		}
		command = runtime.WithCommandEnvironment(command, env)
		command.Argv = runtime.BindMCPCallArgv(sandbox, command.Argv)
		gitEnv, err := runtime.GitEnvironment(sandbox, workspace, invocationGitToken(task.Invocation))
		if err != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("prepare git credentials: %v", err)}
		}
		command = runtime.WithCommandEnvironment(command, gitEnv)
	}
	var result execResult
	if sandbox != nil {
		result = d.executeSandboxProcess(ctx, task, sandbox, command)
	} else {
		result = d.executeProcess(ctx, task, command.Argv, runtime.CommandEnvironment(command))
	}
	result.outputs = collectTaskOutputs(workspace, task.Invocation)
	return result
}

// mcpTarget addresses platform-served MCP servers through this runner's own
// server URL, the one platform address known to be reachable from here.
func (d *Daemon) mcpTarget(taihuPAT string, requireTaihuPAT bool, gitTokens ...string) runtime.MCPTarget {
	var gitToken string
	if len(gitTokens) > 0 {
		gitToken = gitTokens[0]
	}
	return runtime.MCPTarget{ServerURL: d.cfg.ServerURL, TaihuPAT: taihuPAT, GitToken: gitToken, RequireTaihuPAT: requireTaihuPAT}
}

// executeInvocation runs a workflow Agent node locally: it materializes the
// Agent asset view (rules/skills/MCP, provider configuration, task prompt)
// in the task workspace, checks out the run's repository when the task
// carries one (the sandbox and workspace live on this machine), and spawns
// the CLI through the same closed command contract the control plane uses.
// On success it uploads the declared artifact files so the engine can
// continue its verification pipeline.
func (d *Daemon) executeInvocation(ctx context.Context, task domain.RunnerTask) (execResult, map[string]string) {
	inv := task.Invocation
	if inv.Concurrent {
		value, _ := d.workspacePreparation.LoadOrStore(task.RunID, &sync.Mutex{})
		lock := value.(*sync.Mutex)
		lock.Lock()
		preparing := true
		defer func() {
			if preparing {
				lock.Unlock()
			}
		}()
		return d.executePreparedInvocation(ctx, task, func() { preparing = false; lock.Unlock() })
	}
	return d.executePreparedInvocation(ctx, task, func() {})
}

func (d *Daemon) executePreparedInvocation(ctx context.Context, task domain.RunnerTask, ready func()) (execResult, map[string]string) {
	inv := task.Invocation
	workspace := d.taskWorkspace(task)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("workspace %s: %v", workspace, err)}, nil
	}
	if err := checkoutInvocationRepositories(ctx, workspace, inv, runnerTaskSandboxDriver(task)); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("repository checkout: %v", err)}, nil
	}
	if err := writeRunnerHumanContext(workspace, inv.HumanContext); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("write human context: %v", err)}, nil
	}
	if err := prepareInvocationWorkspace(workspace, inv); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: err.Error()}, nil
	}
	driver := "runner"
	var sandbox runtime.Sandbox
	var err error
	if task.Sandbox != nil {
		driver = task.Sandbox.Profile.Driver
		var release func()
		sandbox, release, err = d.acquireTaskSandbox(ctx, task)
		if err != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("create runner sandbox: %v", err)}, nil
		}
		defer release()
	}
	boundAssets, _, err := runtime.PrepareMCPServers(inv.Assets, d.mcpTarget(inv.TaihuPAT, true, invocationGitToken(inv)))
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("bind MCP credentials: %v", err)}, nil
	}
	if err := runtime.ProvisionMCPAssets(ctx, sandbox, workspace, boundAssets, nil); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("provision MCP: %v", err)}, nil
	}
	prepared, err := d.executor.PrepareRemote(workspace, runtime.RemoteInvocation{
		InvocationID: inv.InvocationID,
		Executor:     d.invocationExecutor(task),
		Agent:        inv.Agent,
		Assets:       boundAssets,
		Prompt:       task.Prompt,
		Options:      runtime.InvocationOptions{Concurrent: inv.Concurrent, RunID: inv.RunID, NodeID: inv.NodeID, ResumeSessionID: inv.ResumeSessionID, GateResultPath: inv.GateResultPath, Repositories: invocationRepositoryContexts(inv)},
		Driver:       driver,
	})
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("prepare invocation: %v", err)}, nil
	}
	ready()
	if prepared.Builtin {
		// Builtin executors (mock) need no CLI process.
		if err := d.client.StartTask(ctx, task.ID); err != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("start: %v", err)}, nil
		}
		_ = d.client.SendEvents(ctx, task.ID, []domain.RunnerTaskEvent{{Seq: 1, Type: "system", Data: "builtin executor completed in-process", Ts: time.Now().UTC()}})
		return execResult{status: domain.RunnerTaskSuccess, exitCode: 0, result: prepared.BuiltinResult, outputs: collectTaskOutputs(workspace, inv)}, collectRunnerInvocationArtifacts(workspace, inv)
	}
	argv := prepared.Command.Argv
	if sandbox == nil || !strings.EqualFold(task.Sandbox.Profile.Driver, "docker") {
		argv = resolveCommandPath(argv)
	}
	argv, credentialEnv, err := bindInvocationCredentials(inv, prepared.Command.CredentialBindings, argv)
	if err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: err.Error()}, nil
	}
	if sandbox != nil && inv.CredentialSource == domain.RuntimeAuthPersonal && prepared.Command.PrivateHome {
		credentialEnv, err = bindSandboxPrivateHome(workspace, task.Sandbox.Profile.Driver, task.Provider, credentialEnv)
		if err != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: err.Error()}, nil
		}
	}
	commandEnv := runtime.CommandEnvironment(prepared.Command)
	for key, value := range d.taskManagedEnvironment(task) {
		commandEnv[key] = value
	}
	if prepared.Command.HookOutput != "" {
		commandEnv["AGENTFLOW_HOOK_OUTPUT"] = prepared.Command.HookOutput
	}
	for key, value := range credentialEnv {
		commandEnv[key] = value
	}
	if hasCredentialBinding(prepared.Command.CredentialBindings, runtime.BindGit) {
		gitEnv, gitErr := runtime.GitEnvironment(sandbox, workspace, invocationGitToken(inv))
		if gitErr != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("prepare git credentials: %v", gitErr)}, nil
		}
		for key, value := range gitEnv {
			commandEnv[key] = value
		}
	}
	if hasCredentialBinding(prepared.Command.CredentialBindings, runtime.BindMCP) {
		servers := inv.MCPCallServers
		if len(servers) == 0 {
			servers = boundAssets.MCPServers
		} else {
			gateAssets, _, _ := runtime.PrepareMCPServers(domain.AgentAssetBundle{MCPServers: servers}, d.mcpTarget(inv.TaihuPAT, false, invocationGitToken(inv)))
			servers = gateAssets.MCPServers
		}
		mcpEnv, mcpErr := runtime.ScriptMCPEnvironment(sandbox, workspace, servers)
		if mcpErr != nil {
			return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("prepare mcp call: %v", mcpErr)}, nil
		}
		for key, value := range mcpEnv {
			commandEnv[key] = value
		}
		argv = runtime.BindMCPCallArgv(sandbox, argv)
	}
	var result execResult
	if sandbox != nil {
		command := prepared.Command
		command.Argv = argv
		command = runtime.WithCommandEnvironment(command, commandEnv)
		result = d.executeSandboxProcess(ctx, task, sandbox, command)
	} else {
		result = d.executeProcess(ctx, task, argv, commandEnv)
	}
	result.outputs = collectTaskOutputs(workspace, inv)
	if result.status != domain.RunnerTaskSuccess {
		return result, nil
	}
	artifacts := collectRunnerInvocationArtifacts(workspace, inv)
	return result, artifacts
}

func writeRunnerHumanContext(workspace, context string) error {
	path := filepath.Join(workspace, "human-context.md")
	context = strings.TrimSpace(context)
	if context == "" {
		return nil
	}
	return os.WriteFile(path, []byte("# 人工补充上下文\n\n"+context+"\n"), 0o644)
}

func collectRunnerInvocationArtifacts(workspace string, inv *domain.RunnerInvocation) map[string]string {
	if inv == nil {
		return nil
	}
	base := workspace
	if dir := safeRunnerRepoDir(inv.ArtifactDirectory); dir != "" {
		base = filepath.Join(workspace, dir)
	}
	artifacts := map[string]string{}
	for _, contract := range inv.Artifacts {
		if strings.TrimSpace(contract.Path) == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(contract.Path)))
		if err != nil || len(data) > maxArtifactUploadBytes {
			continue
		}
		artifacts[contract.Name] = string(data)
	}
	if len(artifacts) == 0 {
		return nil
	}
	return artifacts
}

func invocationRepositoryContexts(inv *domain.RunnerInvocation) []domain.RepositoryContext {
	if inv == nil {
		return nil
	}
	repositories := inv.Repositories
	if len(repositories) == 0 && inv.Repository != nil {
		repositories = []domain.RunnerRepository{*inv.Repository}
	}
	out := make([]domain.RepositoryContext, 0, len(repositories))
	for _, repository := range repositories {
		out = append(out, domain.RepositoryContext{
			RepositoryID: repository.RepositoryID,
			Name:         repository.Name,
			WorkspaceDir: repository.Dir,
		})
	}
	return out
}

func bindSandboxPrivateHome(workspace, driver, provider string, env map[string]string) (map[string]string, error) {
	home := filepath.Join(workspace, ".agentflow", "private-home", provider)
	tmp := filepath.Join(workspace, ".agentflow", "tmp", provider)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, fmt.Errorf("create sandbox private home: %w", err)
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, fmt.Errorf("create sandbox private temp: %w", err)
	}
	if env == nil {
		env = map[string]string{}
	}
	if strings.EqualFold(driver, "docker") {
		env["HOME"] = filepath.ToSlash(filepath.Join("/workspace", ".agentflow", "private-home", provider))
		env["TMPDIR"] = filepath.ToSlash(filepath.Join("/workspace", ".agentflow", "tmp", provider))
	} else {
		env["HOME"] = home
		env["TMPDIR"] = tmp
	}
	switch provider {
	case "codex":
		if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
			return nil, err
		}
		env["CODEX_HOME"] = filepath.ToSlash(filepath.Join(env["HOME"], ".codex"))
	case "claude-code":
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
			return nil, err
		}
		env["CLAUDE_CONFIG_DIR"] = filepath.ToSlash(filepath.Join(env["HOME"], ".claude"))
	}
	return env, nil
}

// bindInvocationCredentials maps the closed credential bindings onto the
// task process. Local mode leaves the CLI login state untouched; personal
// mode injects only the selected provider's saved credential.
func bindInvocationCredentials(inv *domain.RunnerInvocation, bindings []runtime.CredentialBinding, argv []string) ([]string, map[string]string, error) {
	env := map[string]string{}
	personal := inv.CredentialSource == domain.RuntimeAuthPersonal
	for _, binding := range bindings {
		switch binding {
		case runtime.BindCodeBuddy:
			key := strings.TrimSpace(inv.CodeBuddyAPIKey)
			if key == "" {
				return nil, nil, errors.New("CodeBuddy 个人 API Key 未配置")
			}
			env["CODEBUDDY_API_KEY"] = key
			if net := strings.TrimSpace(inv.CodeBuddyInternetEnvironment); net != "" {
				env["CODEBUDDY_INTERNET_ENVIRONMENT"] = net
			}
		case runtime.BindCodex:
			if !personal {
				continue
			}
			key := strings.TrimSpace(inv.CodexAPIKey)
			if key == "" {
				return nil, nil, errors.New("Codex 个人 API Key 未配置")
			}
			env["CODEX_API_KEY"] = key
			baseURL := strings.TrimSpace(inv.CodexBaseURL)
			if baseURL == "" {
				baseURL = "https://api.openai.com/v1"
			}
			if len(argv) < 2 {
				return nil, nil, errors.New("invalid Codex invocation")
			}
			config := []string{
				"-c", `model_provider="agentflow_user"`,
				"-c", `model_providers.agentflow_user.name="User API"`,
				"-c", "model_providers.agentflow_user.base_url=" + strconv.Quote(baseURL),
				"-c", `model_providers.agentflow_user.env_key="CODEX_API_KEY"`,
				"-c", `model_providers.agentflow_user.wire_api="responses"`,
				"-c", "model_providers.agentflow_user.requires_openai_auth=false",
			}
			args := append([]string{}, argv[:len(argv)-1]...)
			args = append(args, config...)
			argv = append(args, argv[len(argv)-1])
		case runtime.BindClaudeCode:
			if !personal {
				continue
			}
			key := strings.TrimSpace(inv.ClaudeCodeAPIKey)
			if key == "" {
				return nil, nil, errors.New("Claude Code 个人 API Key 未配置")
			}
			env["ANTHROPIC_API_KEY"] = key
			baseURL := strings.TrimSpace(inv.ClaudeCodeBaseURL)
			if baseURL == "" {
				baseURL = "https://api.anthropic.com"
			}
			env["ANTHROPIC_BASE_URL"] = baseURL
		case runtime.BindGit, runtime.BindMCP:
			continue
		}
	}
	return argv, env, nil
}

// invocationGitToken is the token child processes use for git remotes; every
// repository of one run carries the same responsible user's token.
func invocationGitToken(inv *domain.RunnerInvocation) string {
	if inv == nil {
		return ""
	}
	for _, repo := range inv.Repositories {
		if token := strings.TrimSpace(repo.GitToken); token != "" {
			return token
		}
	}
	if inv.Repository != nil {
		return strings.TrimSpace(inv.Repository.GitToken)
	}
	return ""
}

func hasCredentialBinding(bindings []runtime.CredentialBinding, want runtime.CredentialBinding) bool {
	for _, binding := range bindings {
		if binding == want {
			return true
		}
	}
	return false
}

// checkoutInvocationRepositories checks out every repository the invocation
// carries: each into its Dir under the task workspace. Empty Dir is only
// kept for persisted legacy invocations that already checked out at the
// workspace root. Later invocations and gates reuse the existing checkout
// (once per workspace).
func checkoutInvocationRepositories(ctx context.Context, workspace string, inv *domain.RunnerInvocation, driver string) error {
	if inv == nil {
		return nil
	}
	repos := inv.Repositories
	if len(repos) == 0 && inv.Repository != nil {
		if strings.TrimSpace(inv.Repository.CloneURL) == "" {
			return nil
		}
		repos = []domain.RunnerRepository{*inv.Repository}
	}
	for i := range repos {
		repo := repos[i]
		if strings.TrimSpace(repo.CloneURL) == "" {
			continue
		}
		target := workspace
		if dir := safeRunnerRepoDir(repo.Dir); dir != "" {
			target = filepath.Join(workspace, dir)
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create repository directory %s: %w", dir, err)
			}
		}
		if err := checkoutRepository(ctx, target, &repo); err != nil {
			return err
		}
		if err := gitwork.RecordBaseline(target, repo.Ref); err != nil {
			return err
		}
		if err := configureRepositoryGitIdentity(ctx, target, inv.GitUserName, inv.GitUserEmail); err != nil {
			return err
		}
		if err := runtime.ProtectProjectAgentGitIndex(ctx, target, driver); err != nil {
			return fmt.Errorf("protect repository Agent configuration in %s: %w", repo.Dir, err)
		}
	}
	return nil
}

func configureRepositoryGitIdentity(ctx context.Context, workspace, name, email string) error {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	if name == "" || email == "" {
		return nil
	}
	for _, item := range []struct {
		key, value string
	}{
		{key: "user.name", value: name},
		{key: "user.email", value: email},
	} {
		cmd := exec.CommandContext(ctx, "git", "-C", workspace, "config", "--local", item.key, item.value)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("configure git %s: %w (%s)", item.key, err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func runnerTaskSandboxDriver(task domain.RunnerTask) string {
	if task.Sandbox == nil || strings.TrimSpace(task.Sandbox.Profile.Driver) == "" {
		return "local"
	}
	return strings.TrimSpace(task.Sandbox.Profile.Driver)
}

// safeRunnerRepoDir normalizes a repository workspace dir and rejects path
// traversal, absolute paths and the workspace root itself.
func safeRunnerRepoDir(dir string) string {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(dir)))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return ""
	}
	return clean
}

// checkoutRepository clones the run's repository into the task workspace
// (once per workspace; later invocations and gates reuse the checkout). The
// token, when present, authenticates private HTTPS remotes.
func checkoutRepository(ctx context.Context, workspace string, repo *domain.RunnerRepository) error {
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err == nil {
		return nil
	}
	cloneCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	url := strings.TrimSpace(repo.CloneURL)
	// `-c` auth is scoped to this command, so origin stays the clean URL and
	// the token never lands in .git/config.
	args := append(gitwork.AuthConfigArgs(repo.GitToken), "clone", "--quiet")
	if ref := strings.TrimSpace(repo.Ref); ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, url, ".")
	cmd := exec.CommandContext(cloneCtx, "git", args...)
	cmd.Dir = workspace
	cmd.Env = gitwork.Env(repo.GitToken)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone %s: %v (%s)", repo.CloneURL, err, cloneOutput(out, repo.GitToken))
	}
	if workBranch := strings.TrimSpace(repo.WorkBranch); workBranch != "" && workBranch != strings.TrimSpace(repo.Ref) {
		remoteRef := "refs/remotes/origin/" + workBranch
		if err := exec.CommandContext(cloneCtx, "git", "-C", workspace, "show-ref", "--verify", "--quiet", remoteRef).Run(); err == nil {
			if out, err := exec.CommandContext(cloneCtx, "git", "-C", workspace, "checkout", "--quiet", "-b", workBranch, "--track", "origin/"+workBranch).CombinedOutput(); err != nil {
				return fmt.Errorf("git checkout work branch %s: %v (%s)", workBranch, err, cloneOutput(out, repo.GitToken))
			}
		} else if out, err := exec.CommandContext(cloneCtx, "git", "-C", workspace, "checkout", "--quiet", "-b", workBranch).CombinedOutput(); err != nil {
			return fmt.Errorf("git create work branch %s: %v (%s)", workBranch, err, cloneOutput(out, repo.GitToken))
		}
	}
	return nil
}

// cloneOutput keeps a git failure short and strips the token before the text
// can be stored on the task or shown in the run timeline.
func cloneOutput(out []byte, token string) string {
	detail := gitwork.Redact(strings.TrimSpace(string(out)), token)
	if len(detail) > 300 {
		detail = detail[len(detail)-300:]
	}
	return detail
}

// maxArtifactUploadBytes bounds one uploaded artifact; the complete endpoint
// enforces the same limit server-side.
const maxArtifactUploadBytes = 1 << 20

// resolveCommandPath turns a bare binary name into an absolute path so the
// spawn cannot accidentally resolve against the daemon's own directory.
func resolveCommandPath(argv []string) []string {
	if len(argv) == 0 || argv[0] == "" || strings.ContainsRune(argv[0], os.PathSeparator) {
		return argv
	}
	if resolved, err := exec.LookPath(argv[0]); err == nil {
		argv[0] = resolved
	}
	return argv
}

// executeMock serves the built-in mock provider: it echoes the prompt so the
// full register/claim/execute/complete loop works without a real Agent CLI.
func (d *Daemon) executeMock(ctx context.Context, task domain.RunnerTask) execResult {
	batcher := newEventBatcher(task, d.client)
	if err := d.client.StartTask(ctx, task.ID); err != nil {
		return execResult{status: domain.RunnerTaskFailed, errMsg: fmt.Sprintf("start: %v", err)}
	}
	batcher.addLine("stdout", "MOCK "+task.Prompt)
	batcher.flush(ctx)
	return execResult{status: domain.RunnerTaskSuccess, exitCode: 0, result: "MOCK " + task.Prompt}
}

func (d *Daemon) defaultWorkspace(taskID string) string {
	return filepath.Join(d.cfg.DefaultWorkspaceRoot, taskID)
}

// runnerVersion is reported at registration; it matches the module build.
var runnerVersion = "0.6.0"

// Version returns the runner build version reported at registration.
func Version() string { return runnerVersion }
