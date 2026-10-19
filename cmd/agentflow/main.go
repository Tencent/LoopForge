package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"agentflow/internal/config"
	"agentflow/internal/engine"
	"agentflow/internal/httpapi"
	"agentflow/internal/managedcli"
	"agentflow/internal/notification"
	"agentflow/internal/preview"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
	"github.com/gin-gonic/gin"
)

// loadDotEnv loads KEY=VALUE pairs from the first .env file found. Search
// order: the current working directory and up to three parents (so running
// from a subdirectory still finds the project root), then next to the
// executable. Existing environment variables take precedence and are never
// overwritten.
func loadDotEnv() {
	var candidates []string
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for i := 0; i < 4; i++ {
			candidates = append(candidates, filepath.Join(dir, ".env"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".env"))
	}
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if k != "" && os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
		return
	}
}

func main() {
	if runtime.RunMCPCallCLI(os.Args[1:]) {
		return
	}
	addr := flag.String("addr", "", "HTTP listen address; overrides AGENTFLOW_HTTP_PORT")
	workspaces := flag.String("workspaces", "", "sandbox workspace root")
	workerConcurrency := flag.Int("worker-concurrency", 8, "maximum concurrent node sandboxes for this worker")
	basicUser := flag.String("basic-user", "", "optional HTTP Basic Auth username")
	basicPass := flag.String("basic-pass", "", "optional HTTP Basic Auth password")
	mcpToken := flag.String("mcp-token", "", "MCP gateway bearer token (or set AGENTFLOW_MCP_TOKEN env)")
	tlsCert := flag.String("tls-cert", "", "optional TLS certificate path")
	tlsKey := flag.String("tls-key", "", "optional TLS private key path")
	mysqlDSN := flag.String("mysql-dsn", "", "MySQL DSN; overrides AGENTFLOW_MYSQL_DSN. MySQL is the only data source.")
	roleFlag := flag.String("role", "", "process role: api, worker, or all (default all)")
	cliAction := flag.String("cli-action", "", "local managed CLI administration: status, update, or rollback; exits without starting services")
	cliProvider := flag.String("cli-provider", "", "provider for CLI rollback: codex, codebuddy, or claude-code")
	cliTimeout := flag.Duration("cli-timeout", 80*time.Minute, "total managed CLI operation timeout")
	flag.Parse()
	loadDotEnv()
	if os.Getenv("AGENTFLOW_SESSION_SECRET") == "" && *cliAction == "" {
		log.Fatal("AGENTFLOW_SESSION_SECRET is required; generate with openssl rand -hex 32")
	}
	if os.Getenv("AGENTFLOW_AUTH_REQUIRED") == "" {
		os.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	}
	if *cliAction != "" || *cliProvider != "" {
		if flag.NArg() != 0 {
			log.Fatal("CLI administration does not accept positional arguments")
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		err := runCLI(ctx, *cliAction, *cliProvider, *cliTimeout, os.Stdout)
		stop()
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	role, err := resolveRole(*roleFlag)
	if err != nil {
		log.Fatal(err)
	}
	root, err := resolveWorkspaceRoot(*workspaces)
	if err != nil {
		log.Fatal(err)
	}
	s, err := repo.Open(strings.TrimSpace(*mysqlDSN))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("storage driver=%v database=%v role=%s", s.StorageInfo()["driver"], s.StorageInfo()["database"], role)
	e := engine.New(s, runtime.NewManagerWithLimit(root, *workerConcurrency))
	runtime.PrebuildIsolatedImage()
	host, _ := os.Hostname()
	instanceID := strings.TrimSpace(os.Getenv("AGENTFLOW_INSTANCE_ID"))
	if instanceID == "" {
		instanceID = fmt.Sprintf("%s-%s-%d", role, host, os.Getpid())
	}
	leaseTTL := 30 * time.Second
	if raw := strings.TrimSpace(os.Getenv("AGENTFLOW_RUN_LEASE_TTL")); raw != "" {
		parsed, parseErr := time.ParseDuration(raw)
		if parseErr != nil || parsed <= 0 {
			log.Fatalf("invalid AGENTFLOW_RUN_LEASE_TTL %q", raw)
		}
		leaseTTL = parsed
	}
	executeRuns := role != "api"
	if role == "all" {
		if raw := strings.TrimSpace(os.Getenv("AGENTFLOW_EXECUTE_RUNS")); raw != "" {
			parsed, parseErr := strconv.ParseBool(raw)
			if parseErr != nil {
				log.Fatalf("invalid AGENTFLOW_EXECUTE_RUNS %q", raw)
			}
			executeRuns = parsed
		}
	}
	e.ConfigureExecution(instanceID, host, leaseTTL, executeRuns)
	e.ConfigureCloudRunnerPool(engine.CloudRunnerPool{
		Label:    os.Getenv("AGENTFLOW_CLOUD_RUNNER_LABEL"),
		Projects: engine.ParseCloudRunnerProjects(os.Getenv("AGENTFLOW_CLOUD_RUNNER_PROJECTS")),
		// AGENTFLOW_LOCAL_EXECUTION=false keeps this host from running nodes
		// of pool projects: they wait for, or move to, a pool runner.
		Required: strings.EqualFold(strings.TrimSpace(os.Getenv("AGENTFLOW_LOCAL_EXECUTION")), "false"),
	})
	attachPreviewStore(e, s)
	log.Printf("engine instance=%s role=%s execute_runs=%t run_lease_ttl=%s", instanceID, role, executeRuns, leaseTTL)
	claimCtx, stopClaim := context.WithCancel(context.Background())
	defer stopClaim()
	endpoints, err := config.LoadEndpoints(role, strings.TrimSpace(*addr))
	if err != nil {
		log.Fatal(err)
	}
	config.SetCurrent(endpoints)
	e.ConfigurePlatformMCP(endpoints.PlatformBase, endpoints.PlatformMCPToken)
	if endpoints.PlatformBase == "" {
		log.Printf("platform MCP / knowledge proxy: no sandbox-reachable platform URL; cloud runs skip them (set AGENTFLOW_PLATFORM_MCP_URL)")
	} else {
		log.Printf("platform MCP / knowledge proxy base=%s", endpoints.PlatformBase)
	}

	var srv *http.Server
	var syncSvc *tracker.Service
	stopAssistantBackground := func() {}
	switch role {
	case "worker":
		e.ConfigureControl(engine.ControlConfig{Role: role, URL: endpoints.ControlURL, Token: endpoints.ControlToken, APIURL: endpoints.APIBase})
		e.StartInstanceHeartbeat(claimCtx)
		srv = &http.Server{Addr: endpoints.ListenAddr, Handler: workerHandler(e), ReadHeaderTimeout: 10 * time.Second}
	default:
		e.ConfigureControl(engine.ControlConfig{Token: endpoints.ControlToken, FallbackURL: endpoints.WorkerFallbackURL, APIURL: endpoints.APIBase})
		listenAddr := endpoints.ListenAddr
		syncSvc = tracker.NewService()
		token := *mcpToken
		if token == "" {
			token = os.Getenv("AGENTFLOW_MCP_TOKEN")
		}
		if token != "" {
			syncSvc.SetAuthToken(token)
		}
		gin.SetMode(gin.ReleaseMode)
		cfg := s.SyncConfig()
		syncSvc.ConfigureWithStore(cfg, s)
		if cfg.AutoSyncEnabled {
			syncSvc.StartAutoSync(s)
		}
		syncSvc.StartMRStateSync(s)
		notifications := notification.New(s)
		handler, stopBackground := httpapi.NewWithNotificationsAndBackground(claimCtx, s, e, syncSvc, *basicUser, *basicPass, notifications)
		stopAssistantBackground = stopBackground
		defer stopAssistantBackground()
		notifications.Start(claimCtx)
		srv = &http.Server{Addr: listenAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	}

	// Configure control endpoints and wire the API's Runner transport before
	// claiming work: startup must not execute nodes against an unconfigured engine.
	e.StartMaintenance(claimCtx)
	if executeRuns {
		e.StartClaimLoop(claimCtx)
	}
	stopCLIUpdates := startManagedCLIUpdates(claimCtx, role, executeRuns)
	defer stopCLIUpdates()
	stopped := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Printf("shutting down %s: stop claiming, drain local runs, then stop HTTP...", role)
		stopClaim()
		stopAssistantBackground()
		stopCLIUpdates() // Cancel updater subprocesses and join before draining.
		drainTimeout := 30 * time.Minute
		if role == "api" {
			drainTimeout = 15 * time.Second
		}
		if raw := strings.TrimSpace(os.Getenv("AGENTFLOW_DRAIN_TIMEOUT")); raw != "" {
			if parsed, parseErr := time.ParseDuration(raw); parseErr == nil && parsed > 0 {
				drainTimeout = parsed
			}
		}
		drainCtx, cancelDrain := context.WithTimeout(context.Background(), drainTimeout)
		defer cancelDrain()
		if err := e.Drain(drainCtx); err != nil {
			log.Printf("drain timeout with %d active run(s): %v", e.ActiveRunCount(), err)
		} else {
			log.Printf("all local runs drained")
		}
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("http shutdown: %v", err)
		}
		if syncSvc != nil {
			syncSvc.StopMRStateSync()
			syncSvc.StopAutoSync()
		}
		s.Close()
		close(stopped)
	}()
	displayAddr := srv.Addr
	if strings.HasPrefix(displayAddr, ":") {
		displayAddr = "localhost" + displayAddr
	}
	serve := srv.ListenAndServe
	if role != "worker" && *tlsCert != "" && *tlsKey != "" {
		log.Printf("AgentFlow %s listening on https://%s", role, displayAddr)
		serve = func() error { return srv.ListenAndServeTLS(*tlsCert, *tlsKey) }
	} else {
		log.Printf("AgentFlow %s listening on http://%s", role, displayAddr)
	}
	if err := serve(); err != nil && err != http.ErrServerClosed {
		stopClaim()
		stopCLIUpdates() // log.Fatal bypasses deferred cleanup.
		log.Fatal(err)
	}
	<-stopped
}

func startManagedCLIUpdates(ctx context.Context, role string, executeRuns bool) func() {
	root := strings.TrimSpace(os.Getenv("AGENTFLOW_MANAGED_CLI_ROOT"))
	// Never infer a root or opt personal runners/API-only processes into updates.
	if !executeRuns || (role != "worker" && role != "all") || root == "" {
		return func() {}
	}
	return managedcli.Start(ctx, root)
}

func workerHandler(e *engine.Engine) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = fmt.Fprintf(w, `{"status":"ok","role":"worker","activeRuns":%d}`, e.ActiveRunCount())
	})
	mux.HandleFunc("POST /internal/runs/{id}/interrupt", e.HandleRunInterrupt)
	mux.HandleFunc("POST /internal/workspace-checkpoint", e.HandleWorkspaceCheckpoint)
	return mux
}

func resolveRole(override string) (string, error) {
	role := strings.ToLower(strings.TrimSpace(override))
	if role == "" {
		role = strings.ToLower(strings.TrimSpace(os.Getenv("AGENTFLOW_ROLE")))
	}
	if role == "" {
		role = "all"
	}
	switch role {
	case "api", "worker", "all":
		return role, nil
	default:
		return "", fmt.Errorf("invalid role %q (want api, worker, or all)", role)
	}
}

func resolveWorkspaceRoot(override string) (string, error) {
	if root := strings.TrimSpace(override); root != "" {
		return root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve default sandbox workspace root: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve default sandbox workspace root: user home is empty")
	}
	return filepath.Join(home, ".agentflow", "workspace"), nil
}

func attachPreviewStore(e *engine.Engine, s *repo.Store) {
	cfg := preview.LoadConfig()
	e.SetPreview(preview.NewService(s, preview.NewDBStore(s), cfg))
	if cfg.PublicHost != "" {
		log.Printf("preview gateway path=/preview/{token}/ public=%s://%s", cfg.PublicScheme, cfg.PublicHost)
	} else {
		log.Printf("preview gateway path=/preview/{token}/")
	}
}
