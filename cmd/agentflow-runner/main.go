package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"agentflow/internal/runner"
	"agentflow/internal/runtime"
)

// agentflow-runner is the CLI Runner daemon (cli-connect.md). It runs on a
// user machine or CVM, connects to the AgentFlow control plane over outbound
// HTTPS only, and executes Agent CLI tasks on that machine. Agent CLI
// credentials stay on this machine.
func main() {
	if runtime.RunMCPCallCLI(os.Args[1:]) {
		return
	}
	server := flag.String("server", "", "AgentFlow server URL, e.g. https://workflow.example.com (or AGENTFLOW_SERVER_URL)")
	token := flag.String("token", "", "runner bootstrap token (or AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN)")
	name := flag.String("name", "", "runner display name (default: hostname)")
	statePath := flag.String("state", "", "state file path (default: ~/.agentflow-runner/state.json)")
	workspace := flag.String("workspace", "", "default workspace root for tasks without their own path")
	concurrency := flag.Int("concurrency", 1, "maximum concurrent tasks")
	heartbeat := flag.Duration("heartbeat", 30*time.Second, "heartbeat interval")
	longPoll := flag.Duration("long-poll", 35*time.Second, "client-side long-poll timeout; must exceed the server's")
	autoUpdate := flag.Bool("auto-update", true, "check for platform updates before claiming queued work")
	labels := flag.String("labels", "", "comma-separated runner labels")
	reuseSandbox := flag.Bool("reuse-run-sandbox", true, "run all nodes of a run in one sandbox per definition instead of one per task")
	flag.Parse()
	loadDotEnv()

	cfg := runner.Config{
		ServerURL:              firstNonEmpty(*server, os.Getenv("AGENTFLOW_SERVER_URL")),
		BootstrapToken:         firstNonEmpty(*token, os.Getenv("AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN")),
		StatePath:              firstNonEmpty(*statePath, os.Getenv("AGENTFLOW_RUNNER_STATE_PATH")),
		Name:                   firstNonEmpty(*name, os.Getenv("AGENTFLOW_RUNNER_NAME")),
		DefaultWorkspaceRoot:   firstNonEmpty(*workspace, os.Getenv("AGENTFLOW_RUNNER_WORKSPACE")),
		MaxConcurrency:         *concurrency,
		HeartbeatInterval:      *heartbeat,
		LongPollTimeout:        *longPoll,
		DisableAutoUpdate:      !*autoUpdate,
		ManagedCLIRoot:         strings.TrimSpace(os.Getenv("AGENTFLOW_MANAGED_CLI_ROOT")),
		DisableRunSandboxReuse: !*reuseSandbox,
	}
	if raw := strings.TrimSpace(os.Getenv("AGENTFLOW_RUNNER_CONCURRENCY")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			cfg.MaxConcurrency = parsed
		}
	}
	if *labels != "" {
		for _, label := range strings.Split(*labels, ",") {
			if label = strings.TrimSpace(label); label != "" {
				cfg.Labels = append(cfg.Labels, label)
			}
		}
	}

	daemon, err := runner.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("agentflow-runner %s connecting to %s (concurrency=%d)", runner.Version(), cfg.ServerURL, cfg.MaxConcurrency)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := daemon.Run(ctx); errors.Is(err, runner.ErrUpdateReady) {
		binary, exeErr := os.Executable()
		if exeErr != nil {
			log.Fatal("locate updated runner: ", exeErr)
		}
		log.Printf("restarting updated runner")
		if exeErr := syscall.Exec(binary, os.Args, os.Environ()); exeErr != nil {
			log.Fatal("restart updated runner: ", exeErr)
		}
	} else if err != nil {
		log.Fatal(err)
	}
	log.Printf("runner stopped")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// loadDotEnv mirrors cmd/agentflow so a runner machine can keep its bootstrap
// token in .env next to the working directory.
func loadDotEnv() {
	raw, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
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
}
