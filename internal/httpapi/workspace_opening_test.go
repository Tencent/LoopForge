package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/notification"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestRunnerWorkspaceProgressIsRequestScopedAndCancellationIsForwarded(t *testing.T) {
	hub := quietRunnerHub()
	conn := testRunnerConn("runner-a")
	registerTestConn(hub, conn)
	updates := make(chan domain.WorkspaceOpening, 2)
	hub.onWorkspaceProgress = func(id string, p domain.WorkspaceOpening) {
		if id != "run-a" {
			t.Errorf("unexpected run %s", id)
		}
		updates <- p
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := hub.requestWorkspace(ctx, conn.runnerID, domain.RunnerWorkspaceRequest{RunID: "run-a", RunKind: domain.RunKindWorkspace, Operation: domain.RunnerWorkspaceOperationPrepare})
		done <- err
	}()
	message := <-conn.send
	progress := domain.WorkspaceOpening{State: "running", Phase: "clone", Output: "50 KiB/s"}
	hub.resolveWorkspaceProgress(testRunnerConn("runner-b"), message.RequestID, progress)
	hub.resolveWorkspaceProgress(conn, "stale-request", progress)
	select {
	case <-updates:
		t.Fatal("accepted unrelated runner/request progress")
	default:
	}
	hub.resolveWorkspaceProgress(conn, message.RequestID, progress)
	select {
	case p := <-updates:
		if p.Output != progress.Output {
			t.Fatal(p)
		}
	case <-time.After(time.Second):
		t.Fatal("no live progress")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result: %v", err)
	}
	select {
	case msg := <-conn.send:
		if msg.Type != "workspace.cancel" || msg.RequestID != message.RequestID {
			t.Fatalf("wrong cancel: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("runner was not canceled")
	}
	hub.resolveWorkspaceProgress(conn, message.RequestID, progress)
	select {
	case <-updates:
		t.Fatal("accepted progress after cancellation")
	default:
	}
}

func TestWorkspaceCancellationWaitsForBusyControlWriter(t *testing.T) {
	for _, operation := range []string{domain.RunnerWorkspaceOperationPrepare, domain.RunnerWorkspaceOperationCheckpointExport, domain.RunnerWorkspaceOperationCheckpoint} {
		t.Run(operation, func(t *testing.T) {
			hub := quietRunnerHub()
			conn := testRunnerConn("busy-writer")
			conn.send = make(chan runnerControlMessage)
			registerTestConn(hub, conn)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := hub.requestWorkspace(ctx, conn.runnerID, domain.RunnerWorkspaceRequest{RunKind: domain.RunKindWorkspace, Operation: operation})
				done <- err
			}()
			request := <-conn.send
			cancel()
			// Simulate a writer occupied with another message. The cancellation
			// must wait briefly instead of being silently discarded.
			select {
			case err := <-done:
				t.Fatalf("dropped cancellation while writer was busy: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			select {
			case stop := <-conn.send:
				if stop.Type != "workspace.cancel" || stop.RequestID != request.RequestID {
					t.Fatalf("wrong cancellation: %+v", stop)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("remote cancellation was lost")
			}
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel result: %v", err)
			}
		})
	}
}

func TestWorkspaceOpeningFailureKeepsBindingAndRetryUsesSameRun(t *testing.T) {
	h, owner := workbenchFixture(t)
	if err := h.repo.PutRepository(domain.Repository{ID: "repo-web", ProjectID: "workbench", Name: "web", CloneURL: "https://git.example.com/team/web.git", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{
		ID: "chat-retry", ProjectID: "workbench",
		ExecutionTarget: &domain.AssistantExecutionTarget{Pool: domain.ExecutionModeCloud, RunnerID: "cloud-runner"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.PutRunner(domain.Runner{ID: "cloud-runner", RunnerVersion: domain.RunnerVersionConversationCLI, Status: domain.RunnerStatusOnline}); err != nil {
		t.Fatal(err)
	}
	en := engine.New(h.repo, runtime.NewManager(t.TempDir()))
	h.deps.engine = en
	var hub *runnerControlHub
	gateway := &agentToolGateway{deps: h.deps}
	gateway.api = newWithNotifications(h.repo, en, tracker.NewService(), "", "", notification.New(h.repo), nil, func(_ *assistantHandlers, runs *runsHandlers) { hub = runs.runnerControls })
	attempts := 0
	var ids []string
	fakeRunnerConnection(t, hub, "cloud-runner", func(request domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse {
		if request.Operation == domain.RunnerWorkspaceOperationPrepare {
			attempts++
			ids = append(ids, request.RunID)
			if attempts == 1 {
				return domain.RunnerWorkspaceResponse{Status: 500, Error: "docker build: apt mirror unreachable"}
			}
		}
		return domain.RunnerWorkspaceResponse{Payload: []byte(`{"entries":[],"status":"ready"}`)}
	})
	s := &AgentToolSession{Mode: AgentToolAssistant, ProjectID: "workbench", UserID: owner.ID, User: owner, ChatSessionID: "chat-retry", ChatOwnerID: owner.ID}
	if _, err := gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_open", map[string]any{"goal": "修复 README"}); err == nil || !strings.Contains(err.Error(), "已保留绑定") || !strings.Contains(err.Error(), "apt mirror unreachable") {
		t.Fatalf("failure not actionable: %v", err)
	}
	first := s.workspaceRunID
	if first == "" {
		t.Fatal("failed workspace was discarded")
	}
	run, _ := h.repo.RunState(first)
	if run.Status != "waiting" || run.LatestWorkspaceOpening() == nil || run.LatestWorkspaceOpening().State != "failed" {
		t.Fatalf("failure not retained: %+v", run)
	}
	if _, err := gateway.callMainBotWorkspace(context.Background(), s, "workbench_workspace_open", map[string]any{"goal": "重试当前工作区"}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != first || ids[1] != first || s.workspaceRunID != first {
		t.Fatalf("retry allocated another run: %v, %s", ids, s.workspaceRunID)
	}
}

func TestWorkspaceOpeningFailureAppearsInProblemsWithoutNodes(t *testing.T) {
	run := domain.Run{Kind: domain.RunKindWorkspace, Status: "waiting", Events: []domain.RunEvent{{WorkspaceOpening: &domain.WorkspaceOpening{State: "failed", Phase: "build", Message: "docker build: apt mirror unreachable"}}}}
	problems := assistantRunProblems(run, "owner", 2000)
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "apt mirror unreachable") {
		t.Fatalf("missing workspace failure: %+v", problems)
	}
	run.Events = append(run.Events, domain.RunEvent{WorkspaceOpening: &domain.WorkspaceOpening{State: "ready", Message: "ready"}})
	if problems := assistantRunProblems(run, "owner", 2000); len(problems) != 0 {
		t.Fatalf("retry retained stale failure: %+v", problems)
	}
}
