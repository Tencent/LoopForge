package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/managedagent"
	"agentflow/internal/opensandbox"
	"agentflow/internal/repo"
)

type testManagedAgent struct{}

func (testManagedAgent) Run(_ context.Context, prompt string, onText func(string)) (string, error) {
	if !strings.Contains(prompt, "describe the change") {
		return "", errors.New("the WMA task book is missing the run input")
	}
	onText("wma-plan")
	return "wma-plan", nil
}

func TestOpenSandboxMixedSOP(t *testing.T) {
	api, store, eng := newDaemonTestServer(t)
	var wma managedagent.TextAgent = testManagedAgent{}
	input := "describe the change"
	if session := os.Getenv("LF_WMA_E2E_SESSION_ID"); session != "" {
		client, err := managedagent.NewWMA(managedagent.Config{
			AgentID: os.Getenv("LF_WMA_E2E_AGENT_ID"), SessionID: session,
			TencentCloud: &managedagent.TencentCloudConfig{
				SecretID: os.Getenv("AGENTFLOW_WMA_SECRET_ID"), SecretKey: os.Getenv("AGENTFLOW_WMA_SECRET_KEY"),
				Token: os.Getenv("AGENTFLOW_WMA_SECURITY_TOKEN"), Region: os.Getenv("AGENTFLOW_WMA_REGION"),
			},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		wma = client
		input = "Only output wma-plan. Do not add quotes, decoration, or explanations. Do not call tools."
	}
	if err := eng.ConfigureManagedAgent(domain.WMAExecutorID, "sandbox-pilot", 42, wma); err != nil {
		t.Fatal(err)
	}

	endpoint := os.Getenv("LF_OPENSANDBOX_E2E_URL")
	runnerURL, key, image := api.URL, "mock-api-key", "runner:test"
	var repoContext domain.RepositoryContext
	if endpoint == "" {
		endpoint = startOpenSandboxFixture(t, api.URL)
		private := os.Getenv("LF_OPENSANDBOX_PRIVATE_SNAPSHOT")
		if private == "" {
			private = bareRemoteWithBaseline(t)
		}
		branch := runGitT(t, private, "symbolic-ref", "--short", "HEAD")
		repoContext = domain.RepositoryContext{RepositoryID: "fixture", Name: "sandbox fixture", CloneURL: private, BaseBranch: branch, WorkBranch: "pilot/verify"}
	} else {
		runnerURL = exposeRunnerTestAPI(t, api.URL, os.Getenv("LF_OPENSANDBOX_E2E_BIND_HOST"))
		key, image = os.Getenv("LF_OPENSANDBOX_E2E_API_KEY"), os.Getenv("LF_OPENSANDBOX_E2E_IMAGE")
	}
	provider, err := opensandbox.New(opensandbox.Config{
		Endpoint: endpoint, APIKey: key, RunnerURL: runnerURL, RunnerImage: image,
		Scope: "runner-integration-test", RunnerSigningKey: strings.Repeat("test", 10),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ConfigureOpenSandbox("sandbox-test", provider, []string{"sandbox-pilot"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRunnerTask(domain.RunnerTask{ID: "unrelated", Provider: "terminal", Status: domain.RunnerTaskQueued, MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	role := domain.AgentDefinition{ID: "wma-planner", Name: "WMA planner", ExecutorID: domain.WMAExecutorID, Prompt: "{{input}}", ProjectID: "sandbox-pilot"}
	if err := store.PutAgent(role); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{
		ID: "mixed-execution", Name: "Mixed execution", SandboxProfileID: repo.TestNativeSandboxID,
		Nodes: []domain.WorkflowNode{
			{ID: "plan", Name: "WMA plan", Type: "agent", AgentID: role.ID, MaxAttempts: 1},
			{ID: "prepare", Name: "Prepare", Type: "script", Script: `set -eu; test "$(cat .agentflow/inputs/plan/result.md)" = wma-plan; printf 'proof\n' > lf-sandbox-proof.txt`, MaxAttempts: 1},
			{ID: "verify", Name: "Verify", Type: "script", Script: `set -eu; test "$(cat lf-sandbox-proof.txt)" = proof; printf 'shared workspace verified\n'`, MaxAttempts: 1},
		},
		Edges: []domain.WorkflowEdge{
			{ID: "plan-prepare", Source: "plan", Target: "prepare", When: "success"},
			{ID: "prepare-verify", Source: "prepare", Target: "verify", When: "success"},
		},
	}
	if err := store.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	run, err := eng.StartWithExecutionContext(wf.ID, input, repoContext, domain.TelemetryContext{ProjectID: "sandbox-pilot", ResponsibleUserID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if run.ExecutionContext.ExecutionProviderID != "sandbox-test" || run.ExecutionContext.ExecutionRunnerID != domain.OpenSandboxRunnerID(run.ID) {
		t.Fatalf("unexpected Runner binding: %q", run.ExecutionContext.ExecutionRunnerID)
	}
	waitFor(t, 150*time.Second, "mixed SOP to finish", func() bool {
		final, _ := store.Run(run.ID)
		return final.Status == "succeeded" || final.Status == "failed"
	})
	final, _ := store.Run(run.ID)
	if final.Status != "succeeded" {
		t.Fatalf("run %s: plan=%s prepare=%s verify=%s", final.Status, final.Nodes["plan"].Error, final.Nodes["prepare"].Error, final.Nodes["verify"].Error)
	}
	if !strings.Contains(final.Nodes["verify"].Output, "shared workspace verified") || strings.TrimSpace(final.Nodes["plan"].Output) != "wma-plan" {
		t.Fatalf("plan and verify outputs: %q, %q", final.Nodes["plan"].Output, final.Nodes["verify"].Output)
	}
	unrelated, _ := store.RunnerTask("unrelated")
	if unrelated.Status != domain.RunnerTaskQueued || unrelated.LeaseOwner != "" {
		t.Fatalf("workspace Runner claimed unrelated task: %+v", unrelated)
	}
	registeredID := final.ExecutionContext.ExecutionRunnerID
	externalID := final.ExecutionContext.ExecutionResourceID
	if externalID == "" {
		t.Fatal("sandbox binding was not persisted")
	}
	waitFor(t, 25*time.Second, "sandbox and Runner release", func() bool {
		_, present := store.Runner(registeredID)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := provider.Inspect(ctx, run.ID, externalID)
		return !present && errors.Is(err, opensandbox.ErrNotFound)
	})
	t.Logf("workspace %s used OpenSandbox %s with Runner %s", run.ID, externalID, registeredID)
}

// startOpenSandboxFixture speaks the lifecycle HTTP contract and boots the
// actual Runner daemon on the host. The optional real test uses the same SOP.
func startOpenSandboxFixture(t *testing.T, runnerURL string) string {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	root := t.TempDir()
	var mu sync.Mutex
	var metadata map[string]string
	var daemonDone chan struct{}
	created := false
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("OPEN-SANDBOX-API-KEY") != "mock-api-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		record := func() any {
			return map[string]any{"id": "sandbox-1", "metadata": metadata, "status": map[string]string{"state": "Running"}}
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/sandboxes":
			items := []any{}
			if created {
				items = append(items, record())
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items, "pagination": map[string]int{"totalItems": len(items)}})
		case "POST /v1/sandboxes":
			if created {
				t.Error("test SOP allocated a second sandbox")
				w.WriteHeader(http.StatusConflict)
				return
			}
			var request struct {
				Env      map[string]string `json:"env"`
				Metadata map[string]string `json:"metadata"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode OpenSandbox create: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if value := request.Metadata["loopforge-binding"]; len(value) == 0 || len(value) > 63 {
				t.Errorf("OpenSandbox binding is not a Kubernetes label value: %q", value)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			cfg := Config{
				ServerURL: runnerURL, BootstrapToken: request.Env["AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN"],
				Name: request.Env["AGENTFLOW_RUNNER_NAME"], MaxConcurrency: 1, DisableAutoUpdate: true,
				StatePath: filepath.Join(root, "state.json"), DefaultWorkspaceRoot: filepath.Join(root, "workspace"),
				HeartbeatInterval: time.Second, LongPollTimeout: 2 * time.Second,
			}
			daemon, err := New(cfg)
			if err != nil {
				t.Errorf("start fixture Runner: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			metadata, created = request.Metadata, true
			done := make(chan struct{})
			daemonDone = done
			go func() {
				defer close(done)
				if err := daemon.Run(ctx); err != nil && ctx.Err() == nil {
					t.Errorf("fixture Runner: %v", err)
				}
			}()
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(record())
		case "GET /v1/sandboxes/sandbox-1":
			if !created {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(record())
		case "DELETE /v1/sandboxes/sandbox-1":
			created = false
			stop()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected lifecycle request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(func() {
		fixture.Close()
		stop()
		if daemonDone != nil {
			select {
			case <-daemonDone:
			case <-time.After(5 * time.Second):
				t.Error("fixture Runner did not stop")
			}
		}
	})
	return fixture.URL
}

func exposeRunnerTestAPI(t *testing.T, target, bindHost string) string {
	t.Helper()
	if net.ParseIP(bindHost) == nil {
		t.Fatal("LF_OPENSANDBOX_E2E_BIND_HOST must be the test cluster's host bridge IP")
	}
	upstream, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(bindHost, "0"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: httputil.NewSingleHostReverseProxy(upstream), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	}()
	t.Cleanup(func() { server.Close() })
	return fmt.Sprintf("http://%s", listener.Addr())
}
