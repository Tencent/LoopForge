package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestInsightIngressPermissionsAndValidation(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "insights-test-secret")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	for _, login := range []string{"insight-owner", "insight-member", "insight-outsider"} {
		if err := s.PutUser(domain.User{LoginName: login}); err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := s.UserByLoginName("insight-owner")
	member, _ := s.UserByLoginName("insight-member")
	outsider, _ := s.UserByLoginName("insight-outsider")
	if err := s.PutProject(domain.Project{ID: "ingest-project", Name: "Ingest", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: member.ID, Role: "member"}}}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	input := map[string]any{"projectId": "ingest-project", "name": "CI", "userMappings": map[string]int64{"alice": member.ID}}
	for _, user := range []domain.User{member, outsider} {
		requestJSONAuthed[map[string]any](t, h, user, "POST", "/api/insights/v1/sources", input, 403)
	}
	type created struct {
		Source domain.InsightSource `json:"source"`
		Token  string               `json:"token"`
	}
	result := requestJSONAuthed[created](t, h, owner, "POST", "/api/insights/v1/sources", input, 201)
	if !strings.HasPrefix(result.Token, "afi_") || result.Source.ProjectID != "ingest-project" {
		t.Fatalf("create: %+v", result.Source)
	}
	requestJSONAuthed[map[string]any](t, h, outsider, "GET", "/api/insights/v1/sources?projectId=ingest-project", nil, 403)
	list := requestJSONAuthed[[]domain.InsightSource](t, h, owner, "GET", "/api/insights/v1/sources?projectId=ingest-project", nil, 200)
	raw, _ := json.Marshal(list)
	if bytes.Contains(raw, []byte(result.Token)) || bytes.Contains(raw, []byte("tokenHash")) {
		t.Fatal("credential leaked")
	}
	input["userMappings"] = map[string]int64{"other": outsider.ID}
	requestJSONAuthed[map[string]any](t, h, owner, "POST", "/api/insights/v1/sources", input, 400)
	now := time.Now().UTC().Add(-time.Minute)
	v := domain.InsightExecution{ID: "event", Type: "workflow_execution", ExternalExecutionID: "exec", ExternalUserID: "alice", Workflow: domain.InsightWorkflow{ID: "flow", Name: "CI flow"}, Status: "failed", StartedAt: now.Add(-time.Hour), FinishedAt: now, Usage: []domain.InsightUsage{{Provider: "p", Model: "m", InputTokens: 10, OutputTokens: 5, CostStatus: "unknown"}}}
	send := func(handler http.Handler, token, path string, body any, want int) string {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s: %d want %d: %s", path, rec.Code, want, rec.Body.String())
		}
		return rec.Body.String()
	}
	send(h, "", insightIngestPath, v, 401)
	send(h, "bad", insightIngestPath, v, 401)
	// Scoped credentials cannot be used as general API credentials.
	send(h, result.Token, "/api/insights/v1/sources", input, 401)
	send(h, result.Token, insightIngestPath, v, 201)
	if body := send(h, result.Token, insightIngestPath, v, 200); !strings.Contains(body, "duplicate") {
		t.Fatal(body)
	}

	// Exercise the shipped CLI against the real ingress, including safe retry.
	if python, err := exec.LookPath("python3"); err == nil {
		server := httptest.NewServer(h)
		t.Cleanup(server.Close)
		file := filepath.Join(t.TempDir(), "usage.json")
		payload, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, payload, 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(python, "../../scripts/report-workflow-usage.py", file, "--url", server.URL, "--attempts", "1")
		cmd.Env = append(os.Environ(), "AGENTFLOW_INSIGHT_TOKEN="+result.Token)
		output, err := cmd.CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("duplicate")) {
			t.Fatalf("reporter: %v %s", err, output)
		}
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("retry file removed: %v", err)
		}
	}
	changed := v
	changed.Status = "succeeded"
	send(h, result.Token, insightIngestPath, changed, 409)
	changed = v
	changed.ID = "new-event"
	send(h, result.Token, insightIngestPath, changed, 409)
	changed = v
	changed.ExternalUserID = "unmapped"
	send(h, result.Token, insightIngestPath, changed, 400)
	for _, body := range []map[string]any{{"projectId": "another"}, {"responsibleUserId": outsider.ID}, {"type": "model_usage"}} {
		send(h, result.Token, insightIngestPath, body, 400)
	}
	tooBig := map[string]any{"id": strings.Repeat("x", (1<<20)+1)}
	send(h, result.Token, insightIngestPath, tooBig, 413)
	// Dedicated Bearer ingress also works behind Basic Auth without weakening other endpoints.
	guarded := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "preview", "password")
	send(guarded, result.Token, insightIngestPath, v, 200)
	send(guarded, result.Token, "/api/insights/v1/sources", input, 401)
	got := requestJSONAuthed[domain.AnalyticsSnapshot](t, h, owner, "GET", "/api/analytics?projectId=ingest-project&origin=external", nil, 200)
	if got.Summary.TotalTokens != 15 || got.Summary.FailedRuns != 1 || len(got.UserUsage) != 1 || got.UserUsage[0].UserID == "unassigned" {
		t.Fatalf("analytics: %+v", got)
	}
	requestJSONAuthed[map[string]any](t, h, outsider, "DELETE", "/api/insights/v1/sources/"+result.Source.ID, nil, 403)
	requestJSONAuthed[map[string]any](t, h, owner, "DELETE", "/api/insights/v1/sources/"+result.Source.ID, nil, 200)
	send(h, result.Token, insightIngestPath, v, 401)
	expired := domain.InsightSource{ID: "expired", ProjectID: "ingest-project", ExpiresAt: now}
	if err := s.CreateInsightSource(expired, insightTokenHash("expired-token")); err != nil {
		t.Fatal(err)
	}
	send(h, "expired-token", insightIngestPath, v, 401)
}

func TestInsightUsageContract(t *testing.T) {
	now := time.Now().UTC()
	valid := func() domain.InsightExecution {
		return domain.InsightExecution{ID: "e", Type: "workflow_execution", ExternalExecutionID: "x", Workflow: domain.InsightWorkflow{ID: "w", Name: "workflow"}, Status: "succeeded", StartedAt: now.Add(-time.Minute), FinishedAt: now, Usage: []domain.InsightUsage{{Provider: "p", Model: "m", InputTokens: 10, OutputTokens: 5, CostStatus: "unknown"}}}
	}
	zero := 0.0
	negative := -1.0
	for _, tc := range []struct {
		name   string
		mutate func(*domain.InsightExecution)
		want   bool
	}{
		{"unknown", func(v *domain.InsightExecution) {}, true},
		{"zero is known", func(v *domain.InsightExecution) { v.Usage[0].CostStatus = "reported"; v.Usage[0].CostUSD = &zero }, true},
		{"unknown amount", func(v *domain.InsightExecution) { v.Usage[0].CostUSD = &zero }, false},
		{"known missing", func(v *domain.InsightExecution) { v.Usage[0].CostStatus = "reported" }, false},
		{"negative cost", func(v *domain.InsightExecution) { v.Usage[0].CostStatus = "estimated"; v.Usage[0].CostUSD = &negative }, false},
		{"negative tokens", func(v *domain.InsightExecution) { v.Usage[0].InputTokens = -1 }, false},
		{"overflow", func(v *domain.InsightExecution) { v.Usage[0].InputTokens = 1 << 62 }, false},
		{"cache subset", func(v *domain.InsightExecution) { v.Usage[0].CacheReadTokens = 11 }, false},
		{"reasoning subset", func(v *domain.InsightExecution) { v.Usage[0].ReasoningTokens = 6 }, false},
		{"duplicate model", func(v *domain.InsightExecution) { v.Usage = append(v.Usage, v.Usage[0]) }, false},
		{"future", func(v *domain.InsightExecution) { v.FinishedAt = now.Add(time.Hour) }, false},
		{"reversed", func(v *domain.InsightExecution) { v.FinishedAt = v.StartedAt.Add(-time.Second) }, false},
		{"active", func(v *domain.InsightExecution) { v.Status = "running" }, false},
		{"unsafe url", func(v *domain.InsightExecution) { v.URL = "javascript:alert(1)" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := valid()
			tc.mutate(&v)
			reason := validateInsightExecution(&v, now)
			if (reason == "") != tc.want {
				t.Fatalf("valid=%v reason=%s", tc.want, reason)
			}
		})
	}
}
