package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

func TestDeploymentMetricsRemainingValidation(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutProject(domain.Project{ID: "cvb-deploy", Name: "Deploy"}); err != nil {
		t.Fatal(err)
	}
	repositories := []domain.Repository{
		{ID: "cvb-skillhub", ProjectID: "cvb-deploy", Name: "skillhub", GongfengProjectID: skillHubGongfengProjectID},
		{ID: "cvb-other", ProjectID: "platform", Name: "skillhub", GongfengProjectID: skillHubGongfengProjectID},
		{ID: "cvb-name-only", ProjectID: "cvb-deploy", Name: "SkillHub", WebURL: "https://git.example/example/team-project"},
	}
	for _, repository := range repositories {
		if err := f.s.PutRepository(repository); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		path string
		auth bool
		want int
	}{
		{"/api/deployment-metrics?projectId=cvb-deploy&repositoryId=cvb-skillhub", false, http.StatusUnauthorized},
		{"/api/deployment-metrics", true, http.StatusBadRequest},
		{"/api/deployment-metrics?projectId=cvb-deploy&repositoryId=cvb-skillhub&branch=" + strings.Repeat("x", 256), true, http.StatusBadRequest},
		{"/api/deployment-metrics?projectId=absent&repositoryId=cvb-skillhub", true, http.StatusNotFound},
		{"/api/deployment-metrics?projectId=cvb-deploy&repositoryId=absent", true, http.StatusNotFound},
		{"/api/deployment-metrics?projectId=cvb-deploy&repositoryId=cvb-other", true, http.StatusNotFound},
	}
	for _, tc := range cases {
		rec := cvBRequest(t, f, http.MethodGet, tc.path, nil, tc.auth)
		if rec.Code != tc.want {
			t.Errorf("%s = %d, want %d: %s", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}

	if positiveQueryInt(cvBGinContext(t, "/?n=bad"), "n", 7) != 0 ||
		positiveQueryInt(cvBGinContext(t, "/?n=-1"), "n", 7) != 0 ||
		positiveQueryInt(cvBGinContext(t, "/?n=9"), "n", 7) != 9 {
		t.Fatal("positiveQueryInt edge handling")
	}
	for _, repository := range repositories {
		if got := isSkillHubRepository(repository); !got {
			t.Errorf("repository should be recognized: %+v", repository)
		}
	}
	if isSkillHubRepository(domain.Repository{Name: "SkillHub", WebURL: "https://example.test/other"}) {
		t.Fatal("unrelated SkillHub name accepted")
	}
	if got := deploymentPipelineURL(domain.Repository{PipelineURLPattern: "https://ci/{build_id}"}, 1, 2); got != "https://ci/current" {
		t.Fatalf("pipeline URL = %q", got)
	}
}

func cvBGinContext(t *testing.T, target string) *gin.Context {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return c
}

func cvBRawRequest(t *testing.T, f cvBHTTPFixture, method, path, body string, authed bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authed {
		req = authedRequest(t, req, f.user)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func TestDeploymentMetricsParsingLastBranches(t *testing.T) {
	if _, err := decodeMCPJSON("```"); err == nil {
		t.Fatal("empty fenced response accepted")
	}
	if rows := deploymentRows(`{"items":[{"id":"x"}]}`); len(rows) != 1 {
		t.Fatalf("string rows = %#v", rows)
	}
	if rows := deploymentRows([]any{"skip", map[string]any{"id": "x"}}); len(rows) != 1 {
		t.Fatalf("mixed rows = %#v", rows)
	}
	if deploymentRows(map[string]any{"none": true}) != nil {
		t.Fatal("unexpected rows")
	}
	if intField(map[string]any{"n": 4}, "n") != 4 || intField(map[string]any{"n": "5"}, "n") != 5 {
		t.Fatal("integer conversion")
	}
	if timeField(map[string]any{"t": "2026-10-30 12:13:14.123456"}, "t").IsZero() {
		t.Fatal("fractional local time was not parsed")
	}
	start := time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC)
	if got := durationField(map[string]any{"elapsed": "bad"}, time.Time{}, time.Time{}); got != 0 {
		t.Fatalf("bad duration = %d", got)
	}
	if got := durationField(map[string]any{}, start, start.Add(-time.Second)); got != 0 {
		t.Fatalf("backward duration = %d", got)
	}
	if got := summarizeDeploymentMetrics(nil); got.Total != 0 {
		t.Fatalf("empty summary = %+v", got)
	}
	if records, err := normalizeDeploymentRecords(`[{"status":2,"duration":1}]`, "", domain.Repository{}, 1, 2); err != nil || len(records) != 1 || records[0].URL == "" {
		t.Fatalf("id-less record = %+v, %v", records, err)
	}
	if _, err := normalizeDeploymentRecords(`{}`, "", domain.Repository{}, 1, 2); err == nil {
		t.Fatal("object without record list accepted")
	}
	_ = deploymentReadableRows("x. ignored\n2. no separator\n3. Unknown: value")
}

func TestDeploymentMetricsGatewayBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutProject(domain.Project{ID: "cvb-gateway", Name: "Gateway"}); err != nil {
		t.Fatal(err)
	}
	repository := domain.Repository{ID: "cvb-gateway-repo", ProjectID: "cvb-gateway", Name: "skillhub", GongfengProjectID: skillHubGongfengProjectID}
	if err := f.s.PutRepository(repository); err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "rpc-error"):
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"failed"}}`))
		case strings.Contains(string(body), "malformed"):
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"not json"}]}}`))
		default:
			payload := `{"results":[{"id":"old","status":2,"cur_branch":"main","created":"2026-10-29T00:00:00Z","duration":1},{"id":"new","status":2,"cur_branch":"main","created":"2026-10-30T00:00:00Z","duration":2}]}`
			encoded, _ := json.Marshal(payload)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":` + string(encoded) + `}]}}`))
		}
	}))
	t.Cleanup(gateway.Close)
	if err := f.s.PutMCPServer(domain.MCPServerDefinition{
		ID: "cvb-qci", Name: "QCI", MarketID: "7897", ProjectID: "cvb-gateway",
		Scope: "project", Transport: "http", URL: gateway.URL,
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/deployment-metrics?projectId=cvb-gateway&repositoryId=cvb-gateway-repo&branch=rpc-error&pageSize=0", http.StatusBadGateway},
		{"/api/deployment-metrics?projectId=cvb-gateway&repositoryId=cvb-gateway-repo&branch=malformed", http.StatusBadGateway},
		{"/api/deployment-metrics?projectId=cvb-gateway&repositoryId=cvb-gateway-repo&branch=main&pageSize=999", http.StatusOK},
	} {
		rec := cvBRequest(t, f, http.MethodGet, tc.path, nil, true)
		if rec.Code != tc.want {
			t.Errorf("%s = %d, want %d: %s", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	handler := deploymentMetricsHandlers{deps: &deps{repo: f.s}}
	_, listed := marketItem("zhiyan-pipeline")
	if server, ok := handler.lookupQCIProjectMCP("unconfigured-project", f.user.ID); ok != listed || ok && server.MarketID == "" {
		t.Fatalf("market fallback must follow the market listing (%v): %+v %v", listed, server, ok)
	}
}

func TestRunsRemainingPureAndHTTPBranches(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, tc := range []struct {
		method string
		path   string
		body   any
		auth   bool
		want   int
	}{
		{http.MethodGet, "/api/analytics?from=2026-10-01T00:00:00Z&to=2026-10-30T00:00:00Z&origin=external&scope=x&userId=1&issueSource=tapd&grain=day&tz=UTC", nil, false, http.StatusOK},
		{http.MethodGet, "/api/runs?responsible=me", nil, false, http.StatusUnauthorized},
		{http.MethodPost, "/api/runs", map[string]any{}, false, http.StatusConflict},
		{http.MethodPost, "/api/runs", map[string]any{"projectId": "missing"}, true, http.StatusBadRequest},
		{http.MethodPatch, "/api/runs", nil, false, http.StatusMethodNotAllowed},
	} {
		rec := cvBRequest(t, f, tc.method, tc.path, tc.body, tc.auth)
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
	if rec := cvBRawRequest(t, f, http.MethodPost, "/api/runs", "{", true); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed run = %d: %s", rec.Code, rec.Body.String())
	}
	runsHandler := &runsHandlers{deps: &deps{repo: f.s, engine: f.e}}
	router := gin.New()
	router.GET("/cvb-default/:id", runsHandler.run)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cvb-default/anything", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("run default = %d", rec.Code)
	}
	wf := domain.WorkflowDefinition{
		Description: strings.Repeat("d", 300),
		Nodes:       []domain.WorkflowNode{{ID: "outer", Script: "secret", Command: "secret"}},
		SOPCheckpoint: &domain.SOPCheckpoint{
			Workflow: domain.WorkflowDefinition{Nodes: []domain.WorkflowNode{{ID: "inner", Script: "secret", Command: "secret"}}},
			Nodes:    map[string]*domain.NodeRun{"inner": {NodeID: "inner"}},
		},
		GroupHistory: []domain.GroupTaskHistory{{
			Node:  domain.WorkflowNode{ID: "old", Script: "secret", Command: "secret"},
			State: domain.NodeRun{Output: strings.Repeat("o", 3000)},
		}},
	}
	slim := slimWorkflowSnapshot(wf)
	if slim.Nodes[0].Script != "" || slim.SOPCheckpoint.Nodes != nil || slim.SOPCheckpoint.Workflow.Nodes[0].Command != "" ||
		slim.GroupHistory[0].Node.Script != "" || len(slim.GroupHistory[0].State.Output) >= 3000 || len(slim.Description) >= 300 {
		t.Fatalf("workflow not slimmed: %+v", slim)
	}
}
