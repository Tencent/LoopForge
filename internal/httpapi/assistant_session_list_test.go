package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func sessionListFixture(t testing.TB) (*assistantHandlers, domain.User, domain.User, string) {
	t.Helper()
	var store *repo.Store
	var err error
	if dsn := os.Getenv("AGENTFLOW_SESSION_LIST_TEST_MYSQL_DSN"); dsn != "" {
		if !strings.Contains(dsn, "@tcp(127.0.0.1:") || !strings.Contains(dsn, "/agentflow_session_test") {
			t.Fatal("session list integration tests require a disposable local Docker database")
		}
		store, err = repo.Open(dsn)
	} else {
		store, err = repo.OpenTest()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	owner := domain.User{ID: 101, LoginName: "session-list-owner"}
	viewer := domain.User{ID: 102, LoginName: "session-list-viewer"}
	return &assistantHandlers{deps: &deps{repo: store}}, owner, viewer, fmt.Sprintf("session-list-%x-", time.Now().UnixNano())
}

func putSessionListProject(t testing.TB, h *assistantHandlers, project domain.Project) {
	t.Helper()
	if err := h.repo.PutProject(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.repo.DB.Exec("DELETE FROM project_members WHERE project_id=?", project.ID)
		h.repo.DB.Exec("DELETE FROM projects WHERE id=?", project.ID)
	})
}

func putSessionListConversation(t testing.TB, h *assistantHandlers, ownerID int64, session domain.AssistantSession) {
	t.Helper()
	if err := h.repo.PutAssistantSession(ownerID, session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.repo.DB.Exec("DELETE FROM assistant_generations WHERE session_id=?", session.ID)
		h.repo.DB.Exec("DELETE FROM assistant_sessions WHERE id=?", session.ID)
	})
}

func putSessionListRun(t testing.TB, h *assistantHandlers, run domain.Run) {
	t.Helper()
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.repo.DeleteRun(run.ID) })
}

func TestAssistantSessionListPreservesVisibilityAndMetadata(t *testing.T) {
	t.Setenv("AGENTFLOW_PLATFORM_ADMINS", "example-maintainer")
	h, owner, viewer, prefix := sessionListFixture(t)
	projects := []domain.Project{
		{ID: prefix + "owned", OwnerID: viewer.ID},
		{ID: prefix + "member", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: viewer.ID, Role: "member"}}},
		{ID: prefix + "private", OwnerID: owner.ID},
		{ID: prefix + "open"},
		{ID: prefix + "members-only", Members: []domain.ProjectMember{{UserID: owner.ID, Role: "member"}}},
	}
	ids := map[string]string{}
	for _, project := range projects {
		putSessionListProject(t, h, project)
		runID := project.ID + "-run"
		putSessionListRun(t, h, domain.Run{ID: runID, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: project.ID}})
		id := repo.RunAssistantSessionID(runID)
		ids[strings.TrimPrefix(project.ID, prefix)] = id
		putSessionListConversation(t, h, owner.ID, domain.AssistantSession{ID: id, RunID: runID, ProjectID: project.ID, Shared: true, Title: project.ID, Messages: []domain.AssistantMessage{{Role: "user", Content: "private transcript"}}})
	}
	memberRun := prefix + "member-run"
	branch := domain.AssistantSession{ID: "sop-branch-" + prefix, RunID: memberRun, ProjectID: prefix + "member", Shared: true, ParentSessionID: ids["member"], RootSessionID: ids["member"], HistoryRevision: 3, ForkMessageIndex: 2}
	putSessionListConversation(t, h, owner.ID, branch)
	ids["branch"] = branch.ID
	putSessionListConversation(t, h, viewer.ID, domain.AssistantSession{ID: prefix + "personal", Archived: true, Title: "archived personal chat"})
	putSessionListConversation(t, h, viewer.ID, domain.AssistantSession{ID: prefix + "legacy", RunID: memberRun})
	putSessionListConversation(t, h, owner.ID, domain.AssistantSession{ID: prefix + "foreign", Title: "another user's private chat"})
	putSessionListConversation(t, h, owner.ID+10000, domain.AssistantSession{ID: "sop-forged-" + prefix, RunID: memberRun, ProjectID: prefix + "member", Shared: true})
	for _, missing := range []string{"missing-run", "missing-project", "mismatch", "unbound"} {
		runID, projectID := prefix+missing, prefix+"open"
		if missing == "missing-project" {
			projectID = prefix + "absent"
		}
		if missing == "unbound" {
			projectID = ""
		}
		if missing != "missing-run" {
			runProject := projectID
			if missing == "mismatch" {
				runProject = prefix + "private"
			}
			putSessionListRun(t, h, domain.Run{ID: runID, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: runProject}})
		}
		putSessionListConversation(t, h, owner.ID, domain.AssistantSession{ID: repo.RunAssistantSessionID(runID), RunID: runID, ProjectID: projectID, Shared: true})
	}
	job := domain.AssistantGeneration{ID: prefix + "generation", SessionID: ids["member"], UserID: owner.ID, ActorID: viewer.ID, Status: "running"}
	if err := h.repo.BeginAssistantGeneration(job); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		user domain.User
		keys []string
	}{
		{"member", viewer, []string{"owned", "member", "open", "branch", "personal"}},
		{"owner", owner, []string{"member", "private", "open", "members-only", "branch", "foreign"}},
		{"outsider", domain.User{ID: 103, LoginName: "session-list-outsider"}, []string{"open"}},
		{"admin", domain.User{ID: 104, LoginName: "example-maintainer"}, []string{"owned", "member", "private", "open", "members-only", "branch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := workbenchContext(tc.user, "GET", "/api/assistant/sessions", bytes.NewBuffer(nil))
			h.listSessions(c)
			if rec.Code != 200 {
				t.Fatalf("list status %d: %s", rec.Code, rec.Body)
			}
			var list []domain.AssistantSession
			if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			want := map[string]bool{}
			for _, key := range tc.keys {
				id := ids[key]
				if id == "" {
					id = prefix + key
				}
				want[id] = true
			}
			seen := map[string]bool{}
			for i, item := range list {
				if !strings.HasPrefix(item.ID, prefix) && !strings.HasPrefix(item.ID, "sop-branch-"+prefix) && !want[item.ID] {
					// Ignore unrelated rows in a shared disposable MySQL test DB.
					if item.RunID == "" || !strings.HasPrefix(item.RunID, prefix) {
						continue
					}
				}
				if !want[item.ID] || seen[item.ID] || len(item.Messages) != 0 {
					t.Fatalf("unexpected, duplicate or heavy session: %s", item.ID)
				}
				seen[item.ID] = true
				if i > 0 && list[i-1].UpdatedAt.Before(item.UpdatedAt) {
					t.Fatal("session sort order changed")
				}
				if item.ID == prefix+"personal" && !item.Archived {
					t.Fatal("archive metadata lost")
				}
				if item.ID == branch.ID && (item.RootSessionID != branch.RootSessionID || item.ParentSessionID != branch.ParentSessionID || item.HistoryRevision != 3 || item.ForkMessageIndex != 2) {
					t.Fatal("shared branch metadata lost")
				}
				if item.ID == job.SessionID && item.GenerationStatus != "running" {
					t.Fatal("generation status lost")
				}
			}
			if len(seen) != len(want) {
				t.Fatalf("missing sessions: got=%v want=%v", seen, want)
			}
		})
	}
}

type sessionListQueryLog struct {
	logger.Interface
	queries []string
}

func (l *sessionListQueryLog) Trace(_ context.Context, _ time.Time, query func() (string, int64), _ error) {
	sql, _ := query()
	l.queries = append(l.queries, sql)
}

func TestAssistantSessionListQueryCountIsBounded(t *testing.T) {
	h, owner, viewer, prefix := sessionListFixture(t)
	putSessionListProject(t, h, domain.Project{ID: prefix, OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: viewer.ID, Role: "member"}}})
	originalLogger := h.repo.DB.Logger
	counts := []int{}
	for i := 0; i < 27; i++ {
		runID := fmt.Sprintf("%s%d", prefix, i)
		putSessionListRun(t, h, domain.Run{ID: runID, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: prefix}, Nodes: map[string]*domain.NodeRun{"node": {NodeID: "node", Output: strings.Repeat("large output", 1000)}}, Artifacts: []domain.Artifact{{ID: runID + "artifact", RunID: runID, Content: strings.Repeat("large artifact", 1000)}}})
		putSessionListConversation(t, h, owner.ID, domain.AssistantSession{ID: repo.RunAssistantSessionID(runID), RunID: runID, ProjectID: prefix, Shared: true, Messages: []domain.AssistantMessage{{Role: "user", Content: strings.Repeat("large transcript", 1000)}}})
		if i != 0 && i != 26 {
			continue
		}
		recorder := &sessionListQueryLog{Interface: logger.Default}
		h.repo.DB.Logger = recorder
		c, rec := workbenchContext(viewer, "GET", "/api/assistant/sessions", bytes.NewBuffer(nil))
		start := time.Now()
		h.listSessions(c)
		h.repo.DB.Logger = originalLogger
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "large transcript") {
			t.Fatalf("list failed or transferred history: %d", rec.Code)
		}
		for _, sql := range recorder.queries {
			for _, table := range []string{"run_nodes", "run_events", "run_artifacts", "run_logs", "run_telemetry"} {
				if strings.Contains(sql, table) {
					t.Fatalf("session listing loaded %s", table)
				}
			}
		}
		counts = append(counts, len(recorder.queries))
		t.Logf("%d shared sessions: %d queries, %s", i+1, len(recorder.queries), time.Since(start))
	}
	// Run decoration adds three fixed reads (message counts, run headers and
	// requirement identities); the count must still not grow with the list.
	if counts[0] != counts[1] || counts[1] > 10 {
		t.Fatalf("session count increased database work: %v", counts)
	}
}

func TestAssistantSessionListMembershipFailureIsClosed(t *testing.T) {
	h, owner, viewer, prefix := sessionListFixture(t)
	putSessionListProject(t, h, domain.Project{ID: prefix, OwnerID: owner.ID})
	putSessionListRun(t, h, domain.Run{ID: prefix, Status: "succeeded", ExecutionContext: domain.TelemetryContext{ProjectID: prefix}})
	putSessionListConversation(t, h, owner.ID, domain.AssistantSession{ID: repo.RunAssistantSessionID(prefix), RunID: prefix, ProjectID: prefix, Shared: true})
	if err := h.repo.DB.Callback().Query().Before("gorm:query").Register(prefix, func(tx *gorm.DB) {
		if tx.Statement.Table == "project_members" {
			tx.AddError(errors.New("membership read failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.repo.DB.Callback().Query().Remove(prefix) })
	c, rec := workbenchContext(viewer, "GET", "/api/assistant/sessions", bytes.NewBuffer(nil))
	h.listSessions(c)
	if rec.Code != 500 {
		t.Fatalf("membership failure must not grant access: %d %s", rec.Code, rec.Body)
	}
}
