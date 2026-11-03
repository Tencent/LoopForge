package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/notification"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"

	"github.com/gin-gonic/gin"
)

// Command-level workspace tests run against a disposable Docker sandbox.
func hostConversationWorkspace(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENTFLOW_TEST_DOCKER") != "1" {
		t.Skip("set AGENTFLOW_TEST_DOCKER=1 for real Docker workspace tests")
	}
}
func configureConversationSandbox(t *testing.T, h *assistantHandlers) {
	t.Helper()
	if os.Getenv("AGENTFLOW_TEST_DOCKER") == "1" {
		err := h.repo.PutDockerfile(domain.DockerfileDefinition{ID: repo.GenericBackendDockerfileID, Name: "Isolated workspace test", ProjectID: "workbench", Shared: true, Content: "FROM alpine:3.20\nRUN apk add --no-cache bash git coreutils && adduser -D -u 1000 user_00\nWORKDIR /workspace\n"})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestConversationWorkspaceToolsForAllProjectParticipants(t *testing.T) {
	names := func(s *AgentToolSession) string {
		var out []string
		for _, d := range mainBotWorkspaceTools(s) {
			out = append(out, d.Name)
		}
		return strings.Join(out, ",")
	}
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p", UserID: 1, ChatSessionID: "chat", ChatOwnerID: 1}
	if got := names(s); !strings.HasPrefix(got, "workbench_workspace_open,workbench_workspace_sop,workbench_workspace_tree") || !strings.Contains(got, "workbench_workspace_submit_mr") {
		t.Fatalf("ordinary project chat tools = %s", got)
	}
	peer := &AgentToolSession{Mode: s.Mode, ProjectID: s.ProjectID, UserID: s.UserID, ChatSessionID: s.ChatSessionID, ChatOwnerID: 2}
	if got := names(peer); got != names(s) {
		t.Fatalf("participant tools differ from creator tools: %s", got)
	}
	for name, mutate := range map[string]func(*AgentToolSession){
		"no project":    func(s *AgentToolSession) { s.ProjectID = "" },
		"consultation":  func(s *AgentToolSession) { s.ExecMode.Consultation = true },
		"no session id": func(s *AgentToolSession) { s.ChatSessionID = "" },
	} {
		c := &AgentToolSession{Mode: s.Mode, ProjectID: s.ProjectID, UserID: s.UserID, ChatSessionID: s.ChatSessionID, ChatOwnerID: s.ChatOwnerID}
		mutate(c)
		if got := names(c); got != "" {
			t.Fatalf("%s exposed %s", name, got)
		}
	}
	task := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "p", UserID: 1, RunID: "run"}
	if got := names(task); strings.Contains(got, "workbench_workspace_open") || !strings.Contains(got, "workbench_workspace_tree") {
		t.Fatalf("task conversation tools = %s", got)
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestConversationWorkspaceOpensBindsAndIsReused(t *testing.T) {
	hostConversationWorkspace(t)
	h, owner := workbenchFixture(t)
	configureConversationSandbox(t, h)
	if err := h.repo.PutUser(domain.User{LoginName: "viewer"}); err != nil {
		t.Fatal(err)
	}
	viewer, _ := h.repo.UserByLoginName("viewer")
	project, _ := h.repo.Project("workbench")
	project.Members = []domain.ProjectMember{{UserID: viewer.ID, Role: "viewer"}}
	if err := h.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	source := gitRepo(t)
	if err := h.repo.PutRepository(domain.Repository{ID: "repo-web", ProjectID: "workbench", Name: "web", LocalPath: source, DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutRepository(domain.Repository{ID: "repo-api", ProjectID: "workbench", Name: "api", LocalPath: gitRepo(t), DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "chat-1", ProjectID: "workbench", Messages: []domain.AssistantMessage{{Role: "user", Content: "改一下 README"}}}); err != nil {
		t.Fatal(err)
	}
	en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
	h.deps.engine = en
	gateway := &agentToolGateway{deps: h.deps}
	gateway.api = New(h.repo, en, tracker.NewService(), "", "")
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "chat-1", ChatOwnerID: owner.ID}

	if _, err := gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_read", map[string]any{"path": "README.md"}); err == nil || !strings.Contains(err.Error(), "workbench_workspace_open") {
		t.Fatalf("read before open must point to workbench_workspace_open: %v", err)
	}
	raw, err := gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_open", map[string]any{"goal": "README 加一行"})
	if err != nil {
		t.Fatal(err)
	}
	var opened struct {
		RunID        string `json:"runId"`
		Repositories []struct {
			Name       string `json:"name"`
			Dir        string `json:"dir"`
			WorkBranch string `json:"workBranch"`
		} `json:"repositories"`
		Reused     bool `json:"reused"`
		WorkBranch string
	}
	if err = json.Unmarshal([]byte(raw), &opened); err != nil || opened.RunID == "" || len(opened.Repositories) != 2 || opened.Reused || !strings.Contains(raw, "web/README.md") || !strings.Contains(raw, "api/README.md") {
		t.Fatalf("open result = %s (%v)", raw, err)
	}
	for _, r := range opened.Repositories {
		if r.Name == "web" && r.Dir == "web" {
			opened.WorkBranch = r.WorkBranch
		}
	}
	if opened.WorkBranch == "" {
		t.Fatalf("web checkout missing: %s", raw)
	}
	run, _ := h.repo.RunState(opened.RunID)
	if !run.IsWorkspace() || run.ExecutionContext.ResponsibleUserID != owner.ID || !strings.Contains(run.Input, "README 加一行") {
		t.Fatalf("workspace run = %+v", run)
	}
	if saved, _ := h.repo.AssistantSession(owner.ID, "chat-1"); saved.WorkspaceRunID != opened.RunID || saved.RunID != "" {
		t.Fatalf("binding not saved: workspace=%q run=%q", saved.WorkspaceRunID, saved.RunID)
	}

	if _, err = gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_write", map[string]any{"path": "web/README.md", "content": "hello\nworld\n", "expectedSha256": runtime.WorkspaceContentSHA("hello\n")}); err != nil {
		t.Fatal(err)
	}
	raw, err = gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_command", map[string]any{"command": "grep -c world web/README.md && git -C web rev-parse --abbrev-ref HEAD"})
	if err != nil || !strings.Contains(raw, `"exitCode":0`) || !strings.Contains(raw, opened.WorkBranch) {
		t.Fatalf("command must run on the work branch %s: %s %v", opened.WorkBranch, raw, err)
	}
	// The model sees only the command's own output: no echoed wrapper, exit
	// marker or shell prompt.
	var ran struct {
		Output string `json:"output"`
	}
	_ = json.Unmarshal([]byte(raw), &ran)
	if got := strings.TrimSpace(ran.Output); got != "1\n"+opened.WorkBranch {
		t.Fatalf("command output carries terminal noise: %q", ran.Output)
	}
	raw, err = gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_command", map[string]any{"command": "printf 'it'\"'\"'s\\n'; echo two >&2; exit 3"})
	if err != nil || !strings.Contains(raw, `"exitCode":3`) {
		t.Fatalf("exit status lost: %s %v", raw, err)
	}
	_ = json.Unmarshal([]byte(raw), &ran)
	if got := strings.TrimSpace(ran.Output); got != "it's\ntwo" {
		t.Fatalf("failing command output = %q", ran.Output)
	}

	// A later turn (fresh tool session) resumes the same workspace.
	next := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "chat-1", ChatOwnerID: owner.ID, workspaceRunID: opened.RunID}
	raw, err = gateway.callMainBotWorkspace(context.Background(), next, "workbench_workspace_open", map[string]any{"goal": "再改一处"})
	if err != nil || !strings.Contains(raw, `"reused":true`) || !strings.Contains(raw, opened.RunID) {
		t.Fatalf("second open must reuse: %s %v", raw, err)
	}
	// Saving the turn without the field keeps the binding.
	if err = h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "chat-1", ProjectID: "workbench", Messages: []domain.AssistantMessage{{Role: "user", Content: "改一下 README"}, {Role: "assistant", Content: "{}"}}}); err != nil {
		t.Fatal(err)
	}
	if saved, _ := h.repo.AssistantSession(owner.ID, "chat-1"); saved.WorkspaceRunID != opened.RunID {
		t.Fatalf("turn save dropped the workspace: %q", saved.WorkspaceRunID)
	}

	// Project access also grants the viewer the conversation's workspace tools.
	if err = h.repo.PutAssistantSession(viewer.ID, domain.AssistantSession{ID: "chat-viewer", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	v := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: viewer.ID, User: viewer, ChatSessionID: "chat-viewer", ChatOwnerID: viewer.ID}
	if _, err = gateway.callMainBotWorkspace(context.Background(), v, "workbench_workspace_open", map[string]any{"goal": "x"}); err != nil {
		t.Fatalf("project participant could not open a workspace: %v", err)
	}
	// Continue the creator's chat in its existing workspace.
	v.ChatSessionID, v.ChatOwnerID = s.ChatSessionID, owner.ID
	v.workspaceRunID = opened.RunID
	if _, err = gateway.callMainBotWorkspace(context.Background(), v, "workbench_workspace_read", map[string]any{"path": "web/README.md"}); err != nil {
		t.Fatalf("project participant could not read shared workspace: %v", err)
	}
	if _, err = gateway.callMainBotWorkspace(context.Background(), v, "workbench_workspace_write", map[string]any{"path": "web/README.md", "content": "shared edit\n", "expectedSha256": runtime.WorkspaceContentSHA("hello\nworld\n")}); err != nil {
		t.Fatalf("project participant could not write shared workspace: %v", err)
	}
	if raw, err = gateway.callMainBotWorkspace(context.Background(), v, "workbench_workspace_command", map[string]any{"command": "cat web/README.md"}); err != nil || !strings.Contains(raw, "shared edit") {
		t.Fatalf("project participant could not run shared workspace command: %s %v", raw, err)
	}
}

// fakeRunnerConnection answers workspace RPCs on the control hub the way a
// connected execution machine would.
func fakeRunnerConnection(t *testing.T, hub *runnerControlHub, runnerID string, answer func(domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse) {
	t.Helper()
	conn := &runnerControlConn{runnerID: runnerID, send: make(chan runnerControlMessage, 64), done: make(chan struct{})}
	hub.mu.Lock()
	hub.conns[runnerID] = conn
	hub.mu.Unlock()
	t.Cleanup(func() { close(conn.done) })
	go func() {
		for {
			select {
			case message := <-conn.send:
				if message.Type == "workspace.request" && message.WorkspaceRequest != nil {
					hub.resolveWorkspace(message.RequestID, answer(*message.WorkspaceRequest))
				}
			case <-conn.done:
				return
			}
		}
	}()
}

// On a cloud runner the checkout only exists after a prepare request. Opening
// a workspace in a fresh chat used to list a directory that did not exist yet.
func TestConversationWorkspaceOpenPreparesRunnerCheckout(t *testing.T) {
	hostConversationWorkspace(t)
	h, owner := workbenchFixture(t)
	configureConversationSandbox(t, h)
	if err := h.repo.PutRepository(domain.Repository{ID: "repo-web", ProjectID: "workbench", Name: "web", CloneURL: "https://git.example.com/team/web.git", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "chat-runner", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutRunner(domain.Runner{ID: "cloud-runner", RunnerVersion: domain.RunnerVersionConversationWorkspace, Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetDefaultRunner(owner.ID, "cloud-runner", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
	h.deps.engine = en
	var hub *runnerControlHub
	gateway := &agentToolGateway{deps: h.deps}
	gateway.api = newWithNotifications(h.repo, en, tracker.NewService(), "", "", notification.New(h.repo), nil, func(_ *assistantHandlers, runs *runsHandlers) { hub = runs.runnerControls })
	var mu sync.Mutex
	var operations []string
	prepared := false
	fakeRunnerConnection(t, hub, "cloud-runner", func(request domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse {
		mu.Lock()
		defer mu.Unlock()
		operations = append(operations, request.Operation)
		switch request.Operation {
		case domain.RunnerWorkspaceOperationPrepare:
			if request.RefreshCleanRepositories || request.PreparationInvocation == nil || len(request.PreparationInvocation.Repositories) != 1 {
				return domain.RunnerWorkspaceResponse{Status: 400, Error: "unexpected prepare request"}
			}
			prepared = true
			raw, _ := json.Marshal(runtime.WorkspacePreparationStatus{Status: "ready", Steps: []runtime.WorkspacePreparationStep{}})
			return domain.RunnerWorkspaceResponse{Payload: raw}
		case domain.RunnerWorkspaceOperationTree:
			if !prepared {
				return domain.RunnerWorkspaceResponse{Status: 404, Error: "stat /runner/workspaces/" + request.RunID + ": no such file or directory"}
			}
			return domain.RunnerWorkspaceResponse{Payload: json.RawMessage(`{"entries":[{"path":"web/README.md","name":"README.md","type":"file","size":6}],"truncated":false}`)}
		}
		return domain.RunnerWorkspaceResponse{Status: 409, Error: "not simulated"}
	})
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "chat-runner", ChatOwnerID: owner.ID}
	raw, err := gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_open", map[string]any{"goal": "README 加一行"})
	if err != nil {
		t.Fatalf("first open on a runner failed: %v", err)
	}
	if !strings.Contains(raw, "web/README.md") {
		t.Fatalf("open must list the checkout: %s", raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(operations) < 2 || operations[0] != domain.RunnerWorkspaceOperationPrepare || operations[1] != domain.RunnerWorkspaceOperationTree {
		t.Fatalf("checkout must precede listing: %v", operations)
	}
}

// Deleting a conversation removes the temporary workspace it opened. Nothing
// else can reach that run, so it would otherwise stay in the run list as an
// idle free collaboration task for every deleted chat.
func TestDeletingConversationRemovesItsWorkspace(t *testing.T) {
	hostConversationWorkspace(t)
	h, owner := workbenchFixture(t)
	configureConversationSandbox(t, h)
	if err := h.repo.PutRepository(domain.Repository{ID: "repo-web", ProjectID: "workbench", Name: "web", LocalPath: gitRepo(t), DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
	h.deps.engine = en
	gateway := &agentToolGateway{deps: h.deps}
	gateway.api = New(h.repo, en, tracker.NewService(), "", "")
	open := func(chat string) string {
		t.Helper()
		if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: chat, ProjectID: "workbench", Messages: []domain.AssistantMessage{{Role: "user", Content: "改一下 README"}}}); err != nil {
			t.Fatal(err)
		}
		s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: chat, ChatOwnerID: owner.ID}
		raw, err := gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_open", map[string]any{"goal": "README 加一行"})
		if err != nil {
			t.Fatal(err)
		}
		var opened struct {
			RunID string `json:"runId"`
		}
		if err := json.Unmarshal([]byte(raw), &opened); err != nil || opened.RunID == "" {
			t.Fatalf("open = %s %v", raw, err)
		}
		return opened.RunID
	}
	remove := func(chat string) {
		t.Helper()
		c, w := workbenchContext(owner, "DELETE", "/", bytes.NewBuffer(nil))
		c.Params = gin.Params{{Key: "id", Value: chat}}
		h.session(c)
		if w.Code != 200 {
			t.Fatalf("delete %s = %d %s", chat, w.Code, w.Body.String())
		}
		if _, ok := h.repo.AssistantSession(owner.ID, chat); ok {
			t.Fatalf("conversation %s not deleted", chat)
		}
	}

	idle := open("chat-idle")
	if run, _ := h.repo.RunState(idle); run.Status != "waiting" {
		t.Fatalf("workspace run status = %s, want an idle free collaboration", run.Status)
	}
	again := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "chat-idle", ChatOwnerID: owner.ID, workspaceRunID: idle}
	second, err := gateway.bindConversationWorkspace(again, workspaceOpenChoice{Goal: "另一个需求", New: true})
	if err != nil || second.Previous != idle {
		t.Fatalf("second workspace = %+v %v", second, err)
	}
	remove("chat-idle")
	for _, id := range []string{idle, second.Run.ID} {
		if _, ok := h.repo.RunState(id); ok {
			t.Fatalf("the deleted conversation's idle workspace run %s was left behind", id)
		}
	}

	busy := open("chat-busy")
	run, _ := h.repo.RunState(busy)
	run.Nodes = map[string]*domain.NodeRun{"helper": {Status: "running"}}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	remove("chat-busy")
	if kept, ok := h.repo.RunState(busy); !ok || kept.Status != "waiting" {
		t.Fatalf("a workspace with work in progress must be left to the run page: ok=%v status=%s", ok, kept.Status)
	}
}

// The model runs arbitrary shell commands in a chat workspace; by default it
// must be the platform container, never a host shell next to the platform's
// configuration and secrets.
func TestConversationWorkspaceFallsBackToGenericDocker(t *testing.T) {
	h, owner := workbenchFixture(t)
	if err := h.repo.PutRepository(domain.Repository{ID: "repo-web", ProjectID: "workbench", Name: "web", LocalPath: gitRepo(t), DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "chat-docker", ProjectID: "workbench"}); err != nil {
		t.Fatal(err)
	}
	h.deps.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
	gateway := &agentToolGateway{deps: h.deps}
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "chat-docker", ChatOwnerID: owner.ID}
	bound, err := gateway.bindConversationWorkspace(s, workspaceOpenChoice{Goal: "改 README"})
	run := bound.Run
	if err != nil {
		t.Fatal(err)
	}
	if got := run.WorkflowSnapshot.SandboxProfileID; got != repo.GenericDockerSandboxID {
		t.Fatalf("workspace run sandbox = %q", got)
	}
}

// bash 5 toggles bracketed paste around every line it reads; with echo on the
// typed wrapper is also printed. Neither may reach the model.
func TestWorkspaceCommandOutputDropsTerminalNoise(t *testing.T) {
	begin, marker := "__main_bot_begin_tok", "__main_bot_exit_tok:"
	echoed := "printf '\\n%s\\n' '" + begin + "'; /bin/sh -c 'x'; printf '\\n" + marker + "%s\\n' \"$?\"; exit\r\n"
	raw := "\x1b[?2004h" + echoed + "\x1b[?2004l\r\n\r\n" + begin + "\r\n1\r\nfeature/fix\r\n\r\n" + marker + "0\r\nexit\r\n"
	out, code, err := workspaceCommandOutput(raw, begin, marker)
	if err != nil || code != 0 || out != "1\nfeature/fix\n" {
		t.Fatalf("output=%q code=%d err=%v", out, code, err)
	}
	out, code, err = workspaceCommandOutput("\x1b[?2004l\r\n\r\n"+begin+"\r\n\r\n"+marker+"3\r\n", begin, marker)
	if err != nil || code != 3 || out != "" {
		t.Fatalf("empty output=%q code=%d err=%v", out, code, err)
	}
	if _, _, err = workspaceCommandOutput("\x1b[?2004h"+echoed, begin, marker); err == nil {
		t.Fatal("echoed wrapper alone must not count as a started command")
	}
}

// A stopped command reports what it printed and how to retry, not a bare
// "context deadline exceeded".
func TestWorkspaceCommandTimeoutKeepsOutputAndHint(t *testing.T) {
	begin := "__main_bot_begin_tok"
	raw := "printf '" + begin + "'\r\n\r\n" + begin + "\r\nstep 1 ok\r\nstep 2 running\r\n"
	msg := workspaceCommandTimeout(30, raw, begin).Error()
	if !strings.Contains(msg, "30 seconds") || !strings.Contains(msg, "timeoutSeconds") || !strings.Contains(msg, "step 1 ok\nstep 2 running") {
		t.Fatalf("timeout message: %q", msg)
	}
	if strings.Contains(msg, "printf") {
		t.Fatalf("wrapper leaked into the timeout output: %q", msg)
	}
	msg = workspaceCommandTimeout(120, "", begin).Error()
	if strings.Contains(msg, "larger timeoutSeconds") || !strings.Contains(msg, "background") || !strings.Contains(msg, "printed nothing") {
		t.Fatalf("max timeout message: %q", msg)
	}
}
