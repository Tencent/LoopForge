package httpapi

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func scheduleSessionFixture(t *testing.T) (*assistantHandlers, domain.User, domain.ScheduleJob) {
	t.Helper()
	st := cvAStore(t)
	if err := st.PutUser(domain.User{LoginName: "sess-owner", CodeBuddyAPIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := st.UserByLoginName("sess-owner")
	if err := st.PutProject(domain.Project{ID: "sess-project", Name: "Sess", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	job := domain.ScheduleJob{ID: "sess-job", Name: "巡检", ProjectID: "sess-project", SkipDispatch: true, AssistantPrompt: "检查", IntervalSec: 3600, Enabled: true}
	h := &assistantHandlers{deps: &deps{repo: st}}
	replies := 0
	h.generate = func(context.Context, string, domain.User) (string, error) {
		replies++
		return "第 " + string(rune('0'+replies)) + " 次巡检完成", nil
	}
	return h, owner, job
}

// By default every execution opens and keeps its own conversation, so each
// execution record can open the one it ran in.
func TestScheduleJobOpensNewConversationPerExecution(t *testing.T) {
	h, owner, job := scheduleSessionFixture(t)
	_, first, err := h.runScheduleJobPrompt(job)
	if err != nil || first == "" {
		t.Fatalf("first run = %q %v", first, err)
	}
	_, second, err := h.runScheduleJobPrompt(job)
	if err != nil || second == "" || second == first {
		t.Fatalf("second run = %q (first %q) %v", second, first, err)
	}
	// The scheduler learns the conversation before the prompt finishes.
	var reported []string
	_, third, err := h.runScheduleJobPromptReporting(job, func(id string) { reported = append(reported, id) })
	if err != nil || len(reported) != 1 || reported[0] != third {
		t.Fatalf("reported %v, run = %q %v", reported, third, err)
	}
	for _, id := range []string{first, second} {
		s, ok := h.repo.AssistantSession(owner.ID, id)
		if !ok || s.Source != domain.AssistantSourceScheduled || s.TaskID != job.ID || len(s.Messages) != 2 {
			t.Fatalf("session %s = %+v found=%v", id, s, ok)
		}
	}
}

// A fixed job continues its pinned conversation, and opens (and pins) a new
// one when the pinned conversation is gone.
func TestScheduleJobFixedSessionContinuesConversation(t *testing.T) {
	h, owner, job := scheduleSessionFixture(t)
	job.SessionMode = domain.ScheduleSessionFixed
	_, pinned, err := h.runScheduleJobPrompt(job)
	if err != nil || pinned == "" {
		t.Fatalf("first fixed run = %q %v", pinned, err)
	}
	job.FixedSessionID = pinned
	msg, again, err := h.runScheduleJobPrompt(job)
	if err != nil || again != pinned {
		t.Fatalf("second fixed run = %q (pinned %q) %v", again, pinned, err)
	}
	if msg != "第 2 次巡检完成" {
		t.Fatalf("second reply = %q", msg)
	}
	s, _ := h.repo.AssistantSession(owner.ID, pinned)
	if len(s.Messages) != 4 || s.Messages[0].Role != "user" || s.Messages[2].Role != "user" || s.Messages[3].Role != "assistant" {
		t.Fatalf("pinned transcript = %+v", s.Messages)
	}
	if err := h.deleteConversation(owner.ID, pinned); err != nil {
		t.Fatal(err)
	}
	_, fresh, err := h.runScheduleJobPrompt(job)
	if err != nil || fresh == "" || fresh == pinned {
		t.Fatalf("run after pinned deleted = %q %v", fresh, err)
	}
	// Another job's conversation is never continued.
	job.FixedSessionID = fresh
	job.ID = "other-job"
	_, other, err := h.runScheduleJobPrompt(job)
	if err != nil || other == fresh {
		t.Fatalf("foreign pinned session reused: %q %v", other, err)
	}
}

// The janitor never trims a job's pinned conversation.
func TestScheduledSessionJanitorKeepsPinnedConversation(t *testing.T) {
	h, owner, job := scheduleSessionFixture(t)
	job.SessionMode = domain.ScheduleSessionFixed
	_, pinned, err := h.runScheduleJobPrompt(job)
	if err != nil {
		t.Fatal(err)
	}
	job.SessionMode = ""
	_, newer, err := h.runScheduleJobPrompt(job)
	if err != nil {
		t.Fatal(err)
	}
	job.SessionMode, job.FixedSessionID = domain.ScheduleSessionFixed, pinned
	if err := h.repo.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	later := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	h.pruneScheduledAssistantSessions(later)
	if _, ok := h.repo.AssistantSession(owner.ID, pinned); !ok {
		t.Fatal("pinned conversation was trimmed")
	}
	if _, ok := h.repo.AssistantSession(owner.ID, newer); !ok {
		t.Fatal("latest conversation was trimmed")
	}
}

// The pinned conversation is server-managed: a save cannot point it at
// another conversation, and leaving fixed mode unpins it.
func TestScheduleJobSaveKeepsSessionModeServerManaged(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "schedule-session-test-secret")
	h, _ := workbenchFixture(t)
	st := h.repo
	if err := st.PutUser(domain.User{LoginName: "mode-owner", CodeBuddyAPIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := st.UserByLoginName("mode-owner")
	if err := st.PutProject(domain.Project{ID: "mode-proj", Name: "Mode", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	server := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	job := domain.ScheduleJob{ID: "mode-job", ProjectID: "mode-proj", Name: "巡检", SkipDispatch: true, AssistantPrompt: "p", IntervalSec: 3600, SessionMode: "fixed", FixedSessionID: "forged", LastSessionID: "forged"}
	saved := requestJSONAuthed[domain.ScheduleJob](t, server, owner, http.MethodPut, "/api/schedule-jobs", job, http.StatusOK)
	if saved.SessionMode != domain.ScheduleSessionFixed || saved.FixedSessionID != "" || saved.LastSessionID != "" {
		t.Fatalf("first save = mode %q fixed %q last %q", saved.SessionMode, saved.FixedSessionID, saved.LastSessionID)
	}
	stored, _ := st.ScheduleJob("mode-job")
	stored.FixedSessionID, stored.LastSessionID = "chat-pinned", "chat-pinned"
	if err := st.PutScheduleJob(stored); err != nil {
		t.Fatal(err)
	}
	saved = requestJSONAuthed[domain.ScheduleJob](t, server, owner, http.MethodPut, "/api/schedule-jobs", job, http.StatusOK)
	if saved.FixedSessionID != "chat-pinned" || saved.LastSessionID != "chat-pinned" {
		t.Fatalf("resave = fixed %q last %q", saved.FixedSessionID, saved.LastSessionID)
	}
	job.SessionMode = "whatever"
	saved = requestJSONAuthed[domain.ScheduleJob](t, server, owner, http.MethodPut, "/api/schedule-jobs", job, http.StatusOK)
	if saved.SessionMode != "" || saved.FixedSessionID != "" {
		t.Fatalf("leaving fixed mode = mode %q fixed %q", saved.SessionMode, saved.FixedSessionID)
	}
}

// A job that opens a conversation per execution keeps only its newest
// SessionKeep conversations; older ones go right after the next execution.
func TestScheduleJobKeepsItsNewestConversations(t *testing.T) {
	h, owner, job := scheduleSessionFixture(t)
	job.SessionKeep = 10
	if err := h.repo.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 12; i++ {
		_, id, err := h.runScheduleJobPrompt(job)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond)
	}
	if got := len(h.repo.ListAssistantSessionsByTask(domain.AssistantSourceScheduled, job.ID)); got != 10 {
		t.Fatalf("kept %d conversations, want 10", got)
	}
	for i, id := range ids {
		_, ok := h.repo.AssistantSession(owner.ID, id)
		if want := i >= 2; ok != want {
			t.Fatalf("conversation %d kept=%v, want %v", i, ok, want)
		}
	}

	// Unlimited keeps everything, also through the daily janitor.
	job.SessionKeep = domain.ScheduleSessionKeepUnlimited
	if err := h.repo.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.runScheduleJobPrompt(job); err != nil {
		t.Fatal(err)
	}
	h.pruneScheduledAssistantSessions(time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))
	if got := len(h.repo.ListAssistantSessionsByTask(domain.AssistantSourceScheduled, job.ID)); got != 11 {
		t.Fatalf("unlimited kept %d conversations, want 11", got)
	}

	// The janitor applies the stored limit whatever the age, and a job
	// saved before the setting keeps the default 50.
	job.SessionKeep = 10
	if err := h.repo.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	if removed := h.pruneScheduledAssistantSessions(time.Now().UTC()); removed != 1 {
		t.Fatalf("janitor removed %d, want 1", removed)
	}
	if got := (domain.ScheduleJob{SkipDispatch: true}).SessionKeepLimit(); got != domain.ScheduleSessionKeepDefault {
		t.Fatalf("default limit = %d", got)
	}
	if got := (domain.ScheduleJob{SkipDispatch: true, SessionMode: domain.ScheduleSessionFixed, SessionKeep: 10}).SessionKeepLimit(); got != 0 {
		t.Fatalf("fixed mode limit = %d", got)
	}
}

// A save accepts only the offered limits and fills in the default.
func TestScheduleJobSaveValidatesSessionKeep(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "schedule-keep-test-secret")
	h, _ := workbenchFixture(t)
	st := h.repo
	if err := st.PutUser(domain.User{LoginName: "keep-owner", CodeBuddyAPIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := st.UserByLoginName("keep-owner")
	if err := st.PutProject(domain.Project{ID: "keep-proj", Name: "Keep", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	server := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	body := func(keep int) map[string]any {
		return map[string]any{"id": "keep-job", "projectId": "keep-proj", "name": "巡检", "skipDispatch": true, "assistantPrompt": "p", "intervalSec": 3600, "sessionKeep": keep}
	}
	if saved := requestJSONAuthed[domain.ScheduleJob](t, server, owner, http.MethodPut, "/api/schedule-jobs", body(0), http.StatusOK); saved.SessionKeep != 50 {
		t.Fatalf("default keep = %d", saved.SessionKeep)
	}
	for _, keep := range []int{10, 100, -1} {
		if saved := requestJSONAuthed[domain.ScheduleJob](t, server, owner, http.MethodPut, "/api/schedule-jobs", body(keep), http.StatusOK); saved.SessionKeep != keep {
			t.Fatalf("keep %d saved as %d", keep, saved.SessionKeep)
		}
	}
	if code := scheduleAuthzStatus(t, server, owner, http.MethodPut, "/api/schedule-jobs", body(7)); code != http.StatusBadRequest {
		t.Fatalf("keep 7 = %d", code)
	}
}
