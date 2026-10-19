package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/preview"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

type Engine struct {
	repo               *repo.Store
	sandboxes          *runtime.Manager
	runner             *runtime.ExecutorRunner
	mu                 sync.Mutex
	stateMu            sync.Mutex
	running            map[string]context.CancelFunc
	runEnvMu           sync.Mutex
	runEnvs            map[string]*runEnvironment
	workspaceTerminals runtime.TerminalPool
	// workspaceFacts are the latest facts an execution machine reported
	// per run, read where the engine used to inspect its own workspace copy.
	workspaceFacts map[string]domain.RunnerWorkspaceFacts
	cloudPool      CloudRunnerPool
	runExec        map[string]*sync.Mutex
	subMu          sync.Mutex
	subs           map[string]map[chan domain.Run]struct{}
	runnerTaskMu   sync.Mutex
	runnerTaskSubs map[string]map[chan struct{}]struct{}
	instanceID     string
	workspaceHost  string
	leaseTTL       time.Duration
	executeRuns    bool
	draining       bool

	// MergeIntoBranch creates or reuses a Gongfeng MR from source→target and
	// merges it (merge commit, source branch kept). Scheduled SOP uses this
	// to land each issue work branch on the integrate baseline. Nil means
	// the engine falls back to a direct push (local/dev without Gongfeng).
	// The default branch is never an automatic merge target.
	MergeIntoBranch func(projectID, sourceBranch, targetBranch, title, description string) (string, error)

	// FindBranchMR resolves the Gongfeng MR whose source is a run repository's
	// work branch. Nil (local/dev) falls back to MR links in run artifacts.
	FindBranchMR func(projectID, sourceBranch string) (domain.DeliveryMR, bool, error)

	// runnerDispatchNotify wakes long-polling CLI runners when the engine
	// queues a runner task. Wired via SetRunnerDispatchNotify (httpapi.New);
	// nil falls back to the runner claim retry interval.
	runnerDispatchNotify func(provider string)
	// runnerControlNotify pushes durable task control changes to a connected
	// execution machine. The database desired_state remains authoritative.
	runnerControlNotify func(runnerID, taskID, desiredState string)
	// runControlNotify wakes the owning worker after pause/cancel flags are
	// written. Nil means this process looks up engine_instances (and optional
	// fallbackWorkerURL) and POSTs the interrupt itself.
	runControlNotify     func(runID string)
	controlRole          string
	controlURL           string
	controlToken         string
	workspaceControlBase string // Worker -> API; independent of the sandbox-facing MCP URL.
	fallbackWorkerURL    string
	controlClient        *http.Client

	// 平台 MCP 绑定：角色通过 AgentDefinition.PlatformMCP 显式接入
	// agentflow-platform（/mcp）。base 为平台 API 基地址，token 为可选
	// Bearer 令牌（仅来自环境变量，绝不落库）。空 base 表示未启用。
	platformMCPBase  string
	platformMCPToken string

	preview *preview.Service

	obsMu      sync.Mutex
	obsPending []pendingObservation
	obsKick    chan struct{}

	WorkspaceActive              func(domain.Run) bool
	WorkspaceCheckpointTransport func(context.Context, domain.Run, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error)
	maintenanceOnce              sync.Once
}

var ErrExecutionDisabled = errors.New("run execution is disabled on this instance")

func New(s *repo.Store, sm *runtime.Manager) *Engine {
	host, _ := os.Hostname()
	e := &Engine{repo: s, sandboxes: sm, runner: runtime.NewExecutorRunner(), running: map[string]context.CancelFunc{}, runEnvs: map[string]*runEnvironment{}, workspaceFacts: map[string]domain.RunnerWorkspaceFacts{}, subs: map[string]map[chan domain.Run]struct{}{}, runnerTaskSubs: map[string]map[chan struct{}]struct{}{}, instanceID: fmt.Sprintf("%s-%d", host, os.Getpid()), workspaceHost: host, leaseTTL: 30 * time.Second, executeRuns: true, obsKick: make(chan struct{}, 1)}
	return e
}

// StartMaintenance starts the process-lifetime background work owned by the
// engine. New deliberately does not start goroutines: short-lived callers
// (especially tests) can construct an Engine without leaving sweepers behind
// after their Store has closed.
func (e *Engine) StartMaintenance(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.maintenanceOnce.Do(func() {
		go e.reclaimSandboxes()
		go e.sandboxSweep(ctx)
		go e.observationFlushLoop(ctx)
	})
}

func (e *Engine) ConfigureExecution(instanceID, workspaceHost string, ttl time.Duration, enabled bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if strings.TrimSpace(instanceID) != "" {
		e.instanceID = strings.TrimSpace(instanceID)
	}
	if strings.TrimSpace(workspaceHost) != "" {
		e.workspaceHost = strings.TrimSpace(workspaceHost)
	}
	if ttl > 0 {
		e.leaseTTL = ttl
	}
	e.executeRuns = enabled
}

// ConfigurePlatformMCP sets the base URL and optional bearer token of this
// AgentFlow deployment's own MCP endpoint (/mcp), used to materialize the
// reserved agentflow-platform server for roles that opt in via
// AgentDefinition.PlatformMCP. An empty base disables the binding.
func (e *Engine) ConfigurePlatformMCP(baseURL, token string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.platformMCPBase = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	e.platformMCPToken = strings.TrimSpace(token)
}

func (e *Engine) SetPreview(svc *preview.Service) {
	e.preview = svc
	if svc != nil {
		svc.SetHost(e)
	}
}

func (e *Engine) Preview() *preview.Service { return e.preview }

func (e *Engine) OnRunnerTaskCompleted(task domain.RunnerTask) {
	if e.preview != nil {
		e.preview.OnArchiveTaskCompleted(task)
	}
}
