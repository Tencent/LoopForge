package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
)

func TestConversationWorkspaceStartupFailureFallsBackToPlatform(t *testing.T) {
	for _, tc := range []struct {
		name        string
		startError  string
		fromHandoff bool
	}{
		{"Docker mount failure during handoff", "docker start: invalid mount configuration", true},
		{"CLI install timeout in saved workspace", "准备 codebuddy 原生CLI失败：context deadline exceeded", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, user := workbenchFixture(t)
			h.sourceRoot = t.TempDir()
			h.engine = engine.New(h.repo, runtime.NewManager(t.TempDir()))
			h.runnerControls = quietRunnerHub()
			const chatID = "workspace-fallback"
			const workspaceID = "bound-workspace"
			const runnerID = "workspace-runner"
			const question = "请查询当前项目的需求"
			target := domain.AssistantExecutionTarget{Pool: domain.ExecutionModeRunner, RunnerID: runnerID}
			if err := h.repo.PutRunner(domain.Runner{ID: runnerID, OwnerUserID: user.ID, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionConversationCLI}); err != nil {
				t.Fatal(err)
			}
			if err := h.repo.SetRunnerSharedProjects(runnerID, user.ID, []string{"workbench"}); err != nil {
				t.Fatal(err)
			}
			run := domain.Run{
				ID: workspaceID, Kind: domain.RunKindWorkspace, Status: "waiting",
				ExecutionContext: domain.TelemetryContext{
					ProjectID: "workbench", ExecutionMode: target.Pool, ExecutionRunnerID: runnerID, ResponsibleUserID: user.ID,
				},
				WorkflowSnapshot: domain.WorkflowDefinition{
					ID: domain.ConversationWorkspaceWorkflowID, SandboxProfileID: "docker",
					SandboxSnapshot: &domain.RunnerSandboxSpec{Profile: domain.SandboxProfile{ID: "docker", Driver: domain.SandboxDriverDocker}},
				},
			}
			if err := h.repo.PutRun(run); err != nil {
				t.Fatal(err)
			}
			conversation := domain.AssistantSession{ID: chatID, ProjectID: "workbench", WorkspaceRunID: workspaceID, ExecutionTarget: &target}
			if !tc.fromHandoff {
				conversation.Messages = []assistantMessage{{Role: "user", Content: "旧问题"}, {Role: "assistant", Content: "旧回复"}}
			}
			if err := h.repo.PutAssistantSession(user.ID, conversation); err != nil {
				t.Fatal(err)
			}
			if !tc.fromHandoff {
				if err := h.repo.SetAssistantRuntimeMode(user.ID, chatID, conversation.ProjectID, domain.AssistantModeWorkspace); err != nil {
					t.Fatal(err)
				}
				old := assistantNativeSession{root: assistantSharedSessionRoot(chatID), checkpoint: assistantNativeCheckpoint{
					SessionID: "previous-native-session", Ready: true, MessageHashes: assistantMessageHashes(conversation.Messages),
				}}
				if err := os.MkdirAll(old.root, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := old.writeCheckpoint(); err != nil {
					t.Fatal(err)
				}
			}
			h.engine.WorkspaceCheckpointTransport = func(_ context.Context, _ domain.Run, req domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
				if req.Operation == domain.RunnerWorkspaceOperationPrepare {
					return domain.RunnerWorkspaceResponse{Status: 200}, nil
				}
				return domain.RunnerWorkspaceResponse{Status: 404, Error: "no remote workspace files in fixture"}, nil
			}
			t.Cleanup(func() { waitWorkspaceCheckpointSettled(t, h.repo, workspaceID) })

			type cliStart struct {
				workspace string
				request   runtime.ConversationProcessRequest
			}
			var mu sync.Mutex
			var starts []cliStart
			fakeRunnerConnection(t, h.runnerControls, runnerID, func(envelope domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse {
				var req runtime.ConversationProcessRequest
				if err := json.Unmarshal(envelope.Conversation, &req); err != nil {
					return domain.RunnerWorkspaceResponse{Status: 400, Error: err.Error()}
				}
				mu.Lock()
				defer mu.Unlock()
				result := runtime.ConversationProcessResult{ID: req.ID, Root: req.SourceRoot}
				switch req.Operation {
				case "start":
					starts = append(starts, cliStart{workspace: envelope.RunID, request: req})
					if envelope.RunID != "" {
						return domain.RunnerWorkspaceResponse{Status: 409, Error: tc.startError}
					}
					s, ok := agentToolSessions.get(req.Token)
					if !ok {
						t.Error("platform phase has no tool session")
						break
					}
					if tc.fromHandoff && len(starts) == 1 {
						// The workspace-open tool has prepared the existing binding.
						s.requestMode(domain.AssistantModeWorkspace)
						break
					}
					gateway := &agentToolGateway{deps: h.deps}
					_, err := gateway.openConversationWorkspace(context.Background(), s, nil)
					if err == nil || err.Error() != assistantWorkspaceFallbackPrompt {
						t.Errorf("fallback phase allowed another workspace handoff: %v", err)
					}
				case "poll":
					reply := `{"type":"result","result":"已查询项目需求。"}`
					if tc.fromHandoff && len(starts) == 1 {
						reply = `{"type":"result","result":"好的。"}`
					}
					result.Done = true
					result.Sequence = 1
					result.Events = []domain.TelemetryEvent{{Name: "process.stdout", Message: reply}}
				}
				payload, _ := json.Marshal(result)
				return domain.RunnerWorkspaceResponse{Status: 200, Payload: payload}
			})
			body, _ := json.Marshal(assistantRequest{
				ProjectID: conversation.ProjectID, SessionID: chatID,
				Messages: append(conversation.Messages, assistantMessage{Role: "user", Content: question}),
			})
			c, response := workbenchContext(user, "POST", "/api/assistant/generations", bytes.NewBuffer(body))
			h.startGeneration(c)
			if response.Code != 202 {
				t.Fatalf("start generation: %d %s", response.Code, response.Body.String())
			}
			g := waitGeneration(t, h, user.ID, chatID, func(g domain.AssistantGeneration) bool { return g.Status != "" && g.Status != "running" })
			if g.Status != "completed" || g.RuntimeMode != domain.AssistantModePlatform || g.WorkspaceRunID != workspaceID ||
				!strings.Contains(string(g.Reply), "已查询项目需求。") {
				t.Fatalf("generation did not continue in platform mode: %+v", g)
			}
			if !slices.ContainsFunc(g.Events, func(event domain.AssistantGenerationEvent) bool {
				return bytes.Contains(event.Data, []byte(assistantWorkspaceFallbackStatus))
			}) {
				t.Fatal("missing fallback notice in the generation stream")
			}
			saved, _ := h.repo.AssistantSession(user.ID, chatID)
			if saved.Mode() != domain.AssistantModePlatform || saved.WorkspaceRunID != workspaceID || saved.Execution() != target ||
				len(saved.Messages) != len(conversation.Messages)+2 || saved.Messages[len(saved.Messages)-2].Content != question {
				t.Fatalf("fallback did not preserve the conversation: %+v", saved)
			}

			mu.Lock()
			defer mu.Unlock()
			want := []string{workspaceID, ""}
			if tc.fromHandoff {
				want = append([]string{""}, want...)
			}
			if len(starts) != len(want) {
				t.Fatalf("started %d phases, want %d", len(starts), len(want))
			}
			for i, phase := range starts {
				if phase.workspace != want[i] {
					t.Fatalf("phase %d used workspace %q, want %q", i, phase.workspace, want[i])
				}
			}
			failed, fallback := starts[len(starts)-2].request, starts[len(starts)-1].request
			resume := func(req runtime.ConversationProcessRequest) string {
				args := req.Command.Argv
				if i := slices.Index(args, "--resume"); i >= 0 && i+1 < len(args) {
					return args[i+1]
				}
				return ""
			}
			if resume(failed) == "" || resume(fallback) != resume(failed) || fallback.Token == failed.Token {
				t.Fatal("fallback must resume the same CLI journal with a fresh tool token")
			}
			prompt := fallback.Command.Argv[len(fallback.Command.Argv)-1]
			if !strings.Contains(prompt, assistantWorkspaceFallbackPrompt) || strings.Contains(prompt, assistantWorkspaceSystemPrompt) {
				t.Fatal("fallback received stale workspace instructions")
			}
			if tc.fromHandoff == strings.Contains(prompt, question) {
				t.Errorf("unexpected user prompt replay with handoff=%t", tc.fromHandoff)
			}
		})
	}
}
