package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func sopHandoffFixture(t *testing.T) (*Engine, *repo.Store, domain.Run, domain.AssistantSession) {
	t.Helper()
	e, s := newRecoveryEngine(t)
	e.ConfigureExecution("handoff-test", "localhost", time.Minute, false)
	wf := domain.WorkflowDefinition{ID: "original", Nodes: []domain.WorkflowNode{{ID: "design", Type: "agent"}, {ID: "build", Type: "agent"}, {ID: "review", Type: "agent"}}, Edges: []domain.WorkflowEdge{{Source: "design", Target: "build"}, {Source: "build", Target: "review"}}}
	states := map[string]*domain.NodeRun{"design": {NodeID: "design", Status: "succeeded", Attempt: 1, Output: "old approved design"}, "build": {NodeID: "build", Status: "succeeded", Attempt: 2}, "review": {NodeID: "review", Status: "succeeded", Attempt: 1}}
	run := domain.Run{ID: "handoff-run", WorkflowID: wf.ID, Status: "waiting", PauseRequested: true, CreatedAt: time.Now().UTC(), Input: "update account name", ExecutionContext: domain.TelemetryContext{ProjectID: "workbench", ResponsibleUserID: 7}, WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group", SOPCheckpoint: &domain.SOPCheckpoint{Workflow: wf, Nodes: states}, Nodes: []domain.WorkflowNode{{ID: "member-task", Name: "开发", AgentID: "dev", RequestID: "one", TaskPrompt: "fix display"}}}, Nodes: map[string]*domain.NodeRun{"member-task": {NodeID: "member-task", Status: "succeeded", Output: "modified ui.js; checks not run", Attempt: 1}}, Events: []domain.RunEvent{{ID: "speech", NodeID: "member-task", Type: "group.message", Message: "I changed ui.js", Timestamp: time.Now().UTC()}}}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.RunWorkspace(run.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.RunWorkspace(run.ID), "ui.js"), []byte("group modification"), 0644); err != nil {
		t.Fatal(err)
	}
	session, err := s.EnsureRunAssistantSession(run.ID, "workbench", "shared", 7)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		session.Messages = append(session.Messages, domain.AssistantMessage{Role: "user", Content: "retain all decisions", AuthorName: "Alice", CreatedAt: run.CreatedAt.Add(time.Duration(i) * time.Nanosecond)})
	}
	session.Messages[0].Content = "EARLY-CONSTRAINT " + strings.Repeat("long requirement ", 300)
	session.Messages[99].Content = "LATEST-CORRECTION use account display name"
	if err := s.PutAssistantSession(session.UserID, session); err != nil {
		t.Fatal(err)
	}
	session, _ = s.SharedAssistantSession(session.ID)
	run, _ = s.RunState(run.ID)
	return e, s, run, session
}

func TestGroupSOPHandoffArchivesAllSpeechAndSurvivesWorkspaceReconstruction(t *testing.T) {
	e, s, run, session := sopHandoffFixture(t)
	err := e.RestoreGroupSOPWithHandoff(context.Background(), run.ID, "build", 99, func(ctx context.Context, r domain.Run, got domain.AssistantSession, raw string) (string, error) {
		if r.ExecutionContext.ResponsibleUserID != 99 || got.ID != session.ID {
			t.Fatal("not bound to requesting actor/session")
		}
		for _, want := range []string{"EARLY-CONSTRAINT", "LATEST-CORRECTION", "I changed ui.js", "checks not run", "unverified"} {
			if !strings.Contains(raw, want) {
				t.Fatalf("lost context %q", want)
			}
		}
		var source struct {
			Conversation []domain.GroupConversationEntry `json:"conversation"`
		}
		if err := json.Unmarshal([]byte(raw), &source); err != nil {
			t.Fatal(err)
		}
		if len(source.Conversation) != 101 || source.Conversation[0].Truncated {
			t.Fatal("used truncated chat projection")
		}
		// The exclusive workspace use also blocks other writers/generations.
		if _, err := e.BeginConversationWorkspaceUse(run.ID); !errors.Is(err, repo.ErrWorkspaceInUse) {
			t.Fatal("workspace was not reserved", err)
		}
		return "## 最新要求\nUse account display name\n## 验证\nNot yet run", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.RunState(run.ID)
	ref := got.WorkflowSnapshot.GroupSOPHandoff
	if ref == nil || got.WorkflowSnapshot.CollaborationMode != "" || !got.PauseRequested || got.Status != "waiting" || got.Nodes["build"].Status != "pending" || got.Nodes["design"].Status != "succeeded" {
		t.Fatalf("bad restored state: %+v", got)
	}
	if got.ExecutionContext.ResponsibleUserID != 7 {
		t.Fatal("handoff changed run owner")
	}
	if len(got.Artifacts) != 2 || len(got.WorkflowSnapshot.GroupHistory) != 1 {
		t.Fatal("archive/history not committed")
	}
	for _, a := range got.Artifacts {
		content, err := os.ReadFile(filepath.Join(e.RunWorkspace(run.ID), a.Path))
		if err != nil || string(content) != a.Content {
			t.Fatalf("file not saved %s %v", a.Path, err)
		}
	}
	old, _ := os.ReadFile(filepath.Join(e.RunWorkspace(run.ID), "ui.js"))
	if string(old) != "group modification" {
		t.Fatal("overwrote group work")
	}
	original, _ := s.SharedAssistantSession(session.ID)
	if repo.SOPHandoffSessionSource(original) != repo.SOPHandoffSessionSource(session) {
		t.Fatal("rewrote chat")
	}
	// A later node and a completely fresh workspace receive the archived summary,
	// without a graph edge from the synthetic handoff artifact.
	root := t.TempDir()
	node := domain.WorkflowNode{ID: "review", Type: "agent"}
	manifest, err := materializeInputs(root, got.WorkflowSnapshot, node, got)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := sopHandoffInputPath(ref, "sop-handoff")
	content, err := os.ReadFile(filepath.Join(root, inputPath))
	if err != nil || !strings.Contains(string(content), "Use account display name") || !strings.Contains(manifest, inputPath) {
		t.Fatal("rebuild lost summary", err)
	}
	// Inputs are authoritative after a retry, even if a previous process modified them.
	if err := os.Chmod(filepath.Join(root, inputPath), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, inputPath), []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeInputs(root, got.WorkflowSnapshot, node, got); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(filepath.Join(root, inputPath))
	if string(content) == "stale" {
		t.Fatal("retry consumed stale handoff")
	}
	_, files := runnerArtifactInputs(got.WorkflowSnapshot, node, got)
	if len(files) != 2 {
		t.Fatalf("runner lost handoff: %v", files)
	}
	for _, f := range files {
		if !strings.HasPrefix(f.Path, ".agentflow/inputs/") {
			t.Fatal("incompatible runner input", f.Path)
		}
	}
	prompt := renderPrompt(got.WorkflowSnapshot, node, got)
	if !strings.Contains(prompt, inputPath) || !strings.Contains(prompt, "必须读取") {
		t.Fatal("node not instructed to read handoff", prompt)
	}
}

func TestGroupSOPHandoffRejectsFailureAndConcurrentChanges(t *testing.T) {
	for _, kind := range []string{"model-failure", "empty", "new-chat", "new-run-state", "write-failure", "active-bot", "invalid-node", "running-member", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			e, s, run, session := sopHandoffFixture(t)
			nodeID := "build"
			called := false
			if kind == "active-bot" {
				if err := s.BeginAssistantGeneration(domain.AssistantGeneration{ID: "busy", SessionID: session.ID, UserID: session.UserID, Status: "running"}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "invalid-node" {
				nodeID = "missing"
			}
			if kind == "running-member" {
				run.Nodes["member-task"].Status = "running"
				if err := s.PutRun(run); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "terminal" {
				e.WorkspaceActive = func(domain.Run) bool { return true }
			}
			err := e.RestoreGroupSOPWithHandoff(context.Background(), run.ID, nodeID, 7, func(ctx context.Context, r domain.Run, chat domain.AssistantSession, raw string) (string, error) {
				called = true
				switch kind {
				case "model-failure":
					return "", errors.New("offline")
				case "empty":
					return " ", nil
				case "new-chat":
					chat.Messages = append(chat.Messages, domain.AssistantMessage{Role: "user", Content: "additional requirement"})
					if err := s.PutAssistantSession(chat.UserID, chat); err != nil {
						t.Fatal(err)
					}
				case "new-run-state":
					e.AppendRunEvent(run.ID, "workspace.changed", "changed while summarizing")
				case "write-failure":
					if err := os.WriteFile(filepath.Join(e.RunWorkspace(run.ID), "handoffs"), []byte("not a directory"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return "handoff", nil
			})
			if err == nil {
				t.Fatal("unsafe handoff succeeded")
			}
			if (kind == "active-bot" || kind == "invalid-node" || kind == "running-member" || kind == "terminal") && called {
				t.Fatal("generated while busy or invalid")
			}
			got, _ := s.RunState(run.ID)
			if got.WorkflowSnapshot.CollaborationMode != "group" || got.WorkflowSnapshot.GroupSOPHandoff != nil || len(got.Artifacts) != 0 {
				t.Fatal("partial switch persisted")
			}
		})
	}
}

func TestGroupSOPHandoffUsesExistingRunnerArchiveTransport(t *testing.T) {
	e, s, run, _ := sopHandoffFixture(t)
	run.ExecutionContext.ExecutionMode = domain.ExecutionModeRunner
	run.ExecutionContext.ExecutionRunnerID = "runner"
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.WorkspaceCheckpointTransport = func(ctx context.Context, r domain.Run, request domain.RunnerWorkspaceRequest) (domain.RunnerWorkspaceResponse, error) {
		if request.Operation != domain.RunnerWorkspaceOperationArtifacts {
			t.Fatal("new runner protocol needed", request.Operation)
		}
		paths := []string{}
		for _, f := range request.ArtifactFiles {
			if f.SHA256 != runtime.WorkspaceContentSHA(f.Content) {
				t.Fatal("bad digest")
			}
			paths = append(paths, f.Path)
		}
		calls++
		raw, _ := json.Marshal(map[string]any{"restoredArtifacts": paths})
		return domain.RunnerWorkspaceResponse{Payload: raw}, nil
	}
	err := e.RestoreGroupSOPWithHandoff(context.Background(), run.ID, "build", 7, func(context.Context, domain.Run, domain.AssistantSession, string) (string, error) {
		return "handoff", nil
	})
	if err != nil || calls != 1 {
		t.Fatal(calls, err)
	}
}

func TestSOPHandoffIsFrozenIntoChildBeforeItStarts(t *testing.T) {
	e, s, parent, _ := sopHandoffFixture(t)
	if err := e.RestoreGroupSOPWithHandoff(context.Background(), parent.ID, "build", 7, func(context.Context, domain.Run, domain.AssistantSession, string) (string, error) {
		return "latest user decision", nil
	}); err != nil {
		t.Fatal(err)
	}
	parent, _ = s.RunState(parent.ID)
	wf := domain.WorkflowDefinition{ID: "handoff-child", Name: "child", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "work", Type: "script", Script: "true", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	ctx := parent.ExecutionContext
	ctx.ParentRunID = parent.ID
	ctx.ParentNodeID = "build"
	child, err := e.StartWithRepositoriesContext(wf.ID, "child task", nil, ctx)
	if err != nil {
		t.Fatal(err)
	}
	ref := child.WorkflowSnapshot.GroupSOPHandoff
	if ref == nil || ref.SummaryArtifactID == parent.WorkflowSnapshot.GroupSOPHandoff.SummaryArtifactID || len(child.Artifacts) != 2 {
		t.Fatal("child context not independently frozen")
	}
	if err := validateSOPHandoffArtifacts(child); err != nil {
		t.Fatal(err)
	}
	_, files := runnerArtifactInputs(child.WorkflowSnapshot, wf.Nodes[0], child)
	if len(files) != 2 || !strings.Contains(files[0].Content, "latest user decision") {
		t.Fatal("child did not inherit handoff")
	}
	imported, err := importChildArtifacts(parent.ID, domain.WorkflowNode{}, 1, child)
	if err != nil || len(imported) != 0 {
		t.Fatal("handoff context leaked into child deliverables", err)
	}
}

// The workspace-control fix routes Worker -> API independently of the public
// MCP URL. Handoff files must use that authenticated path, including failures.
func TestGroupSOPHandoffUsesAuthenticatedWorkspaceControl(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprintf("denied-%t", denied), func(t *testing.T) {
			e, s, run, _ := sopHandoffFixture(t)
			run.ExecutionContext.ExecutionMode = domain.ExecutionModeRunner
			run.ExecutionContext.ExecutionRunnerID = "handoff-control-runner"
			if err := s.PutRun(run); err != nil {
				t.Fatal(err)
			}
			calls := 0
			control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/internal/workspace-checkpoint" || r.Header.Get(controlTokenHeader) != "handoff-test-control-token" {
					t.Error("wrong control destination or missing auth")
				}
				var req domain.RunnerWorkspaceRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				// Failure cleanup may also try to checkpoint the still-paused group.
				// Count handoff publication separately; this is not a second publication.
				if denied && req.Operation == domain.RunnerWorkspaceOperationCheckpoint {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				calls++
				if denied {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if req.RunID != run.ID || req.Operation != domain.RunnerWorkspaceOperationArtifacts || len(req.ArtifactFiles) != 2 {
					t.Error("handoff did not use existing archive transport")
				}
				paths := []string{}
				for _, file := range req.ArtifactFiles {
					paths = append(paths, file.Path)
				}
				payload, _ := json.Marshal(map[string]any{"restoredArtifacts": paths})
				if err := json.NewEncoder(w).Encode(domain.RunnerWorkspaceResponse{Payload: payload}); err != nil {
					t.Error(err)
				}
			}))
			defer control.Close()
			e.ConfigureControl(ControlConfig{APIURL: control.URL, Token: "handoff-test-control-token"})
			e.ConfigurePlatformMCP("http://must-not-use-public-mcp.invalid", "")
			err := e.RestoreGroupSOPWithHandoff(context.Background(), run.ID, "build", 7, func(context.Context, domain.Run, domain.AssistantSession, string) (string, error) {
				return "user decisions", nil
			})
			got, _ := s.RunState(run.ID)
			if calls != 1 {
				t.Fatal("control request missing or duplicated", calls)
			}
			if denied {
				if !errors.Is(err, ErrWorkspaceControlConfiguration) || got.WorkflowSnapshot.CollaborationMode != "group" || len(got.Artifacts) != 0 {
					t.Fatal("authentication failure partially switched SOP", err)
				}
			} else if err != nil || got.WorkflowSnapshot.GroupSOPHandoff == nil {
				t.Fatal("authenticated handoff failed", err)
			}
		})
	}
}

func TestGroupSOPHandoffSecondReturnUpdatesPreviousWithoutOverwritingHistory(t *testing.T) {
	e, s, run, session := sopHandoffFixture(t)
	firstSummary := "ROUND-ONE: retain accessibility; old blue button; tests not run"
	if err := e.RestoreGroupSOPWithHandoff(context.Background(), run.ID, "build", 7, func(context.Context, domain.Run, domain.AssistantSession, string) (string, error) {
		return firstSummary, nil
	}); err != nil {
		t.Fatal(err)
	}
	first, _ := s.RunState(run.ID)
	firstRef := *first.WorkflowSnapshot.GroupSOPHandoff
	var firstContent string
	for _, a := range first.Artifacts {
		if a.ID == firstRef.SummaryArtifactID {
			firstContent = a.Content
		}
	}
	if err := e.EnableTaskGroup(run.ID); err != nil {
		t.Fatal(err)
	}
	session, _ = s.SharedAssistantSession(session.ID)
	session.Messages = append(session.Messages, domain.AssistantMessage{Role: "user", Content: "ROUND-TWO: use green instead of blue; preserve accessibility", CreatedAt: time.Now().UTC()})
	if err := s.PutAssistantSession(session.UserID, session); err != nil {
		t.Fatal(err)
	}
	if err := e.RestoreGroupSOPWithHandoff(context.Background(), run.ID, "build", 7, func(_ context.Context, _ domain.Run, _ domain.AssistantSession, raw string) (string, error) {
		var source struct {
			Previous     string                          `json:"previousHandoff"`
			Mode         string                          `json:"contextMode"`
			Conversation []domain.GroupConversationEntry `json:"conversation"`
		}
		if err := json.Unmarshal([]byte(raw), &source); err != nil {
			t.Fatal(err)
		}
		if source.Previous != firstContent || !strings.Contains(raw, "ROUND-TWO") || source.Mode != "incremental" || len(source.Conversation) != 1 || strings.Contains(raw, "EARLY-CONSTRAINT") {
			t.Fatal("second handoff lost its previous summary or resent old conversation", raw)
		}
		return "ROUND-TWO: retain accessibility; green replaces blue; tests not run", nil
	}); err != nil {
		t.Fatal(err)
	}
	latest, _ := s.RunState(run.ID)
	latestRef := latest.WorkflowSnapshot.GroupSOPHandoff
	if latestRef.ID == firstRef.ID || latestRef.SummaryArtifactID == firstRef.SummaryArtifactID || len(latest.Artifacts) != 4 {
		t.Fatal("overwrote immutable handoff archive")
	}
	for _, a := range latest.Artifacts {
		if a.ID == firstRef.SummaryArtifactID && a.Content != firstContent {
			t.Fatal("modified previous archive")
		}
	}
	_, files := sopHandoffInputs(latest)
	if len(files) != 2 || !strings.Contains(files[0].Content, "green replaces blue") || strings.Contains(files[0].Content, "old blue button") {
		t.Fatal("resumed nodes received stale handoff")
	}
}

func TestGroupSOPHandoffIncrementalBoundaryFallsBackSafely(t *testing.T) {
	for _, kind := range []string{"missing-summary", "corrupt-summary", "missing-transcript", "corrupt-transcript", "invalid-transcript", "foreign-transcript", "edited-speech", "removed-speech"} {
		t.Run(kind, func(t *testing.T) {
			e, s, run, session := sopHandoffFixture(t)
			if err := e.RestoreGroupSOPWithHandoff(context.Background(), run.ID, "build", 7, func(context.Context, domain.Run, domain.AssistantSession, string) (string, error) {
				return "Keep the original accessibility constraint", nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := e.EnableTaskGroup(run.ID); err != nil {
				t.Fatal(err)
			}
			run, _ = s.RunState(run.ID)
			ref := run.WorkflowSnapshot.GroupSOPHandoff
			switch kind {
			case "edited-speech":
				session.Messages[0].Content = "UPDATED EARLY-CONSTRAINT"
			case "removed-speech":
				session.Messages = session.Messages[1:]
			default:
				for i := range run.Artifacts {
					a := &run.Artifacts[i]
					if a.ID == ref.SummaryArtifactID {
						if kind == "missing-summary" {
							a.ID = "missing"
						}
						if kind == "corrupt-summary" {
							a.Content += "tampered"
						}
					}
					if a.ID != ref.TranscriptArtifactID {
						continue
					}
					switch kind {
					case "missing-transcript":
						a.ID = "missing"
					case "corrupt-transcript":
						a.Content += "tampered"
					case "invalid-transcript":
						a.Content = "{}"
						a.SHA256 = runtime.WorkspaceContentSHA(a.Content)
					case "foreign-transcript":
						a.Content = strings.Replace(a.Content, run.ID, "different-run", 1)
						a.SHA256 = runtime.WorkspaceContentSHA(a.Content)
					}
				}
			}
			session.Messages = append(session.Messages, domain.AssistantMessage{Role: "user", Content: "LATEST NEW REQUIREMENT", CreatedAt: time.Now().UTC()})
			raw, err := e.groupSOPHandoffTranscript(run, "build", session)
			if err != nil {
				t.Fatal(err)
			}
			model, err := groupSOPHandoffModelTranscript(run, raw)
			var full, rebuilt sopHandoffArchive
			_ = json.Unmarshal(raw, &full)
			_ = json.Unmarshal(model, &rebuilt)
			if err != nil || len(rebuilt.Conversation) != len(full.Conversation) || rebuilt.PreviousHandoff != "" || !strings.Contains(string(model), "LATEST NEW REQUIREMENT") {
				t.Fatal("invalid prior boundary silently dropped historical context", err)
			}
		})
	}
}

func TestGroupSOPHandoffIncrementalIncludesLateSpeechAndChangedFacts(t *testing.T) {
	previous := sopHandoffArchive{
		RunID: "incremental-run", Goal: "original goal", CapturedAt: time.Now().UTC().Add(-time.Hour),
		Conversation: []domain.GroupConversationEntry{{ID: "conversation/0", Kind: "message", Speaker: "Alice", Content: "Keep accessibility", Timestamp: time.Now().UTC()}},
		Tasks:        []sopHandoffTask{{ID: "dev", Status: "succeeded", Output: "old result"}, {ID: "unchanged", Status: "succeeded", Output: "already summarized"}},
		Artifacts:    []sopHandoffArtifact{{ID: "removed-report", Path: "old.md"}},
		Verification: domain.GroupVerification{Status: "passed", Checks: []domain.GroupVerificationCheck{{ID: "passed-check", Status: "passed"}, {ID: "removed-check", Status: "passed"}}},
	}
	oldRaw, _ := json.Marshal(previous)
	artifacts, err := buildSOPHandoffArtifacts(previous.RunID, "first", "EARLY SUMMARY: Keep accessibility; verify current evidence", oldRaw)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: previous.RunID, Artifacts: artifacts, WorkflowSnapshot: domain.WorkflowDefinition{GroupSOPHandoff: &domain.GroupSOPHandoff{ID: "first", SummaryPath: artifacts[0].Path, SummaryArtifactID: artifacts[0].ID, TranscriptPath: artifacts[1].Path, TranscriptArtifactID: artifacts[1].ID}}}
	current := previous
	current.ResumeNode, current.Goal, current.HumanContext = "review", "updated goal", "new human clarification"
	current.CapturedAt = time.Now().UTC()
	current.Conversation = append(current.Conversation, domain.GroupConversationEntry{ID: "event/late", Kind: "message", Content: "Late public result", Timestamp: previous.CapturedAt.Add(-time.Hour)})
	current.Tasks = []sopHandoffTask{{ID: "dev", Status: "succeeded", Output: "NEW RESULT"}, previous.Tasks[1]}
	current.Artifacts = []sopHandoffArtifact{{ID: "new-report", Path: "new.md"}}
	current.Verification = domain.GroupVerification{Status: "historical", Reason: "workspace changed", Checks: []domain.GroupVerificationCheck{{ID: "passed-check", Status: "passed", Historical: true}}}
	raw, _ := json.Marshal(current)
	model, err := groupSOPHandoffModelTranscript(run, raw)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Mode             string                          `json:"contextMode"`
		Previous         string                          `json:"previousHandoff"`
		Conversation     []domain.GroupConversationEntry `json:"conversation"`
		Tasks            []sopHandoffTask                `json:"tasks"`
		Artifacts        []sopHandoffArtifact            `json:"artifacts"`
		Verification     domain.GroupVerification        `json:"verification"`
		RemovedArtifacts []string                        `json:"removedArtifactIds"`
		RemovedChecks    []string                        `json:"removedVerificationIds"`
	}
	if err := json.Unmarshal(model, &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "incremental" || !strings.Contains(got.Previous, "Keep accessibility") || len(got.Conversation) != 1 || got.Conversation[0].ID != "event/late" || len(got.Tasks) != 1 || got.Tasks[0].Output != "NEW RESULT" || len(got.Artifacts) != 1 || len(got.RemovedArtifacts) != 1 || got.RemovedArtifacts[0] != "removed-report" || len(got.RemovedChecks) != 1 || got.RemovedChecks[0] != "removed-check" || got.Verification.Status != "historical" || len(got.Verification.Checks) != 1 || !got.Verification.Checks[0].Historical {
		t.Fatal("incremental context omitted changed execution evidence", string(model))
	}
	for _, want := range []string{"updated goal", "review", "new human clarification"} {
		if !strings.Contains(string(model), want) {
			t.Fatal("lost current target context", want)
		}
	}
	if strings.Contains(string(model), "already summarized") {
		t.Fatal("unchanged task sent to model again")
	}
}

func TestGroupSOPHandoffLargeArchivePreservesAllPartsAcrossRebuildAndChild(t *testing.T) {
	e, s, run, session := sopHandoffFixture(t)
	large := "LARGE START " + strings.Repeat("完整公开对话；", 90000) + " LARGE END"
	session.Messages = append(session.Messages, domain.AssistantMessage{Role: "user", Content: large, CreatedAt: time.Now().UTC()})
	if err := s.PutAssistantSession(session.UserID, session); err != nil {
		t.Fatal(err)
	}
	if err := e.RestoreGroupSOPWithHandoff(context.Background(), run.ID, "build", 7, func(_ context.Context, _ domain.Run, _ domain.AssistantSession, raw string) (string, error) {
		if !strings.Contains(raw, large) {
			t.Fatal("large history was truncated before summarization")
		}
		return "Keep accessibility and the complete new requirement", nil
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.RunState(run.ID)
	if len(got.Artifacts) < 4 {
		t.Fatal("large archive was not split into bounded transport files")
	}
	selected, err := archivedSOPHandoffArtifacts(got)
	if err != nil {
		t.Fatal(err)
	}
	full, err := readSOPHandoffTranscript(selected)
	if err != nil || !strings.Contains(string(full), large) || !strings.Contains(string(full), "EARLY-CONSTRAINT") {
		t.Fatal("archive missing original history", err)
	}
	for _, a := range selected {
		if len(a.Content) > maxArtifactBytes {
			t.Fatal("incompatible Runner artifact size")
		}
		file, err := os.ReadFile(filepath.Join(e.RunWorkspace(run.ID), a.Path))
		if err != nil || string(file) != a.Content {
			t.Fatal("multipart source file was not published", a.Path, err)
		}
	}
	root := t.TempDir()
	if _, err := materializeSOPHandoffInputs(root, got); err != nil {
		t.Fatal(err)
	}
	_, files := sopHandoffInputs(got)
	if len(files) != len(selected) {
		t.Fatal("Runner inputs lost transcript parts")
	}
	for _, file := range files {
		content, err := os.ReadFile(filepath.Join(root, file.Path))
		if err != nil || string(content) != file.Content {
			t.Fatal("reconstructed workspace lost part", file.Path, err)
		}
	}
	child := domain.Run{ID: "child", ExecutionContext: got.ExecutionContext}
	if err := inheritSOPHandoff(got, &child); err != nil {
		t.Fatal(err)
	}
	if err := validateSOPHandoffArtifacts(child); err != nil || len(child.Artifacts) != len(selected) {
		t.Fatal("child did not inherit multipart handoff", err)
	}
	// The multipart archive can itself be the next round's incremental boundary.
	if err := e.EnableTaskGroup(run.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.RunState(run.ID)
	session.Messages = append(session.Messages, domain.AssistantMessage{Role: "user", Content: "ONLY NEW DECISION", CreatedAt: time.Now().UTC()})
	raw, err := e.groupSOPHandoffTranscript(got, "build", session)
	if err != nil {
		t.Fatal(err)
	}
	model, err := groupSOPHandoffModelTranscript(got, raw)
	if err != nil || !strings.Contains(string(model), "ONLY NEW DECISION") || strings.Contains(string(model), "LARGE START") || len(model) >= maxArtifactBytes {
		t.Fatal("multipart boundary did not reduce model context", err)
	}
	got.Artifacts[len(got.Artifacts)-1].Content += "changed"
	if err := validateSOPHandoffArtifacts(got); err != nil {
		t.Fatal("group mode does not mount SOP inputs", err)
	}
	got.WorkflowSnapshot.CollaborationMode = ""
	if err := validateSOPHandoffArtifacts(got); err == nil {
		t.Fatal("corrupt multipart archive was accepted")
	}
}

func TestGroupSOPHandoffArchiveLimitNeverTruncates(t *testing.T) {
	e, _, run, session := sopHandoffFixture(t)
	session.Messages = append(session.Messages, domain.AssistantMessage{Role: "user", Content: strings.Repeat("x", runtime.WorkspaceCheckpointMaxBytes)})
	if _, err := e.groupSOPHandoffTranscript(run, "build", session); err == nil || !strings.Contains(err.Error(), "12 MB") || !strings.Contains(err.Error(), "未截断") {
		t.Fatal("oversized source should fail clearly, without truncation", err)
	}
}

func TestGroupSOPHandoffOperationProgressAndIdentity(t *testing.T) {
	for _, phase := range []string{"summarizing", "saving", "success", "invalid-id"} {
		t.Run(phase, func(t *testing.T) {
			e, s, run, _ := sopHandoffFixture(t)
			id := "durable-handoff-job"
			if phase == "invalid-id" {
				id = "../bad"
			}
			if phase != "invalid-id" {
				if _, _, err := s.BeginSOPHandoffJob(domain.SOPHandoffJob{ID: id, RunID: run.ID, NodeID: "build", ActorID: 7}); err != nil {
					t.Fatal(err)
				}
				if _, ok, err := s.ClaimSOPHandoffJob(run.ID, id, "test-owner", time.Now().UTC(), time.Minute); err != nil || !ok {
					t.Fatal("claim handoff", err)
				}
			}
			var reports []string
			ctx := WithSOPHandoffOperation(context.Background(), id, func(got string) error {
				reports = append(reports, got)
				if phase == got {
					return errors.New("progress persistence failed")
				}
				return nil
			})
			err := e.RestoreGroupSOPWithHandoff(ctx, run.ID, "build", 7, func(context.Context, domain.Run, domain.AssistantSession, string) (string, error) {
				return "preserve constraints", nil
			})
			got, _ := s.RunState(run.ID)
			if phase == "success" {
				if err != nil || got.WorkflowSnapshot.GroupSOPHandoff == nil || got.WorkflowSnapshot.GroupSOPHandoff.ID != id || strings.Join(reports, ",") != "summarizing,saving" {
					t.Fatal("durable operation not bound to handoff", err, reports)
				}
				if err := e.RestoreGroupSOPWithHandoff(ctx, run.ID, "build", 7, func(context.Context, domain.Run, domain.AssistantSession, string) (string, error) {
					t.Fatal("committed operation summarized twice")
					return "", nil
				}); err != nil {
					t.Fatal("committed job retry was not idempotent", err)
				}
				return
			}
			if err == nil || len(got.Artifacts) != 0 || got.WorkflowSnapshot.CollaborationMode != "group" {
				t.Fatal("failed progress partially committed handoff", err)
			}
			if phase == "invalid-id" && len(reports) != 0 {
				t.Fatal("invalid operation reached model")
			}
		})
	}
}

func TestSOPHandoffRunnerPayloadAccountsForJSONEscaping(t *testing.T) {
	run := domain.Run{WorkflowSnapshot: domain.WorkflowDefinition{GroupSOPHandoff: &domain.GroupSOPHandoff{ID: "long-handoff"}}}
	task := domain.RunnerTask{ID: "task", Invocation: &domain.RunnerInvocation{Inputs: []domain.RunnerInputFile{{Path: ".agentflow/inputs/group-handoff/part.txt", Content: strings.Repeat("x", 2<<20)}}}}
	if err := validateSOPHandoffRunnerPayload(run, task); err != nil {
		t.Fatal("multipart inputs below the Runner response limit must remain usable", err)
	}
	// Backslashes double on the wire. Do not accept raw byte counts that
	// produce a permanently undecodable claim response for an existing Runner.
	task.Invocation.Inputs[0].Content = strings.Repeat("\\", 4<<20)
	if err := validateSOPHandoffRunnerPayload(run, task); err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatal("oversized Runner payload must fail before queueing, without truncation", err)
	}
}
