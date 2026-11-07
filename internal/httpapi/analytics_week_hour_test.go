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

func TestAnalyticsWeekHourHonorsTimeZoneParam(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	at := time.Date(2026, 9, 10, 20, 30, 0, 0, time.UTC)
	wf := domain.WorkflowDefinition{ID: "wf", Name: "WF"}
	if err := s.PutRun(domain.Run{
		ID: "wh-http", WorkflowID: wf.ID, WorkflowSnapshot: wf, Status: "succeeded",
		ExecutionContext: domain.TelemetryContext{ProjectID: "default"},
		Nodes:            map[string]*domain.NodeRun{"n": {NodeID: "n", Status: "succeeded", FinishedAt: at}},
		Usage:            domain.TokenUsage{TotalTokens: 12}, CreatedAt: at.Add(-time.Minute), UpdatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	fetch := func(query string) map[string]json.RawMessage {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/analytics?projectId=default&scope=overview"+query, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/analytics%s = %d %s", query, rec.Code, rec.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	cell := func(body map[string]json.RawMessage) domain.AnalyticsWeekHourCell {
		var cells []domain.AnalyticsWeekHourCell
		if err := json.Unmarshal(body["weekHour"], &cells); err != nil || len(cells) != 1 {
			t.Fatalf("weekHour = %s (%v)", body["weekHour"], err)
		}
		return cells[0]
	}
	def := fetch("")
	if c := cell(def); c.Weekday != 4 || c.Hour != 4 || c.Runs != 1 || c.Tokens != 12 {
		t.Fatalf("default zone cell = %#v", c)
	}
	if c := cell(fetch("&tz=UTC")); c.Weekday != 3 || c.Hour != 20 {
		t.Fatalf("tz=UTC cell = %#v", c)
	}
	for _, key := range []string{"filter", "summary", "trend", "workflows", "models", "nodes", "failures", "coverage", "delivery", "mrMerges", "demo"} {
		if _, ok := def[key]; !ok {
			t.Fatalf("existing analytics field %q missing from response", key)
		}
	}
}
