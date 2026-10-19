package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func mainBotControlFixture(t *testing.T, saved bool, operation string) (http.Handler, *AgentToolSession, *engine.Engine, domain.Run, string) {
	t.Helper()
	a, owner, _, _, _ := sharedWorkbenchFixture(t)
	en := engine.New(a.repo, runtime.NewManager(t.TempDir()))
	en.ConfigureExecution("api-control", "control-host", time.Minute, false)
	run, _ := a.repo.RunState("shared-run")
	run.Status = "failed"
	run.WorkflowSnapshot = domain.WorkflowDefinition{ID: "wf", ProjectID: run.ExecutionContext.ProjectID, Nodes: []domain.WorkflowNode{{ID: "step", Type: "script", Script: "true", MaxAttempts: 1}}}
	run.Nodes = map[string]*domain.NodeRun{"step": {NodeID: "step", Status: "failed", Attempt: 1}}
	if operation == "resume" {
		run.Status = "waiting"
		run.PauseRequested = true
		run.Nodes["step"].Status = "waiting_approval"
		run.Nodes["step"].Activity = "已由用户暂停，继续后脚本将从头执行"
	}
	if err := a.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if operation == "resume" {
		if _, err := a.repo.RequestRunPause(run.ID); err != nil {
			t.Fatal(err)
		}
	}
	token, err := en.BeginConversationWorkspaceUse(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = en.EndConversationWorkspaceUse(context.Background(), run.ID, token) })
	if saved {
		root := en.RunWorkspace(run.ID)
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "retained.txt"), []byte("kept changes"), 0600); err != nil {
			t.Fatal(err)
		}
		archive, err := runtime.CreateWorkspaceCheckpoint(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.repo.SaveWorkspaceCheckpoint(run.ID, token, time.Now().UTC(), archive); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
	}
	run, _ = a.repo.RunState(run.ID)
	run.WorkspaceCleanedAt = time.Now().UTC()
	if err := a.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	session := &AgentToolSession{Mode: AgentToolAssistant, RunID: run.ID, WorkspaceToken: token, ProjectID: run.ExecutionContext.ProjectID, UserID: owner.ID, User: owner, ExecMode: assistantExecMode{AutoWrite: true}, ExpiresAt: time.Now().Add(time.Minute), Final: make(chan AgentTerminalResult, 1)}
	if err := agentToolSessions.create(session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	return New(a.repo, en, tracker.NewService(), "", ""), session, en, run, token
}

func callMainBotControl(t *testing.T, handler http.Handler, session *AgentToolSession, runID, operation string, ctx context.Context) (bool, string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "agentflow__runs_" + operation, "arguments": map[string]any{"path": map[string]any{"id": runID}, "body": map[string]any{"nodeId": "step"}}}})
	req := httptest.NewRequest("POST", "/internal/agent-tools/"+session.Token+"/mcp", bytes.NewReader(raw)).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("RPC HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *agentToolRPCError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, item := range response.Result.Content {
		text += item.Text
	}
	if response.Error != nil {
		return true, response.Error.Message
	}
	return response.Result.IsError, text
}

func TestNativeMainBotControlReusesAuthenticatedRunLease(t *testing.T) {
	for _, saved := range []bool{false, true} {
		for _, operation := range []string{"retry", "resume"} {
			name := operation + "/legacy"
			if saved {
				name = operation + "/snapshot"
			}
			t.Run(name, func(t *testing.T) {
				handler, session, en, run, token := mainBotControlFixture(t, saved, operation)
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				failed, text := callMainBotControl(t, handler, session, run.ID, operation, ctx)
				if failed {
					t.Fatalf("native control collided with its own lease: %s", text)
				}
				checkpoint, _ := en.ConversationWorkspaceStatus(run.ID)
				if checkpoint.LeaseToken != token {
					t.Fatal("control released or replaced the generation lease")
				}
				if saved {
					content, err := os.ReadFile(filepath.Join(en.RunWorkspace(run.ID), "retained.txt"))
					if err != nil || string(content) != "kept changes" {
						t.Fatal("snapshot was not restored", err)
					}
				}
			})
		}
	}
}

func TestNativeMainBotControlDoesNotBorrowAnotherRunOrWriterLease(t *testing.T) {
	for _, kind := range []string{"another-run", "another-writer"} {
		t.Run(kind, func(t *testing.T) {
			handler, session, en, run, token := mainBotControlFixture(t, true, "retry")
			if kind == "another-run" {
				session.RunID = "different-session-run"
			} else {
				session.WorkspaceToken = ""
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			failed, text := callMainBotControl(t, handler, session, run.ID, "retry", ctx)
			if !failed {
				t.Fatal("reused an unrelated workspace lease", text)
			}
			checkpoint, _ := en.ConversationWorkspaceStatus(run.ID)
			if checkpoint.LeaseToken != token {
				t.Fatal("unrelated workspace lease changed")
			}
			if !strings.Contains(text, "deadline") && !strings.Contains(text, "工作区") {
				t.Fatal("unexpected unrelated lease error", text)
			}
		})
	}
}
