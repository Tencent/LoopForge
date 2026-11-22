package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestAnalyticsUnassignedProjectScopeKeepsPersonalConversations(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	at := time.Now().UTC()
	for _, item := range []struct {
		id, project string
		owner       int64
		tokens      int64
	}{
		{"scope-personal", "", 77, 10},
		{"scope-project", "p1", 77, 20},
		{"scope-other-user", "", 88, 50},
	} {
		session := domain.AssistantSession{ID: item.id, ProjectID: item.project, CreatedAt: at,
			Messages: []domain.AssistantMessage{
				{Role: "user", AuthorID: item.owner, Content: "request", CreatedAt: at},
				{Role: "assistant", Content: "reply", CreatedAt: at, Usage: &domain.TokenUsage{TotalTokens: item.tokens, InputTokens: item.tokens}},
			}}
		if err := s.PutAssistantSession(item.owner, session); err != nil {
			t.Fatal(err)
		}
	}
	for _, run := range []domain.Run{
		{ID: "scope-personal-run", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 77}},
		{ID: "scope-project-run", ExecutionContext: domain.TelemetryContext{ProjectID: "p1", ResponsibleUserID: 77}},
		{ID: "scope-other-run", ExecutionContext: domain.TelemetryContext{ResponsibleUserID: 88}},
	} {
		run.Status, run.CreatedAt = "succeeded", at
		if err := s.PutRun(run); err != nil {
			t.Fatal(err)
		}
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "ws"))), tracker.NewService(), "", "")
	for _, tt := range []struct {
		query    string
		sessions int
		tokens   int64
	}{
		{"", 2, 30},
		{"&projectScope=unassigned", 1, 10},
		{"&projectId=p1", 1, 20},
	} {
		t.Run(tt.query, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/analytics?scope=overview&userId=77"+tt.query, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var snapshot domain.AnalyticsSnapshot
			if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Activity.Sessions != tt.sessions || snapshot.Activity.SessionUsage.Tokens != tt.tokens {
				t.Fatalf("activity=%+v, want %d sessions and %d tokens", snapshot.Activity, tt.sessions, tt.tokens)
			}
			if snapshot.Summary.TotalRuns != tt.sessions {
				t.Fatalf("totalRuns=%d, want %d", snapshot.Summary.TotalRuns, tt.sessions)
			}
		})
	}
	for _, query := range []string{"projectScope=unknown", "projectScope=unassigned&projectId=p1"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/analytics?"+query, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d, want 400", query, rec.Code)
		}
	}
}
