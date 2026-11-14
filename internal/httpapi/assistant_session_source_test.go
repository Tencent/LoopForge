package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestScheduledJobKeepsOneMarkedConversation(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "schedule-source", CodeBuddyAPIKey: "test-key"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("schedule-source")
	h := &assistantHandlers{deps: &deps{repo: st}, generate: func(context.Context, string, domain.User) (string, error) {
		return "巡检完成", nil
	}}
	job := domain.ScheduleJob{ID: "job-daily", Name: "每日巡检", OwnerUserID: user.ID, AssistantPrompt: "检查仓库"}
	for i := 0; i < 2; i++ {
		if _, err := h.prepareScheduleJob(job); err != nil {
			t.Fatal(err)
		}
	}
	var scheduled []domain.AssistantSession
	for _, item := range st.ListAssistantSessions(user.ID) {
		if item.Source == domain.AssistantSourceScheduled {
			scheduled = append(scheduled, item)
		}
	}
	if len(scheduled) != 1 {
		t.Fatalf("scheduled conversations = %d, want 1: %+v", len(scheduled), scheduled)
	}
	if scheduled[0].TaskID != job.ID {
		t.Fatalf("task id = %q, want %q", scheduled[0].TaskID, job.ID)
	}
	if scheduled[0].Source != domain.AssistantSourceScheduled {
		t.Fatalf("source = %q, want %q", scheduled[0].Source, domain.AssistantSourceScheduled)
	}
}

func TestSavingScheduledConversationKeepsItsSource(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "keep-source", CodeBuddyAPIKey: "test-key"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("keep-source")
	now := time.Now().UTC()
	if err := st.PutAssistantSession(user.ID, domain.AssistantSession{
		ID: "scheduled-chat", Source: domain.AssistantSourceScheduled, TaskID: "job-1",
		Title: "定时 · 巡检", CreatedAt: now, UpdatedAt: now,
		Messages: []domain.AssistantMessage{{Role: "user", Content: "第一次"}},
	}); err != nil {
		t.Fatal(err)
	}
	item, ok := st.AssistantSession(user.ID, "scheduled-chat")
	if !ok {
		t.Fatal("scheduled session missing")
	}
	item.Source = ""
	item.Messages = append(item.Messages, domain.AssistantMessage{Role: "user", Content: "继续聊"})
	if err := saveAssistantConversation(st, user.ID, item); err != nil {
		t.Fatal(err)
	}
	stored, ok := st.AssistantSession(user.ID, "scheduled-chat")
	if !ok {
		t.Fatal("scheduled session missing after save")
	}
	if stored.Source != domain.AssistantSourceScheduled || stored.TaskID != "job-1" {
		t.Fatalf("source/task = %q/%q, want scheduled/job-1", stored.Source, stored.TaskID)
	}
}

func TestDeleteScheduledSessionsForTaskRemovesOnlyThatTask(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "delete-source", CodeBuddyAPIKey: "test-key"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("delete-source")
	now := time.Now().UTC()
	for _, item := range []domain.AssistantSession{
		{ID: "job-1-a", Source: domain.AssistantSourceScheduled, TaskID: "job-1", Title: "定时 · A", CreatedAt: now, UpdatedAt: now},
		{ID: "job-1-b", Source: domain.AssistantSourceScheduled, TaskID: "job-1", Title: "定时 · B", CreatedAt: now, UpdatedAt: now},
		{ID: "job-2-a", Source: domain.AssistantSourceScheduled, TaskID: "job-2", Title: "定时 · C", CreatedAt: now, UpdatedAt: now},
		{ID: "chat-a", Source: domain.AssistantSourceChat, Title: "普通对话", CreatedAt: now, UpdatedAt: now},
	} {
		if err := st.PutAssistantSession(user.ID, item); err != nil {
			t.Fatal(err)
		}
	}
	h := &assistantHandlers{deps: &deps{repo: st}}
	h.deleteScheduledSessionsForTask("job-1")
	for _, id := range []string{"job-1-a", "job-1-b"} {
		if _, ok := st.AssistantSession(user.ID, id); ok {
			t.Fatalf("%s should be removed", id)
		}
	}
	for _, id := range []string{"job-2-a", "chat-a"} {
		if _, ok := st.AssistantSession(user.ID, id); !ok {
			t.Fatalf("%s should remain", id)
		}
	}
}

func TestPruneScheduledSessionsKeepsLatestAndLeavesSOPAlone(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "prune-source", CodeBuddyAPIKey: "test-key"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("prune-source")
	now := time.Now().UTC()
	put := func(id, source, taskID, title string, updated time.Time, shared bool, runID string) {
		t.Helper()
		if err := st.PutAssistantSession(user.ID, domain.AssistantSession{
			ID: id, Source: source, TaskID: taskID, Title: title, Shared: shared, RunID: runID,
			CreatedAt: updated, UpdatedAt: updated,
			Messages: []domain.AssistantMessage{{Role: "user", Content: "x"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("scheduled-new", domain.AssistantSourceScheduled, "job-1", "定时 · 巡检", now.Add(-time.Hour), false, "")
	put("scheduled-old", domain.AssistantSourceScheduled, "job-1", "定时 · 巡检", now.Add(-4*24*time.Hour), false, "")
	put("legacy-new", domain.AssistantSourceScheduled, "", "定时 · 旧任务", now.Add(-4*24*time.Hour), false, "")
	put("legacy-old", domain.AssistantSourceScheduled, "", "定时 · 旧任务", now.Add(-5*24*time.Hour), false, "")
	put("chat-old", domain.AssistantSourceChat, "", "普通对话", now.Add(-10*24*time.Hour), false, "")
	put("sop-old", domain.AssistantSourceSOP, "", "SOP", now.Add(-10*24*time.Hour), true, "run-1")

	h := &assistantHandlers{deps: &deps{repo: st}}
	if removed := h.pruneScheduledAssistantSessions(now); removed != 3 {
		t.Fatalf("removed = %d, want 3", removed)
	}
	for _, id := range []string{"scheduled-new", "chat-old", "sop-old"} {
		if _, ok := st.AssistantSession(user.ID, id); !ok {
			t.Fatalf("%s was removed", id)
		}
	}
	for _, id := range []string{"scheduled-old", "legacy-new", "legacy-old"} {
		if _, ok := st.AssistantSession(user.ID, id); ok {
			t.Fatalf("%s should be removed", id)
		}
	}
}

func writeSharedRoot(t *testing.T, id string, modified time.Time) string {
	t.Helper()
	root := assistantSharedSessionRoot(id)
	file := filepath.Join(root, "home", ".codebuddy", "plugins", "marketplace.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(root, modified, modified); err != nil {
		t.Fatal(err)
	}
	return root
}

// A scheduled task deletes its previous run every time, so a shared root left
// behind by each deletion grows the disk by a whole CLI home per run.
func TestDeletingConversationRemovesItsSharedRoot(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, login := range []string{"shared-owner", "shared-other"} {
		if err := st.PutUser(domain.User{LoginName: login, CodeBuddyAPIKey: "test-key"}); err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := st.UserByLoginName("shared-owner")
	other, _ := st.UserByLoginName("shared-other")
	now := time.Now()
	for _, id := range []string{"run-1", "run-2"} {
		if err := st.PutAssistantSession(owner.ID, domain.AssistantSession{ID: id, Source: domain.AssistantSourceScheduled, TaskID: "job-1", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	first, second := writeSharedRoot(t, "run-1", now), writeSharedRoot(t, "run-2", now)
	h := &assistantHandlers{deps: &deps{repo: st}}

	if err := h.deleteConversation(other.ID, "run-1"); err == nil {
		t.Fatal("a user who does not own the conversation deleted it")
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("a refused deletion removed the shared root: %v", err)
	}
	h.deleteScheduledSessionsExcept("job-1", "run-2")
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("the deleted run left its shared root: %v", err)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("the kept run lost its shared root: %v", err)
	}
}

func TestPruneOrphanSharedRootsKeepsLiveAndRecentRoots(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "orphan-owner", CodeBuddyAPIKey: "test-key"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := st.UserByLoginName("orphan-owner")
	now := time.Now()
	old := now.Add(-2 * sharedRootOrphanAge)
	if err := st.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "live", CreatedAt: old, UpdatedAt: old}); err != nil {
		t.Fatal(err)
	}
	live := writeSharedRoot(t, "live", old)
	orphan := writeSharedRoot(t, "deleted-long-ago", old)
	recent := writeSharedRoot(t, "being-saved", now)
	foreign := filepath.Join(filepath.Dir(live), "not-a-root")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(foreign, old, old); err != nil {
		t.Fatal(err)
	}

	h := &assistantHandlers{deps: &deps{repo: st}}
	if removed := h.pruneOrphanSharedRoots(now); removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphaned root kept: %v", err)
	}
	for _, path := range []string{live, recent, foreign} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s removed: %v", path, err)
		}
	}
}
