package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/gitwork"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func quietRunnerHub() *runnerControlHub {
	hub := newRunnerControlHub()
	hub.startupGrace = 0
	hub.reconnectWait = 2 * time.Second
	hub.handoverDrain = time.Second
	return hub
}

func testRunnerConn(runnerID string) *runnerControlConn {
	return &runnerControlConn{runnerID: runnerID, send: make(chan runnerControlMessage, 4), done: make(chan struct{})}
}

// registerTestConn mirrors register without a real websocket.
func registerTestConn(hub *runnerControlHub, conn *runnerControlConn) {
	hub.mu.Lock()
	old := hub.conns[conn.runnerID]
	hub.conns[conn.runnerID] = conn
	close(hub.registered)
	hub.registered = make(chan struct{})
	hub.mu.Unlock()
	if old != nil {
		old.close()
	}
}

// answerOnce serves the next workspace request on conn.
func answerOnce(t *testing.T, hub *runnerControlHub, conn *runnerControlConn, response domain.RunnerWorkspaceResponse) domain.RunnerWorkspaceRequest {
	t.Helper()
	select {
	case message := <-conn.send:
		if message.Type != "workspace.request" || message.WorkspaceRequest == nil {
			t.Fatalf("unexpected message %+v", message)
		}
		hub.resolveWorkspace(conn, message.RequestID, response)
		return *message.WorkspaceRequest
	case <-time.After(3 * time.Second):
		t.Fatal("runner never received the request")
	}
	return domain.RunnerWorkspaceRequest{}
}

type workspaceResult struct {
	response domain.RunnerWorkspaceResponse
	err      error
}

func requestAsync(hub *runnerControlHub, runnerID string, request domain.RunnerWorkspaceRequest) <-chan workspaceResult {
	done := make(chan workspaceResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := hub.requestWorkspace(ctx, runnerID, request)
		done <- workspaceResult{response, err}
	}()
	return done
}

// A runner that is simply offline fails immediately instead of stalling the
// caller for the reconnect window.
func TestRunnerWorkspaceOfflineRunnerFailsFast(t *testing.T) {
	hub := quietRunnerHub()
	started := time.Now()
	_, err := hub.requestWorkspace(context.Background(), "runner-a", domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationTree})
	if !errors.Is(err, errRunnerWorkspaceUnavailable) || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("err=%v after %s", err, time.Since(started))
	}
}

// A socket that just dropped (deploy, network blip) is expected back within
// seconds; the request waits for it instead of failing the tool call.
func TestRunnerWorkspaceWaitsForReconnect(t *testing.T) {
	hub := quietRunnerHub()
	first := testRunnerConn("runner-a")
	registerTestConn(hub, first)
	hub.unregister(first)

	result := requestAsync(hub, "runner-a", domain.RunnerWorkspaceRequest{RunID: "run-a", Operation: domain.RunnerWorkspaceOperationTree})
	time.Sleep(100 * time.Millisecond)
	second := testRunnerConn("runner-a")
	registerTestConn(hub, second)
	answerOnce(t, hub, second, domain.RunnerWorkspaceResponse{Status: 200, Payload: json.RawMessage(`{"ok":true}`)})
	got := <-result
	if got.err != nil || string(got.response.Payload) != `{"ok":true}` {
		t.Fatalf("request across reconnect: %+v err=%v", got.response, got.err)
	}
}

// Once a request reached the socket it may already be running on the runner;
// it must fail rather than run twice (writes, shell commands).
func TestRunnerWorkspaceNeverReplaysDeliveredRequest(t *testing.T) {
	hub := quietRunnerHub()
	first := testRunnerConn("runner-a")
	registerTestConn(hub, first)
	result := requestAsync(hub, "runner-a", domain.RunnerWorkspaceRequest{RunID: "run-a", Operation: domain.RunnerWorkspaceOperationWrite})
	select {
	case <-first.send:
	case <-time.After(3 * time.Second):
		t.Fatal("request not delivered")
	}
	second := testRunnerConn("runner-a")
	registerTestConn(hub, second) // closes first
	got := <-result
	if !errors.Is(got.err, errRunnerWorkspaceUnavailable) {
		t.Fatalf("delivered request must fail on socket loss, got %v", got.err)
	}
	select {
	case message := <-second.send:
		t.Fatalf("delivered request replayed: %+v", message)
	case <-time.After(100 * time.Millisecond):
	}
}

// After a listener handover the old process keeps finishing conversations;
// their workspace calls reach the runner through the successor, including
// the gap before the runner reconnected there.
func TestRunnerWorkspaceRelaysThroughSuccessorAfterHandover(t *testing.T) {
	predecessor, successor := quietRunnerHub(), quietRunnerHub()
	predecessor.successor = successor.requestRelayed
	old := testRunnerConn("runner-a")
	registerTestConn(predecessor, old)

	predecessor.handOver()
	waitClosed(t, old)
	result := requestAsync(predecessor, "runner-a", domain.RunnerWorkspaceRequest{RunID: "run-a", Operation: domain.RunnerWorkspaceOperationTerminal})
	time.Sleep(100 * time.Millisecond)
	moved := testRunnerConn("runner-a")
	registerTestConn(successor, moved)
	request := answerOnce(t, successor, moved, domain.RunnerWorkspaceResponse{Status: 200, Payload: json.RawMessage(`"done"`)})
	if request.RunID != "run-a" || request.Operation != domain.RunnerWorkspaceOperationTerminal {
		t.Fatalf("relayed request changed: %+v", request)
	}
	got := <-result
	if got.err != nil || string(got.response.Payload) != `"done"` {
		t.Fatalf("relayed request: %+v err=%v", got.response, got.err)
	}
}

// A relayed request is served locally only: two processes must never bounce
// a request between each other.
func TestRunnerWorkspaceRelayedRequestIsNotRelayedAgain(t *testing.T) {
	hub := quietRunnerHub()
	hub.reconnectWait = 50 * time.Millisecond
	hub.successor = func(context.Context, string, domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		t.Error("relayed request was relayed again")
		return domain.RunnerWorkspaceResponse{}, nil
	}
	hub.handOver()
	_, err := hub.requestRelayed(context.Background(), "runner-a", domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationTree})
	if !errors.Is(err, errRunnerWorkspaceUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func relayTestEngine(t *testing.T) *engine.Engine {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
}

func TestRunnerWorkspaceRelayEndpointRequiresServiceToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := quietRunnerHub()
	e := relayTestEngine(t)
	router := gin.New()
	router.POST(runnerWorkspaceRelayPath, handleRunnerWorkspaceRelay(e, hub))
	post := func(token string) int {
		req := httptest.NewRequest(http.MethodPost, runnerWorkspaceRelayPath, strings.NewReader(`{"runnerId":"runner-a","request":{"operation":"tree"}}`))
		if token != "" {
			req.Header.Set(engine.ControlTokenHeader, token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}
	// No configured token: the endpoint is closed, even for an empty header.
	if code := post(""); code != http.StatusUnauthorized {
		t.Fatalf("unconfigured token must not authorize, got %d", code)
	}
	e.ConfigureControl(engine.ControlConfig{Token: "service-secret"})
	if code := post("wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	if code := post(""); code != http.StatusUnauthorized {
		t.Fatalf("missing token: %d", code)
	}
}

// End to end over HTTP: the predecessor relays to the successor's endpoint
// with the configured service token.
func TestRunnerWorkspaceRelayOverHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	successor := quietRunnerHub()
	successorEngine := relayTestEngine(t)
	successorEngine.ConfigureControl(engine.ControlConfig{Token: "service-secret"})
	router := gin.New()
	router.POST(runnerWorkspaceRelayPath, handleRunnerWorkspaceRelay(successorEngine, successor))
	server := httptest.NewServer(router)
	defer server.Close()

	predecessorEngine := relayTestEngine(t)
	predecessorEngine.ConfigureControl(engine.ControlConfig{Token: "service-secret", APIURL: server.URL})
	predecessor := quietRunnerHub()
	predecessor.successor = relayRunnerWorkspaceToSuccessor(predecessorEngine)
	predecessor.handOver()

	moved := testRunnerConn("runner-a")
	registerTestConn(successor, moved)
	result := requestAsync(predecessor, "runner-a", domain.RunnerWorkspaceRequest{RunID: "run-a", Operation: domain.RunnerWorkspaceOperationFile, Path: "README.md"})
	request := answerOnce(t, successor, moved, domain.RunnerWorkspaceResponse{Status: 200, Payload: json.RawMessage(`{"content":"hi"}`)})
	if request.Path != "README.md" {
		t.Fatalf("relayed request: %+v", request)
	}
	got := <-result
	if got.err != nil || got.response.Status != 200 || string(got.response.Payload) != `{"content":"hi"}` {
		t.Fatalf("relay over HTTP: %+v err=%v", got.response, got.err)
	}

	// A successor that rejects the credential surfaces as unavailable, not as
	// a silent empty workspace.
	predecessorEngine.ConfigureControl(engine.ControlConfig{Token: "stale", APIURL: server.URL})
	_, err := predecessor.requestWorkspace(context.Background(), "runner-a", domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationTree})
	if !errors.Is(err, errRunnerWorkspaceUnavailable) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("rejected relay: %v", err)
	}
}

func TestRunnerWorkspaceRPCIsTransient(t *testing.T) {
	hub := newRunnerControlHub()
	conn := &runnerControlConn{runnerID: "runner-a", send: make(chan runnerControlMessage, 1), done: make(chan struct{})}
	hub.conns[conn.runnerID] = conn

	type result struct {
		response domain.RunnerWorkspaceResponse
		err      error
	}
	completed := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		response, err := hub.requestWorkspace(ctx, conn.runnerID, domain.RunnerWorkspaceRequest{RunID: "run-a", Operation: domain.RunnerWorkspaceOperationTree})
		completed <- result{response: response, err: err}
	}()

	message := <-conn.send
	if message.Type != "workspace.request" || message.RequestID == "" || message.WorkspaceRequest == nil {
		t.Fatalf("unexpected workspace request: %+v", message)
	}
	payload := json.RawMessage(`{"entries":[],"truncated":false}`)
	hub.resolveWorkspace(conn, message.RequestID, domain.RunnerWorkspaceResponse{Payload: payload, Status: 200})
	got := <-completed
	if got.err != nil || string(got.response.Payload) != string(payload) {
		t.Fatalf("unexpected response: %+v err=%v", got.response, got.err)
	}
	if len(hub.pending) != 0 {
		t.Fatalf("completed transient requests retained in memory: %d", len(hub.pending))
	}
}

func TestRunnerWorkspaceImageBuildUpdatesConversationWhilePreparing(t *testing.T) {
	hub := quietRunnerHub()
	conn := testRunnerConn("runner-a")
	registerTestConn(hub, conn)
	var activity assistantActivityLog
	session := &AgentToolSession{Notify: func(kind string, value any) {
		if kind == "thought" {
			activity.add(value.(assistantStreamEvent))
		}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx = withAssistantDockerBuildProgress(ctx, session)
	completed := make(chan error, 1)
	go func() {
		_, err := hub.requestWorkspace(ctx, conn.runnerID, domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationPrepare})
		completed <- err
	}()
	select {
	case request := <-conn.send:
		if request.Type != "workspace.request" || !request.ObserveImageBuild {
			t.Fatalf("request = %+v", request)
		}
		hub.reportImageBuild(testRunnerConn("runner-b"), request.RequestID, domain.WorkspaceImageBuildProgress{ID: "foreign", Step: "unrelated"})
		hub.reportImageBuild(conn, request.RequestID, domain.WorkspaceImageBuildProgress{ID: "a", Step: "启动构建"})
		hub.reportImageBuild(conn, request.RequestID, domain.WorkspaceImageBuildProgress{ID: "a", Step: "导出镜像"})
		events := activity.snapshot()
		if len(events) != 1 || events[0].Tool != assistantDockerBuildTool || events[0].Detail != "导出镜像" {
			t.Fatalf("live activity = %+v", events)
		}
		hub.reportImageBuild(conn, request.RequestID, domain.WorkspaceImageBuildProgress{ID: "a", Done: true})
		hub.resolveWorkspace(conn, request.RequestID, domain.RunnerWorkspaceResponse{Status: 200})
		if err := <-completed; err != nil {
			t.Fatal(err)
		}
		events = activity.snapshot()
		if len(events) != 2 || events[1].ID != events[0].ID || events[1].Kind != "toolDone" || events[1].Failed {
			t.Fatalf("completed activity = %+v", events)
		}
	case <-ctx.Done():
		t.Fatal("runner did not receive the workspace request")
	}
}

func TestRunnerWorkspaceRelaysImageBuildProgressBeforeReturning(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := quietRunnerHub()
	conn := testRunnerConn("runner-a")
	registerTestConn(hub, conn)
	en := relayTestEngine(t)
	en.ConfigureControl(engine.ControlConfig{Token: "service-secret"})
	router := gin.New()
	router.POST(runnerWorkspaceRelayPath, handleRunnerWorkspaceRelay(en, hub))
	server := httptest.NewServer(router)
	defer server.Close()
	en.ConfigureControl(engine.ControlConfig{Token: "service-secret", APIURL: server.URL})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	updates := make(chan domain.WorkspaceImageBuildProgress, 2)
	ctx = runtime.WithDockerBuildProgress(ctx, func(update domain.WorkspaceImageBuildProgress) { updates <- update })
	completed := make(chan workspaceResult, 1)
	go func() {
		response, err := relayRunnerWorkspaceToSuccessor(en)(ctx, conn.runnerID, domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationPrepare})
		completed <- workspaceResult{response, err}
	}()
	select {
	case request := <-conn.send:
		if !request.ObserveImageBuild {
			t.Fatalf("relayed request did not subscribe: %+v", request)
		}
		want := domain.WorkspaceImageBuildProgress{ID: "relayed-build", Step: "读取 Dockerfile"}
		hub.reportImageBuild(conn, request.RequestID, want)
		select {
		case got := <-updates:
			if got != want {
				t.Fatalf("relayed progress = %+v, want %+v", got, want)
			}
		case <-ctx.Done():
			t.Fatal("relayed progress was not streamed during the request")
		}
		hub.resolveWorkspace(conn, request.RequestID, domain.RunnerWorkspaceResponse{Status: 200})
		if got := <-completed; got.err != nil || got.response.Status != 200 {
			t.Fatalf("relay completion = %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("runner did not receive the relayed request")
	}
}

func TestRunnerWorkspaceSendsScopedPlatformSSHKnownHosts(t *testing.T) {
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hub := quietRunnerHub()
	hub.sshHostLookup = store.PlatformSSHKnownHosts
	conn := testRunnerConn("runner-a")
	registerTestConn(hub, conn)
	_, hostKey := sshTestIdentity(t)
	preparationPin := knownhosts.Line([]string{"gitlab.example.org:2222"}, hostKey) + "\n"
	terminalPin := knownhosts.Line([]string{"terminal.example.org:22"}, hostKey) + "\n"
	otherPin := knownhosts.Line([]string{"other.example.org:22"}, hostKey) + "\n"
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := store.CreatePlatformGitSSHHost(t.Context(), &repo.PlatformGitSSHHost{
		Host: "gitlab.example.org:2222", PublicKeys: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey))),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(terminalPin+otherPin), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(gitwork.SSHKnownHostsEnv, path)
	done := requestAsync(hub, conn.runnerID, domain.RunnerWorkspaceRequest{
		RunID:     "run-a",
		Operation: domain.RunnerWorkspaceOperationTerminal,
		Terminal:  &domain.WorkspaceTerminalRequest{Operation: "open"},
		PreparationInvocation: &domain.RunnerInvocation{
			GitSSHKeys: map[string]string{"ssh://git@gitlab.example.org:2222/group/repo.git": "checkout-identity"},
		},
		TerminalGitSSHKeys: map[string]string{"git@terminal.example.org:group/repo.git": "terminal-identity"},
	})
	got := answerOnce(t, hub, conn, domain.RunnerWorkspaceResponse{Status: http.StatusOK})
	if pins := got.GitSSHKnownHosts; pins == nil || pins.Entries != preparationPin+terminalPin || pins.ReadFailed {
		t.Fatalf("workspace platform pins = %+v", pins)
	}
	if result := <-done; result.err != nil || result.response.Status != http.StatusOK {
		t.Fatalf("workspace response = %+v", result)
	}
}

func TestRunnerWorkspaceRelayHydratesConversationSSHKnownHosts(t *testing.T) {
	predecessor, successor := quietRunnerHub(), quietRunnerHub()
	predecessor.successor = successor.requestRelayed
	predecessor.handOver()
	conn := testRunnerConn("runner-a")
	registerTestConn(successor, conn)

	_, hostKey := sshTestIdentity(t)
	pin := knownhosts.Line([]string{"gitlab.example.org:2222"}, hostKey) + "\n"
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(pin), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(gitwork.SSHKnownHostsEnv, path)
	conversation, err := json.Marshal(runtime.ConversationProcessRequest{
		Operation: "start",
		Command: &runtime.ConversationCommand{
			Credentials: runtime.RunCredentials{GitSSHHostKeys: map[string]string{"gitlab.example.org:2222": "identity"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := requestAsync(predecessor, conn.runnerID, domain.RunnerWorkspaceRequest{
		Operation:    domain.RunnerWorkspaceOperationConversationCLI,
		Conversation: conversation,
	})
	got := answerOnce(t, successor, conn, domain.RunnerWorkspaceResponse{Status: http.StatusOK})
	if pins := got.GitSSHKnownHosts; pins == nil || pins.Entries != pin || pins.ReadFailed {
		t.Fatalf("relayed conversation pins = %+v", pins)
	}
	if result := <-done; result.err != nil || result.response.Status != http.StatusOK {
		t.Fatalf("relayed conversation response = %+v", result)
	}
}

func waitClosed(t *testing.T, conn *runnerControlConn) {
	t.Helper()
	select {
	case <-conn.done:
	case <-time.After(3 * time.Second):
		t.Fatal("handover must drop runner sockets so runners reconnect to the successor")
	}
}

// A request already running on the runner when the listener is handed over
// (a command, a write) finishes on the old socket instead of failing; new
// requests already go to the successor, and the socket closes once idle.
func TestRunnerWorkspaceHandoverDrainsInFlightRequests(t *testing.T) {
	predecessor, successor := quietRunnerHub(), quietRunnerHub()
	predecessor.handoverDrain = 3 * time.Second
	predecessor.successor = successor.requestRelayed
	old := testRunnerConn("runner-a")
	registerTestConn(predecessor, old)

	inFlight := requestAsync(predecessor, "runner-a", domain.RunnerWorkspaceRequest{RunID: "run-a", Operation: domain.RunnerWorkspaceOperationTerminal})
	var delivered runnerControlMessage
	select {
	case delivered = <-old.send:
	case <-time.After(3 * time.Second):
		t.Fatal("request not delivered")
	}
	predecessor.handOver()
	later := requestAsync(predecessor, "runner-a", domain.RunnerWorkspaceRequest{RunID: "run-a", Operation: domain.RunnerWorkspaceOperationTree})
	time.Sleep(100 * time.Millisecond)
	if old.closed() {
		t.Fatal("socket closed while a request was still running on it")
	}
	select {
	case message := <-old.send:
		t.Fatalf("a request after the handover used the draining socket: %+v", message)
	default:
	}
	predecessor.resolveWorkspace(old, delivered.RequestID, domain.RunnerWorkspaceResponse{Status: 200, Payload: json.RawMessage(`"step-4"`)})
	if got := <-inFlight; got.err != nil || string(got.response.Payload) != `"step-4"` {
		t.Fatalf("in-flight request across handover: %+v err=%v", got.response, got.err)
	}
	waitClosed(t, old)
	moved := testRunnerConn("runner-a")
	registerTestConn(successor, moved)
	answerOnce(t, successor, moved, domain.RunnerWorkspaceResponse{Status: 200, Payload: json.RawMessage(`[]`)})
	if got := <-later; got.err != nil || string(got.response.Payload) != `[]` {
		t.Fatalf("request after handover: %+v err=%v", got.response, got.err)
	}
}

// A runner that never comes back cannot hold the old process's sockets open
// forever.
func TestRunnerWorkspaceHandoverDrainIsBounded(t *testing.T) {
	hub := quietRunnerHub()
	hub.handoverDrain = 200 * time.Millisecond
	conn := testRunnerConn("runner-a")
	registerTestConn(hub, conn)
	stuck := requestAsync(hub, "runner-a", domain.RunnerWorkspaceRequest{Operation: domain.RunnerWorkspaceOperationTerminal})
	<-conn.send
	hub.handOver()
	waitClosed(t, conn)
	if got := <-stuck; !errors.Is(got.err, errRunnerWorkspaceUnavailable) {
		t.Fatalf("stuck request: %v", got.err)
	}
}

func TestRunnerWorkspaceCancellationReachesExecutorOnlyForOneShotRequests(t *testing.T) {
	for _, operation := range []string{domain.RunnerWorkspaceOperationCheckpointExport, domain.RunnerWorkspaceOperationPrepare, domain.RunnerWorkspaceOperationConversationCLI} {
		t.Run(operation, func(t *testing.T) {
			hub := quietRunnerHub()
			conn := testRunnerConn("runner-cancel")
			defer conn.close()
			conn.send = make(chan runnerControlMessage, 1)
			registerTestConn(hub, conn)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := hub.requestWorkspace(ctx, conn.runnerID, domain.RunnerWorkspaceRequest{Operation: operation})
				done <- err
			}()
			request := <-conn.send
			wantCancel := operation != domain.RunnerWorkspaceOperationConversationCLI
			if operation == domain.RunnerWorkspaceOperationPrepare {
				// The API must release its waiter without losing the cancel
				// message when the connection's write queue is full.
				conn.send <- runnerControlMessage{Type: "busy"}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("request cancellation blocked")
			}
			if operation == domain.RunnerWorkspaceOperationPrepare {
				<-conn.send
			}
			if !wantCancel {
				select {
				case unexpected := <-conn.send:
					t.Fatalf("persistent CLI lifecycle cancelled by RPC: %+v", unexpected)
				default:
				}
			} else {
				select {
				case stop := <-conn.send:
					if stop.Type != "workspace.cancel" || stop.RequestID != request.RequestID {
						t.Fatalf("wrong cancellation: %+v", stop)
					}
				case <-time.After(time.Second):
					t.Fatal("workspace cancellation not sent")
				}
			}
		})
	}
}
