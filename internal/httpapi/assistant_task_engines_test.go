package httpapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestAssistantScheduledModelKeepsCLIModels(t *testing.T) {
	catalog := assistantFixedCatalog()
	for in, want := range map[string]string{
		"codex:gpt-6-sol":    "codex:gpt-6-sol",
		" claude-code:opus ": "claude-code:opus",
		catalog.Default:      catalog.Default,
		"glm-5.3-ioa":        catalog.Default,
		"unknown-model":      catalog.Default,
		"codex:bad model":    catalog.Default,
		"":                   catalog.Default,
	} {
		if got := assistantScheduledModel(in, catalog); got != want {
			t.Errorf("assistantScheduledModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// A scheduled task on another CLI runs with that CLI's own key, needs no
// CodeBuddy key, and records the CLI on its conversation.
func TestScheduledTaskRunsOnTaskCLI(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutProject(domain.Project{ID: "cli-project", Name: "CLI"})
	_ = st.PutUser(domain.User{LoginName: "codex-only", CodexAPIKey: "codex-secret"})
	user, _ := st.UserByLoginName("codex-only")
	h := &assistantHandlers{deps: &deps{repo: st}}
	task := assistantTask{ID: "cli-job", Name: "巡检", Prompt: "检查", ProjectID: "cli-project", Model: "codex:gpt-6-sol", IntervalSec: 60, Enabled: true}
	h.generate = func(context.Context, string, domain.User) (string, error) { return "完成 codex-secret", nil }
	if err := h.executeAssistantTask(context.Background(), user, &task); err != nil {
		t.Fatalf("codex task = %v %+v", err, task)
	}
	if task.Model != "codex:gpt-6-sol" || strings.Contains(task.LastMessage, "codex-secret") || strings.Contains(task.LastMessage, "[redacted]完") {
		t.Fatalf("codex task result = %+v", task)
	}
	session, _ := st.AssistantSession(user.ID, task.SessionID)
	if session.Model != "codex:gpt-6-sol" || assistantConversationEngine(session.Messages) != assistantEngineCodex {
		t.Fatalf("codex session = model %q engine %q", session.Model, assistantConversationEngine(session.Messages))
	}

	task.Model = "claude-code:opus"
	if err := h.executeAssistantTask(context.Background(), user, &task); err == nil || !strings.Contains(task.LastError, "Claude Code API Key") {
		t.Fatalf("missing claude key = %v %q", err, task.LastError)
	}
	task.Model = ""
	if err := h.executeAssistantTask(context.Background(), user, &task); err == nil || !strings.Contains(task.LastError, "CodeBuddy API Key") {
		t.Fatalf("missing codebuddy key = %v %q", err, task.LastError)
	}
}

func TestScheduleJobAssistantUserNeedsModelCLIKey(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutUser(domain.User{LoginName: "owner-codex", CodexAPIKey: "k"})
	owner, _ := st.UserByLoginName("owner-codex")
	_ = st.PutProject(domain.Project{ID: "owner-project", Name: "Owner", OwnerID: owner.ID})
	h := &assistantHandlers{deps: &deps{repo: st}}
	job := domain.ScheduleJob{ProjectID: "owner-project"}
	if got, err := h.scheduleJobAssistantUser(job, "codex:gpt-6-sol"); err != nil || got.ID != owner.ID {
		t.Fatalf("codex owner = %+v %v", got, err)
	}
	if _, err := h.scheduleJobAssistantUser(job, "glm-5.3-ioa"); err == nil || !strings.Contains(err.Error(), "CodeBuddy") {
		t.Fatalf("codebuddy without key = %v", err)
	}
}

// A scheduled run sees the project's skills in its CLI home, as a chat turn
// does, so the CLI's skill tool can load them.
func TestScheduledTaskWritesProjectSkills(t *testing.T) {
	h, user := workbenchFixture(t)
	if err := h.repo.PutSkill(domain.SkillDefinition{ID: "repo-reader", Name: "repo-reader", ProjectID: "workbench", Description: "读取仓库", Content: "# repo-reader\n读取仓库内容"}); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "codebuddy")
	script := `#!/bin/sh
skills=$(ls "$HOME/.codebuddy/skills" 2>/dev/null | tr '\n' ' ')
printf '{"type":"result","result":"skills: %s"}\n' "$skills"
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_CODEBUDDY_BIN", bin)
	task := assistantTask{ID: "skill-job", Name: "skill", ProjectID: "workbench", Prompt: "加载 skill"}
	if err := h.executeAssistantTask(context.Background(), user, &task); err != nil {
		t.Fatalf("run = %v %+v", err, task)
	}
	if !strings.Contains(task.LastMessage, "repo-reader") {
		t.Fatalf("CLI home lacks the project skill: %q", task.LastMessage)
	}
}

// joinScheduledProject makes userID a member of projectID. A scheduled run is
// an ordinary project conversation, so its runner must be able to continue
// that project's conversations.
func joinScheduledProject(t *testing.T, st *repo.Store, projectID string, userID int64) {
	t.Helper()
	p, ok := st.Project(projectID)
	if !ok {
		t.Fatalf("project %s missing", projectID)
	}
	p.Members = append(p.Members, domain.ProjectMember{UserID: userID, Role: "member"})
	if err := st.PutProject(p); err != nil {
		t.Fatal(err)
	}
}

// A scheduled run is an ordinary chat turn: a live generation the page can
// follow, a transcript saved by chat, and the scheduled session's metadata.
func TestScheduledTaskRunsAsChatTurn(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutProject(domain.Project{ID: "turn-project", Name: "Turn"})
	_ = st.PutIssue(domain.Issue{ID: "turn-issue", ProjectID: "turn-project", Title: "最新需求标题", Description: "需求详情", Status: "todo"})
	_ = st.PutUser(domain.User{LoginName: "turn-user", CodeBuddyAPIKey: "secret"})
	user, _ := st.UserByLoginName("turn-user")
	h := &assistantHandlers{deps: &deps{repo: st}}
	task := assistantTask{ID: "turn-job", Name: "巡检", Prompt: "检查", ProjectID: "turn-project", IntervalSec: 60, Enabled: true, References: []domain.AssistantReference{{Type: "issue", ID: "turn-issue", Name: "客户端伪造名称"}}}

	var during domain.AssistantGeneration
	live := false
	var generatedPrompt string
	h.generate = func(_ context.Context, prompt string, _ domain.User) (string, error) {
		generatedPrompt = prompt
		during, _ = st.AssistantGeneration(user.ID, task.SessionID)
		_, live = assistantLiveGenerations.Load(during.ID)
		return "巡检完成\n\n```notification\n{\"title\":\"T\",\"summary\":\"S\",\"body\":\"B\"}\n```", nil
	}
	if err := h.executeAssistantTask(context.Background(), user, &task); err != nil {
		t.Fatalf("run = %v %+v", err, task)
	}
	if during.Status != "running" || !live {
		t.Fatalf("generation during run = %+v live=%v", during, live)
	}
	done, _ := st.AssistantGeneration(user.ID, task.SessionID)
	if done.ID != during.ID || done.Status != "completed" {
		t.Fatalf("generation after run = %+v", done)
	}
	if task.LastStatus != "ok" || task.LastMessage != "巡检完成" || !strings.Contains(task.NotificationResult, "```notification") {
		t.Fatalf("task result = %+v", task)
	}
	if !strings.Contains(generatedPrompt, "最新需求标题") || strings.Contains(generatedPrompt, "客户端伪造名称") {
		t.Fatalf("scheduled reference context = %q", generatedPrompt)
	}
	session, _ := st.AssistantSession(user.ID, task.SessionID)
	if session.Source != domain.AssistantSourceScheduled || session.TaskID != task.ID || session.Title != "定时 · 巡检" {
		t.Fatalf("scheduled session metadata = source %q task %q title %q", session.Source, session.TaskID, session.Title)
	}
	if n := len(session.Messages); n != 2 || session.Messages[1].Model == "" || strings.Contains(session.Messages[1].Content, "```notification") {
		t.Fatalf("stored transcript = %+v", session.Messages)
	}
	if len(session.Messages[0].References) != 1 || session.Messages[0].References[0].Name != "最新需求标题" {
		t.Fatalf("resolved scheduled references = %+v", session.Messages[0].References)
	}

	first := task.SessionID
	if err := h.executeAssistantTask(context.Background(), user, &task); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.AssistantSession(user.ID, first); ok || task.SessionID == first {
		t.Fatal("a new run must replace the previous scheduled session")
	}
}

// Stopping a scheduled run from its page cancels it like a chat turn.
func TestScheduledTaskStopFromPage(t *testing.T) {
	st := cvAStore(t)
	_ = st.PutProject(domain.Project{ID: "stop-project", Name: "Stop"})
	_ = st.PutUser(domain.User{LoginName: "stop-user", CodeBuddyAPIKey: "secret"})
	user, _ := st.UserByLoginName("stop-user")
	h := &assistantHandlers{deps: &deps{repo: st}}
	task := assistantTask{ID: "stop-job", Name: "巡检", Prompt: "检查", ProjectID: "stop-project", IntervalSec: 60, Enabled: true}
	h.generate = func(ctx context.Context, _ string, _ domain.User) (string, error) {
		g, _ := st.AssistantGeneration(user.ID, task.SessionID)
		if err := st.CancelAssistantGeneration(user.ID, task.SessionID, g.ID); err != nil {
			t.Error(err)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "", errors.New("run was not cancelled")
		}
	}
	if err := h.executeAssistantTask(context.Background(), user, &task); err == nil {
		t.Fatal("stopped run reported success")
	}
	g, _ := st.AssistantGeneration(user.ID, task.SessionID)
	if g.Status != "cancelled" || task.LastStatus != "error" {
		t.Fatalf("stopped generation = %+v task = %+v", g, task)
	}
}
