package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestNormalizeDeploymentRecordsAndSummary(t *testing.T) {
	raw := "结果如下：\n```json\n" + `{
  "data": {"results": [
    {"id": 11, "run_num": 8, "status": 2, "cur_branch": "feature/board", "cur_revision": "abc123", "start_time": "2026-09-20T02:00:00Z", "finish_time": "2026-09-20T02:02:00Z"},
    {"id": 10, "run_num": 7, "status_label": "失败", "cur_branch": "feature/board", "duration": 240, "created": "2026-09-19T02:00:00Z"},
    {"id": 9, "status": 2, "cur_branch": "another", "duration": 1}
  ]}
}` + "\n```"
	records, err := normalizeDeploymentRecords(raw, "feature/board", domain.Repository{}, 5410, 3021248)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records=%+v", records)
	}
	if records[0].DurationMs != 120000 || records[1].DurationMs != 240000 {
		t.Fatalf("durations=%d,%d", records[0].DurationMs, records[1].DurationMs)
	}
	summary := summarizeDeploymentMetricsAt(records, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	if summary.Total != 2 || summary.Succeeded != 1 || summary.Failed != 1 || summary.SuccessRate != 50 {
		t.Fatalf("summary=%+v", summary)
	}
	if summary.AverageDurationMs != 180000 || summary.P50DurationMs != 120000 || summary.P90DurationMs != 240000 {
		t.Fatalf("duration summary=%+v", summary)
	}
	if summary.Deployments7d != 2 || summary.Average7dDurationMs != 180000 {
		t.Fatalf("7-day summary=%+v", summary)
	}
	allRecords, err := normalizeDeploymentRecords(raw, "", domain.Repository{}, 5410, 3021248)
	if err != nil {
		t.Fatal(err)
	}
	if len(allRecords) != 3 || allRecords[2].Branch != "another" {
		t.Fatalf("unfiltered records=%+v", allRecords)
	}
}

func TestNormalizeDeploymentReadableRecords(t *testing.T) {
	raw := `接口名称: 获取流水线最近30天构建结果列表
共 2 条记录（当前页）：
1. 构建结果ID: 100000440 | 流水线ID: 10000001 | 运行序列号: 291 | 构建状态: 2 | 触发方式: TRIGGER_MANUAL | 启动人: example-admin | 触发分支: feature/board | Commit ID: 1f7666fbf02b65f4cf774b2556b4565861c9b37a | 短Commit ID: 1f7666fb | MR源分支:  | MR目标分支:  | 构建备注: {"start_comment":""} | 构建开始时间: 2026-09-19 03:24:37 | 构建结束时间: 2026-09-19 03:52:59 | 构建耗时: 1702 | 排队时间: 0
2. 构建结果ID: 100000441 | 流水线ID: 10000001 | 运行序列号: 292 | 构建状态: 1 | 触发方式: TRIGGER_CI | 启动人: example-dev | 触发分支: feature/running | Commit ID: abc123 | 构建备注: deploying | 构建开始时间: 2026-09-20 08:00:00 | 构建结束时间:  | 构建耗时: 0 | 排队时间: 0
构建总数: 2`
	records, err := normalizeDeploymentRecords(raw, "", domain.Repository{}, skillHubPipelineProjectID, skillHubDeploymentPipelineID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records=%+v", records)
	}
	if records[0].Status != 1 || records[0].Branch != "feature/running" || records[1].DurationMs != 1702000 {
		t.Fatalf("parsed records=%+v", records)
	}
	summary := summarizeDeploymentMetricsAt(records, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	if summary.Running != 1 || summary.Deployments7d != 1 || summary.Average7dDurationMs != 1702000 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestDeploymentMetricsAutoLoadsAllBranches(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "deployment-metrics-test-secret")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "deploy", Name: "Deploy"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRepository(domain.Repository{ID: "repo-deploy", ProjectID: "deploy", Name: "skillhub", GongfengProjectID: skillHubGongfengProjectID, DefaultBranch: "master", PipelineProjectID: 5410, PipelineDefID: 3021248}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUser(domain.User{LoginName: "deployment-user", TaihuPAT: "taihu-secret"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("deployment-user")
	var called bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &request)
		w.Header().Set("Content-Type", "application/json")
		if request.Method != "tools/call" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			return
		}
		called = true
		if request.Params.Name != "coding_mcp" || request.Params.Arguments["project_id"] != "10001" || request.Params.Arguments["username"] != "deployment-user" {
			t.Errorf("unexpected call: %+v", request.Params)
		}
		path, _ := request.Params.Arguments["path"].(string)
		parsed, parseErr := url.Parse(path)
		if parseErr != nil || parsed.Query().Has("cur_branch") || !strings.Contains(parsed.Path, "/pipeline/10000001/totalresult") {
			t.Errorf("unexpected path: %q (%v)", path, parseErr)
		}
		payload := `{"results":[{"id":77,"run_num":9,"status":2,"cur_branch":"feature/board","start_time":"2026-09-20T02:00:00Z","finish_time":"2026-09-20T02:03:00Z"}]}`
		encoded, _ := json.Marshal(payload)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":` + string(encoded) + `}]}}`))
	}))
	t.Cleanup(gateway.Close)
	if err := st.PutMCPServer(domain.MCPServerDefinition{ID: "qci", Name: "智研QCI流水线", MarketID: "7897", ProjectID: "deploy", Scope: "project", Transport: "http", URL: gateway.URL}); err != nil {
		t.Fatal(err)
	}

	handler := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	got := requestJSONAuthed[deploymentMetricsResponse](t, handler, user, http.MethodGet, "/api/deployment-metrics?projectId=deploy&repositoryId=repo-deploy", nil, http.StatusOK)
	if !called || got.Branch != "" || len(got.Records) != 1 || got.Summary.AverageDurationMs != 180000 {
		t.Fatalf("response=%+v called=%v", got, called)
	}
}

func TestDeploymentMetricsRejectsNonSkillHubRepository(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutProject(domain.Project{ID: "deploy-empty", Name: "Deploy"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRepository(domain.Repository{ID: "repo-empty", ProjectID: "deploy-empty", Name: "service", GongfengProjectID: "team/service"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutUser(domain.User{LoginName: "deployment-empty-user"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("deployment-empty-user")
	handler := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	requestJSONAuthed[map[string]string](t, handler, user, http.MethodGet, "/api/deployment-metrics?projectId=deploy-empty&repositoryId=repo-empty&branch=master", nil, http.StatusUnprocessableEntity)
}
