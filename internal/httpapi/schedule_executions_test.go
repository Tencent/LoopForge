package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// A Trace ID pasted into a Studio chat resolves through the read-only
// platform tool, scoped to the conversation's project.
func TestAssistantLooksUpScheduleExecutionByTrace(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	user, ok := st.UserByLoginName("demo-owner")
	if !ok {
		t.Fatal("seed user")
	}
	for _, p := range []string{"proj-trace", "proj-other"} {
		if err := st.PutProject(domain.Project{ID: p, Name: p, OwnerID: user.ID}); err != nil {
			t.Fatal(err)
		}
		if err := st.PutScheduleJob(domain.ScheduleJob{ID: p + "-job", ProjectID: p, Name: "巡检", SkipDispatch: true, AssistantPrompt: "p", IntervalSec: 3600, OwnerUserID: user.ID}); err != nil {
			t.Fatal(err)
		}
	}
	const trace = "8a2f553171ae877bf374a6b79fd6d890"
	for _, e := range []domain.ScheduleJobExecution{
		{JobID: "proj-trace-job", JobName: "巡检", ProjectID: "proj-trace", TraceID: trace, Status: "failed", Error: "CodeBuddy key expired", StartedAt: time.Now().UTC()},
		{JobID: "proj-trace-job", ProjectID: "proj-trace", TraceID: "other-trace", Status: "success", StartedAt: time.Now().UTC()},
	} {
		if err := st.PutScheduleJobExecution(e); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Contains(agentflowAssistantReadToolNames(), "schedule_job_executions_list") {
		t.Fatal("execution lookup is not offered to the assistant")
	}
	api := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	h := &assistantHandlers{deps: &deps{repo: st}, api: api}
	lookup := func(projectID string) []domain.ScheduleJobExecution {
		t.Helper()
		text, err := h.callAssistantPlatform(context.Background(), user, projectID, assistantMCPCall{Server: "agentflow", Tool: "schedule_job_executions_list", Arguments: map[string]any{"query": map[string]any{"traceId": trace}}})
		if err != nil {
			t.Fatal(err)
		}
		var got []domain.ScheduleJobExecution
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatalf("decode %s: %v", text, err)
		}
		return got
	}
	if got := lookup("proj-trace"); len(got) != 1 || got[0].Error != "CodeBuddy key expired" || got[0].TraceID != trace {
		t.Fatalf("lookup in owning project = %+v", got)
	}
	if got := lookup("proj-other"); len(got) != 0 {
		t.Fatalf("lookup leaked across projects: %+v", got)
	}
}

// Execution history exposes prompts' outcomes and errors; it follows the
// same read access as the schedule job itself.
func TestScheduleJobExecutionsFollowJobAccess(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "schedule-exec-test-secret")
	h, _ := workbenchFixture(t)
	st := h.repo
	for _, login := range []string{"exec-owner", "exec-viewer", "exec-outsider"} {
		if err := st.PutUser(domain.User{LoginName: login, CodeBuddyAPIKey: "key-" + login}); err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := st.UserByLoginName("exec-owner")
	viewer, _ := st.UserByLoginName("exec-viewer")
	outsider, _ := st.UserByLoginName("exec-outsider")
	if err := st.PutProject(domain.Project{ID: "exec-proj", Name: "Exec", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: viewer.ID, Role: "viewer"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutScheduleJob(domain.ScheduleJob{ID: "exec-job", ProjectID: "exec-proj", Name: "巡检", SkipDispatch: true, AssistantPrompt: "p", IntervalSec: 3600, OwnerUserID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Minute)
	for _, e := range []domain.ScheduleJobExecution{
		{JobID: "exec-job", ProjectID: "exec-proj", TraceID: "t-ok", Status: "success", StartedAt: started},
		{JobID: "exec-job", ProjectID: "exec-proj", TraceID: "t-bad", Status: "failed", Error: "boom", StartedAt: started.Add(time.Second)},
	} {
		if err := st.PutScheduleJobExecution(e); err != nil {
			t.Fatal(err)
		}
	}
	server := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")

	all := requestJSONAuthed[[]domain.ScheduleJobExecution](t, server, viewer, http.MethodGet, "/api/schedule-job-executions?projectId=exec-proj", nil, http.StatusOK)
	if len(all) != 2 || all[0].TraceID != "t-bad" {
		t.Fatalf("viewer executions = %+v", all)
	}
	failed := requestJSONAuthed[[]domain.ScheduleJobExecution](t, server, owner, http.MethodGet, "/api/schedule-job-executions?jobId=exec-job&status=failed", nil, http.StatusOK)
	if len(failed) != 1 || failed[0].Error != "boom" {
		t.Fatalf("failed executions = %+v", failed)
	}
	if hidden := requestJSONAuthed[[]domain.ScheduleJobExecution](t, server, outsider, http.MethodGet, "/api/schedule-job-executions?traceId=t-bad", nil, http.StatusOK); len(hidden) != 0 {
		t.Fatalf("outsider read executions: %+v", hidden)
	}
	if code := scheduleAuthzStatus(t, server, outsider, http.MethodGet, "/api/schedule-job-executions?jobId=exec-job", nil); code != http.StatusForbidden {
		t.Fatalf("outsider job filter status = %d", code)
	}

	// The dashboard filters, counts and pages on the server.
	dash := requestJSONAuthed[scheduleExecutionDashboard](t, server, viewer, http.MethodGet, "/api/schedule-job-executions/dashboard?projectId=exec-proj&limit=1", nil, http.StatusOK)
	if dash.Total != 2 || len(dash.Items) != 1 || dash.Items[0].TraceID != "t-bad" || dash.Records != 2 || dash.RecentFailed != 1 {
		t.Fatalf("viewer dashboard = %+v", dash)
	}
	if c := dash.Overview.Current; c.Total != 2 || c.Success != 1 || c.Failed != 1 || dash.Overview.Since.IsZero() {
		t.Fatalf("viewer overview = %+v", dash.Overview)
	}
	next := requestJSONAuthed[scheduleExecutionDashboard](t, server, viewer, http.MethodGet, "/api/schedule-job-executions/dashboard?projectId=exec-proj&limit=1&offset=1", nil, http.StatusOK)
	if len(next.Items) != 1 || next.Items[0].TraceID != "t-ok" {
		t.Fatalf("second page = %+v", next.Items)
	}
	searched := requestJSONAuthed[scheduleExecutionDashboard](t, server, owner, http.MethodGet, "/api/schedule-job-executions/dashboard?projectId=exec-proj&jobId=exec-job&q=BOOM", nil, http.StatusOK)
	if searched.Total != 1 || searched.Items[0].TraceID != "t-bad" || searched.Records != 2 {
		t.Fatalf("searched dashboard = %+v", searched)
	}
	later := requestJSONAuthed[scheduleExecutionDashboard](t, server, owner, http.MethodGet, "/api/schedule-job-executions/dashboard?projectId=exec-proj&since="+started.Add(time.Hour).Format(time.RFC3339), nil, http.StatusOK)
	if later.Total != 0 || len(later.Items) != 0 || later.Overview.Current.Total != 2 {
		t.Fatalf("since filters the list, not the overview: %+v", later)
	}
	if hidden := requestJSONAuthed[scheduleExecutionDashboard](t, server, outsider, http.MethodGet, "/api/schedule-job-executions/dashboard?projectId=exec-proj", nil, http.StatusOK); hidden.Total != 0 || hidden.Records != 0 || hidden.Overview.Current.Total != 0 {
		t.Fatalf("outsider read the dashboard: %+v", hidden)
	}
	for path, want := range map[string]int{
		"/api/schedule-job-executions/dashboard?projectId=exec-proj&jobId=exec-job":  http.StatusForbidden,
		"/api/schedule-job-executions/dashboard":                                     http.StatusBadRequest,
		"/api/schedule-job-executions/dashboard?projectId=exec-proj&since=yesterday": http.StatusBadRequest,
	} {
		if code := scheduleAuthzStatus(t, server, outsider, http.MethodGet, path, nil); code != want {
			t.Errorf("%s = %d, want %d", path, code, want)
		}
	}
}

// Executions outlive the conversations a job keeps; the list marks an
// execution whose conversation is gone so the page shows no dead link.
func TestScheduleJobExecutionsMarkCleanedUpConversations(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "schedule-exec-test-secret")
	h, _ := workbenchFixture(t)
	st := h.repo
	if err := st.PutUser(domain.User{LoginName: "gone-owner", CodeBuddyAPIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := st.UserByLoginName("gone-owner")
	if err := st.PutProject(domain.Project{ID: "gone-proj", Name: "Gone", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutScheduleJob(domain.ScheduleJob{ID: "gone-job", ProjectID: "gone-proj", Name: "巡检", SkipDispatch: true, AssistantPrompt: "p", IntervalSec: 3600, OwnerUserID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAssistantSession(owner.ID, domain.AssistantSession{ID: "chat-kept", ProjectID: "gone-proj", Messages: []domain.AssistantMessage{{Role: "user", Content: "p"}}}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Hour)
	for i, e := range []domain.ScheduleJobExecution{
		{TraceID: "t-kept", Status: "success", SessionID: "chat-kept"},
		{TraceID: "t-gone", Status: "success", SessionID: "chat-cleaned"},
		{TraceID: "t-none", Status: "failed", Error: "no key"},
	} {
		e.JobID, e.ProjectID, e.StartedAt = "gone-job", "gone-proj", started.Add(time.Duration(i)*time.Second)
		if err := st.PutScheduleJobExecution(e); err != nil {
			t.Fatal(err)
		}
	}
	server := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	// Started after the server, so startup recovery leaves it running.
	if err := st.PutScheduleJobExecution(domain.ScheduleJobExecution{JobID: "gone-job", ProjectID: "gone-proj", TraceID: "t-running", Status: "running", SessionID: "chat-starting", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	got := requestJSONAuthed[[]scheduleJobExecutionView](t, server, owner, http.MethodGet, "/api/schedule-job-executions?jobId=gone-job", nil, http.StatusOK)
	want := map[string]bool{"t-kept": false, "t-gone": true, "t-running": false, "t-none": false}
	if len(got) != len(want) {
		t.Fatalf("executions = %+v", got)
	}
	for _, e := range got {
		if e.SessionGone != want[e.TraceID] {
			t.Errorf("%s sessionGone = %v, want %v", e.TraceID, e.SessionGone, want[e.TraceID])
		}
	}
}
