package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	flowruntime "agentflow/internal/runtime"

	"github.com/gorilla/websocket"
)

func TestPreviewArchiveSuccessAndFailures(t *testing.T) {
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/content"):
			uploaded, _ = io.ReadAll(r.Body)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	workspace := filepath.Join(root, "runs", "run-preview", "workspace")
	_ = os.MkdirAll(filepath.Join(workspace, "site"), 0o755)
	_ = os.WriteFile(filepath.Join(workspace, "site", "index.html"), []byte("<h1>ok</h1>"), 0o644)
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: root}, client: NewClient(server.URL)}
	task := domain.RunnerTask{ID: "task", RunID: "run-preview", Workspace: "runs/run-preview/workspace",
		PreviewArchive: &domain.RunnerPreviewArchiveSpec{PreviewID: "p", SourcePath: "site", IndexFile: "index.html", MaxFiles: 10, MaxBytes: 1 << 20, UploadToken: "u"}}
	result := d.executePreviewArchive(context.Background(), task)
	if result.status != domain.RunnerTaskSuccess || len(uploaded) == 0 {
		t.Fatalf("result=%+v uploaded=%d", result, len(uploaded))
	}
	task.PreviewArchive = nil
	if result = d.executePreviewArchive(context.Background(), task); result.status != domain.RunnerTaskFailed {
		t.Fatalf("missing spec=%+v", result)
	}
	task.PreviewArchive = &domain.RunnerPreviewArchiveSpec{PreviewID: "p", SourcePath: "missing", MaxBytes: 1024}
	if result = d.executePreviewArchive(context.Background(), task); result.status != domain.RunnerTaskFailed {
		t.Fatalf("archive failure=%+v", result)
	}
	task.RunID = "../unsafe"
	task.Workspace = "/tmp"
	if result = d.executePreviewArchive(context.Background(), task); result.status != domain.RunnerTaskFailed || !strings.Contains(result.errMsg, "unsafe") {
		t.Fatalf("unsafe=%+v", result)
	}
}

func TestWorkBranchDispatchWithoutRepositories(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}, client: NewClient(server.URL)}
	for _, action := range []string{domain.RunnerTaskActionPushWorkBranch, domain.RunnerTaskActionPullWorkBranch} {
		task := domain.RunnerTask{ID: "task-" + action, Action: action, Invocation: &domain.RunnerInvocation{RunID: "run", NodeID: "node"}}
		result := d.executeWorkBranchSync(context.Background(), task)
		if result.status != domain.RunnerTaskSuccess || result.result != "[]" {
			t.Fatalf("%s result=%+v", action, result)
		}
	}
	heads, err := syncTaskWorkBranches(context.Background(), t.TempDir(), nil, true, "")
	if err != nil || len(heads) != 0 {
		t.Fatalf("heads=%v err=%v", heads, err)
	}
}

func TestClientOpenControlWebSocketBranches(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runners/connect":
			if r.Header.Get("Authorization") != "Bearer token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err == nil {
				_ = conn.Close()
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := NewClient(server.URL)
	if _, err := c.OpenControl(context.Background()); err == nil {
		t.Fatal("unauthorized socket opened")
	}
	c.SetSession("token")
	conn, err := c.OpenControl(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if _, err := NewClient("file:///tmp").OpenControl(context.Background()); err == nil {
		t.Fatal("unsupported scheme accepted")
	}
	if _, err := NewClient("://bad").OpenControl(context.Background()); err == nil {
		t.Fatal("invalid URL accepted")
	}
}

func TestControlLoopReceivesSignalsAndWorkspaceRequests(t *testing.T) {
	upgrader := websocket.Upgrader{}
	workspaceResponse := make(chan controlMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var hello controlMessage
		if conn.ReadJSON(&hello) != nil {
			return
		}
		_ = conn.WriteJSON(controlMessage{Type: "task.control", TaskID: "task", DesiredState: domain.RunnerTaskDesiredCancel})
		_ = conn.WriteJSON(controlMessage{
			Type: "workspace.request", RequestID: "req",
			WorkspaceRequest: &domain.RunnerWorkspaceRequest{RunID: "../bad", Operation: domain.RunnerWorkspaceOperationInfo},
		})
		var response controlMessage
		if conn.ReadJSON(&response) == nil {
			workspaceResponse <- response
		}
	}))
	defer server.Close()
	d := &Daemon{
		cfg:             Config{DefaultWorkspaceRoot: t.TempDir()},
		client:          NewClient(server.URL),
		controls:        map[string]chan string{},
		pendingControls: map[string]string{},
	}
	control := d.registerTaskControl("task")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.controlLoop(ctx); close(done) }()
	select {
	case desired := <-control:
		if desired != domain.RunnerTaskDesiredCancel {
			t.Fatalf("desired=%q", desired)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("control signal timeout")
	}
	select {
	case response := <-workspaceResponse:
		if response.Type != "workspace.response" || response.WorkspaceResponse == nil {
			t.Fatalf("response=%+v", response)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workspace response timeout")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("control loop did not stop")
	}
	d.unregisterTaskControl("task")
}

func TestRunnerCredentialArtifactAndRepositoryHelpers(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, "repo")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("artifact"), 0o644)
	inv := &domain.RunnerInvocation{
		ArtifactDirectory: "repo",
		Artifacts:         []domain.ArtifactContract{{Name: "a", Path: "a.txt"}, {Name: "missing", Path: "missing"}, {Name: "blank"}},
		Repositories:      []domain.RunnerRepository{{RepositoryID: "r", Name: "Repo", Dir: "repo", GitToken: " token "}},
	}
	artifacts := collectRunnerInvocationArtifacts(workspace, inv)
	if artifacts["a"] != "artifact" || collectRunnerInvocationArtifacts(workspace, nil) != nil {
		t.Fatalf("artifacts=%v", artifacts)
	}
	contexts := invocationRepositoryContexts(inv)
	if len(contexts) != 1 || contexts[0].WorkspaceDir != "repo" || invocationGitToken(inv) != "token" {
		t.Fatalf("contexts=%+v token=%q", contexts, invocationGitToken(inv))
	}
	legacy := &domain.RunnerInvocation{Repository: &domain.RunnerRepository{RepositoryID: "legacy", Dir: "."}}
	if len(invocationRepositoryContexts(legacy)) != 1 {
		t.Fatal("legacy repository omitted")
	}
	for _, provider := range []string{"codex", "claude-code", "other"} {
		env, err := bindSandboxPrivateHome(workspace, "local", provider, nil)
		if err != nil || env["HOME"] == "" || env["TMPDIR"] == "" {
			t.Fatalf("%s env=%v err=%v", provider, env, err)
		}
	}
	for _, tc := range []struct {
		inv     domain.RunnerInvocation
		binding flowruntime.CredentialBinding
		argv    []string
		wantErr bool
	}{
		{domain.RunnerInvocation{}, flowruntime.BindCodeBuddy, []string{"codebuddy"}, true},
		{domain.RunnerInvocation{CodeBuddyAPIKey: "k", CodeBuddyInternetEnvironment: "intl"}, flowruntime.BindCodeBuddy, []string{"codebuddy"}, false},
		{domain.RunnerInvocation{CredentialSource: domain.RuntimeAuthPersonal}, flowruntime.BindCodex, []string{"codex", "p"}, true},
		{domain.RunnerInvocation{CredentialSource: domain.RuntimeAuthPersonal, CodexAPIKey: "k"}, flowruntime.BindCodex, []string{"codex"}, true},
		{domain.RunnerInvocation{CredentialSource: domain.RuntimeAuthPersonal}, flowruntime.BindClaudeCode, []string{"claude"}, true},
		{domain.RunnerInvocation{CredentialSource: domain.RuntimeAuthPersonal, ClaudeCodeAPIKey: "k"}, flowruntime.BindClaudeCode, []string{"claude"}, false},
	} {
		_, _, err := bindInvocationCredentials(&tc.inv, []flowruntime.CredentialBinding{tc.binding}, tc.argv)
		if (err != nil) != tc.wantErr {
			t.Fatalf("binding=%v err=%v wantErr=%v", tc.binding, err, tc.wantErr)
		}
	}
}

func TestWorkspacePayloadMarshalFailure(t *testing.T) {
	response := runnerWorkspacePayload(make(chan int))
	if response.Status != http.StatusInternalServerError || response.Error == "" {
		t.Fatalf("response=%+v", response)
	}
	payload := runnerWorkspacePayload(map[string]string{"ok": "yes"})
	var got map[string]string
	if payload.Status != http.StatusOK || json.Unmarshal(payload.Payload, &got) != nil || got["ok"] != "yes" {
		t.Fatalf("payload=%+v", payload)
	}
}

func TestDaemonReregisterAndSessionBranches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runners/register" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"runnerId":"runner-new","sessionToken":"token-new","expiresAt":"2035-01-01T00:00:00Z"}`)
	}))
	defer server.Close()
	d, err := New(Config{ServerURL: server.URL, BootstrapToken: "boot", StatePath: filepath.Join(t.TempDir(), "state.json"), Name: "cvR"})
	if err != nil {
		t.Fatal(err)
	}
	d.state.RunnerID = "runner-old"
	if err := d.reregister(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.runnerID() != "runner-new" || d.client.sessionToken() != "token-new" || d.clientSession() != "token-new" {
		t.Fatalf("state=%+v client=%q", d.state, d.client.sessionToken())
	}
	if err := d.ensureRegistered(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.refreshSessionExpiry(time.Time{})
	d.state.ExpiresAt = time.Now().Add(30 * 24 * time.Hour)
	d.refreshSessionExpiry(time.Now().Add(time.Hour))
	if providers := d.availableProviders(); len(providers) < 2 {
		t.Fatalf("providers=%v", providers)
	}
}

func TestExecuteBuiltinInvocation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	root := t.TempDir()
	d := &Daemon{
		cfg:      Config{ServerURL: server.URL, DefaultWorkspaceRoot: root},
		client:   NewClient(server.URL),
		executor: flowruntime.NewExecutorRunner(),
	}
	task := domain.RunnerTask{
		ID: "builtin", RunID: "run", Provider: "mock", Workspace: "runs/run/workspace", Prompt: "hello",
		Invocation: &domain.RunnerInvocation{
			RunID: "run", NodeID: "node", InvocationID: "inv",
			Executor: domain.ExecutorDefinition{Kind: "mock"},
			Agent:    domain.AgentDefinition{ID: "agent", Name: "Agent"},
		},
	}
	result, artifacts := d.executeInvocation(context.Background(), task)
	if result.status != domain.RunnerTaskSuccess || artifacts != nil {
		t.Fatalf("result=%+v artifacts=%v", result, artifacts)
	}
}

func TestExecuteWorkBranchPushAndPull(t *testing.T) {
	remote := bareRemoteWithBaseline(t)
	workspace := t.TempDir()
	repository := domain.RunnerRepository{
		RepositoryID: "repo", Name: "Repo", CloneURL: remote, Ref: "master",
		WorkBranch: "feature/cvr", SyncWorkBranch: true,
	}
	if err := checkoutRepository(context.Background(), workspace, &repository); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "change.txt"), []byte("change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: t.TempDir()}, client: NewClient(server.URL)}
	task := domain.RunnerTask{
		ID: "push", Action: domain.RunnerTaskActionPushWorkBranch, Workspace: workspace, WorkBranchMessage: "cvR change",
		Invocation: &domain.RunnerInvocation{RunID: "run", NodeID: "node", Repositories: []domain.RunnerRepository{repository}},
	}
	result := d.executeWorkBranchSync(context.Background(), task)
	if result.status != domain.RunnerTaskSuccess || !strings.Contains(result.result, "feature/cvr") {
		t.Fatalf("push=%+v", result)
	}
	task.ID = "pull"
	task.Action = domain.RunnerTaskActionPullWorkBranch
	result = d.executeWorkBranchSync(context.Background(), task)
	if result.status != domain.RunnerTaskSuccess {
		t.Fatalf("pull=%+v", result)
	}
}

func TestRunnerWorkspaceCheckpointBranches(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "runs", "check", "workspace")
	_ = os.MkdirAll(workspace, 0o755)
	_ = os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("a"), 0o600)
	d := &Daemon{cfg: Config{DefaultWorkspaceRoot: base}}
	req := domain.RunnerWorkspaceRequest{RunID: "check", WorkspaceLayout: domain.RunnerWorkspaceLayoutShared, RunStatus: "failed"}
	req.Operation = domain.RunnerWorkspaceOperationCheckpointStatus
	if response := d.readRunnerWorkspace(context.Background(), req); response.Status != 200 {
		t.Fatalf("status=%+v", response)
	}
	req.Operation = domain.RunnerWorkspaceOperationCheckpoint
	response := d.readRunnerWorkspace(context.Background(), req)
	if response.Status != 200 {
		t.Fatalf("checkpoint=%+v", response)
	}
	var payload struct {
		Checkpoint []byte `json:"checkpoint"`
	}
	if err := json.Unmarshal(response.Payload, &payload); err != nil || len(payload.Checkpoint) == 0 {
		t.Fatalf("payload=%+v err=%v", payload, err)
	}
	_ = os.RemoveAll(workspace)
	req.Operation = domain.RunnerWorkspaceOperationRestore
	req.Checkpoint = payload.Checkpoint
	if response = d.readRunnerWorkspace(context.Background(), req); response.Status != 200 {
		t.Fatalf("restore=%+v", response)
	}
	req.Operation = domain.RunnerWorkspaceOperationFile
	req.Path = ""
	if response = d.readRunnerWorkspace(context.Background(), req); response.Status != 400 {
		t.Fatalf("empty file=%+v", response)
	}
	req.Operation = domain.RunnerWorkspaceOperationSearch
	req.Query = "x"
	if response = d.readRunnerWorkspace(context.Background(), req); response.Status != 400 {
		t.Fatalf("short search=%+v", response)
	}
}
