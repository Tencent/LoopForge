package tracker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

const testTAPDTaskURL = "https://tapd.example.com/tapd_fe/10000000/task/detail/1010000000100000444"

func TestParseTAPDTaskURL(t *testing.T) {
	want := TAPDStoryRef{WorkspaceID: "10000000", StoryID: "1010000000100000444", Kind: "task"}
	for _, input := range []string{
		testTAPDTaskURL,
		"【DP】修复付费队列\n" + testTAPDTaskURL,
		"[开发任务](" + testTAPDTaskURL + ")",
		"开发任务（" + testTAPDTaskURL + "）",
		"https://www.tapd.cn/10000000/tasks/view/1010000000100000444",
		"https://tapd.example.com/tapd_fe/10000000/iteration/card/1001?dialog_preview_id=task_1010000000100000444",
	} {
		got, err := ParseTAPDStoryURL(input)
		if err != nil || got != want {
			t.Fatalf("ParseTAPDStoryURL(%q) = %+v, %v; want %+v", input, got, err, want)
		}
	}
	_, err := ParseTAPDStoryURL(testTAPDTaskURL + "\n" + strings.Replace(testTAPDTaskURL, "/task/", "/story/", 1))
	if err == nil || !strings.Contains(err.Error(), "多个不同") {
		t.Fatalf("task and story with the same ID must be ambiguous, got %v", err)
	}
}

func TestParseTAPDTaskRecords(t *testing.T) {
	for _, payload := range []string{
		`{"limit":1,"offset":0,"count":1,"page":1,"data":[{"id":"1001","name":"开发任务","description":"任务正文","status":"open","priority":"1","priority_label":"High","story_id":"9001"}]}`,
		`{"data":[{"Task":{"id":"1001","name":"开发任务","description":"任务正文","status":"open","priority_label":"High","story_id":"9001"}}]}`,
		`{"tasks":[{"task":{"id":"1001","name":"开发任务","description":"任务正文","status":"open","priority_label":"High","story_id":"9001"}}]}`,
	} {
		records, err := parseTAPDRecords(payload, "tasks")
		if err != nil || len(records) != 1 || records[0].Name != "开发任务" || records[0].Description != "任务正文" || records[0].PriorityLabel != "High" || records[0].StoryID != "9001" {
			t.Fatalf("records=%+v err=%v", records, err)
		}
	}
}

func TestImportAndResyncTAPDTask(t *testing.T) {
	calls := 0
	gateway := mockTAPDTaskGateway(t, func(tool string, args map[string]any) string {
		calls++
		if tool != "tasks_get" || args["workspace_id"] != "10000000" || args["id"] != "1010000000100000444" || args["fields"] != tapdTaskFields || args["limit"] != float64(1) {
			t.Errorf("unexpected task read: %s %+v", tool, args)
		}
		status, title := "open", "TAPD 任务标题"
		if calls > 1 {
			status, title = "progressing", "更新的任务标题"
		}
		return fmt.Sprintf(`{"limit":1,"offset":0,"count":1,"page":1,"data":[{"id":"1010000000100000444","name":%q,"description":"任务正文","status":%q,"priority":"1","priority_label":"High","creator":"alice","owner":"someone_else","story_id":"9001"}]}`, title, status)
	})
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{
		projects: map[string]domain.Project{"dp": {ID: "dp"}},
		users:    []domain.User{{ID: 7, LoginName: "alice"}},
	}
	issue, err := svc.ImportTAPDStory(store, "dp", "复制的标题\n"+testTAPDTaskURL, 99)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Kind != "requirement" || issue.Title != "TAPD 任务标题" || issue.Description != "任务正文" || issue.Status != "todo" || issue.Priority != "high" || issue.AssigneeUserID != 7 {
		t.Fatalf("unexpected imported task: %+v", issue)
	}
	if issue.ExternalRef == nil || issue.ExternalRef.ExternalType != "task" || issue.ExternalRef.ExternalURL != testTAPDTaskURL || !issue.ExternalRef.Imported {
		t.Fatalf("task external identity lost: %+v", issue.ExternalRef)
	}
	issue.BaseBranch = "feature/continue-development"
	issue.RepositoryID = "repo-1"
	issue.RepositoryIDs = []string{"repo-1"}
	issue.Clarification = domain.ClarificationAssessment{Status: domain.ClarificationReady, Summary: "已澄清"}
	if err := store.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	updated, err := svc.ResyncTAPDIssue(store, issue)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != issue.ID || updated.Title != "更新的任务标题" || updated.Status != "todo" || updated.ExternalRef.ExternalState != "progressing" || updated.ExternalRef.ExternalType != "task" || updated.ExternalRef.ExternalURL != testTAPDTaskURL {
		t.Fatalf("unexpected refreshed task: %+v", updated)
	}
	if updated.BaseBranch != issue.BaseBranch || updated.RepositoryID != issue.RepositoryID || updated.Clarification.Summary != "已澄清" || !updated.ExternalRef.Imported {
		t.Fatalf("refresh must preserve local execution state: %+v", updated)
	}
	if calls != 2 {
		t.Fatalf("expected import and resync reads, got %d calls", calls)
	}
	if removed := reconcileTAPDIssues(store, map[string]bool{}); removed != 0 {
		t.Fatalf("iteration sync must retain manually imported tasks, removed %d", removed)
	}
}

func TestImportTAPDTaskRejectsDifferentReturnedID(t *testing.T) {
	gateway := mockTAPDTaskGateway(t, func(tool string, args map[string]any) string {
		return `{"data":[{"id":"9999","name":"另一个任务"}]}`
	})
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp"}}}
	_, err := svc.ImportTAPDStory(store, "dp", testTAPDTaskURL, 0)
	if err == nil || !strings.Contains(err.Error(), "未找到 TAPD 任务") || len(store.Issues()) != 0 {
		t.Fatalf("unexpected result: err=%v issues=%+v", err, store.Issues())
	}
}

func TestTAPDRecordIdentityIncludesWorkspaceAndType(t *testing.T) {
	svc := NewService()
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp"}}}
	const recordID = "1001"
	for _, item := range []struct{ workspace, kind string }{
		{"10000000", "requirement"},
		{"10000000", "bug"},
		{"10000000", "task"},
		{"10132092", "task"},
		{"10132092", "requirement"},
	} {
		if _, err := svc.upsertTAPDRecord(store, "dp", item.workspace, tapdStory{ID: recordID, Name: item.workspace + item.kind}, item.kind, 0, true); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(store.Issues()); got != 5 {
		t.Fatalf("different TAPD identities must coexist, got %d issues: %+v", got, store.Issues())
	}
	updated, err := svc.upsertTAPDTask(store, "dp", "10132092", tapdStory{ID: recordID, Name: "updated task"}, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Issues()) != 5 || !strings.Contains(updated.ExternalRef.ExternalURL, "/10132092/task/detail/") {
		t.Fatalf("task refresh matched a different identity: %+v", updated)
	}
	pruneDuplicateExternalIssues(store)
	if got := len(store.Issues()); got != 5 {
		t.Fatalf("deduplication merged different TAPD identities, got %d issues", got)
	}
}

func TestWriteBackTAPDTaskUsesTaskAndOriginalWorkspace(t *testing.T) {
	calls := 0
	gateway := mockTAPDTaskGateway(t, func(tool string, args map[string]any) string {
		calls++
		if tool != "comments_create" || args["entry_type"] != "task" || args["entry_id"] != "1010000000100000444" || args["workspace_id"] != "10000000" || args["description"] != "任务澄清结果" {
			t.Errorf("wrong task comment target: %s %+v", tool, args)
		}
		return `{"id":"local-mock-comment"}`
	})
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp", TAPDWorkspaceID: "99999"}}}
	issue := domain.Issue{ID: "tapd-task-1010000000100000444", ProjectID: "dp", Source: "tapd", Kind: "requirement", ExternalRef: &domain.ExternalRef{ExternalID: "1010000000100000444", ExternalType: "task", ExternalURL: testTAPDTaskURL}}
	if err := svc.WriteBackClarification(store, issue, "任务澄清结果"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected one local mock comment, got %d", calls)
	}
}

func TestTAPDTaskStatusAndPriority(t *testing.T) {
	for input, want := range map[string]string{"open": "todo", "progressing": "in_progress", "done": "done"} {
		if got := tapdTaskStatusToStatus(input); got != want {
			t.Errorf("task status %q = %q, want %q", input, got, want)
		}
	}
	for input, want := range map[string]string{"High": "high", "Medium": "medium", "Low": "low", "": "medium"} {
		if got := tapdTaskPriority(input); got != want {
			t.Errorf("task priority %q = %q, want %q", input, got, want)
		}
	}
}

// Opt-in integration verification. Only two TAPD read calls are made; the
// resulting issue and local execution settings exist in an ephemeral database.
// Dedicated variable names keep ordinary test runs away from real credentials.
func TestLiveTAPDTaskImportReadOnly(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("AGENTFLOW_LIVE_TAPD_MCP_TOKEN"))
	endpoint := strings.TrimSpace(os.Getenv("AGENTFLOW_LIVE_TAPD_MCP_URL"))
	if token == "" || endpoint == "" {
		t.Skip("set AGENTFLOW_LIVE_TAPD_MCP_TOKEN and AGENTFLOW_LIVE_TAPD_MCP_URL for read-only TAPD verification")
	}
	store, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := domain.Project{ID: "live-task-readonly", Name: "Read-only TAPD task verification"}
	if err := store.PutProject(project); err != nil {
		t.Fatal(err)
	}
	svc := NewService()
	svc.tapd = NewMCPClient(endpoint, map[string]string{"Authorization": "Bearer " + token})
	issue, err := svc.ImportTAPDStory(store, project.ID, "开发任务\n"+testTAPDTaskURL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Kind != "requirement" || issue.Title == "" || issue.ExternalRef == nil || issue.ExternalRef.ExternalType != "task" || issue.ExternalRef.ExternalURL != testTAPDTaskURL {
		t.Fatal("live task import did not preserve the task type, canonical URL and content")
	}
	issue.BaseBranch = "feature/live-readonly-verification"
	if err := store.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	updated, err := svc.ResyncTAPDIssue(store, issue)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != issue.ID || updated.BaseBranch != issue.BaseBranch || updated.ExternalRef == nil || updated.ExternalRef.ExternalType != "task" || updated.ExternalRef.ExternalURL != testTAPDTaskURL {
		t.Fatal("live task refresh did not preserve local base branch or external task identity")
	}
}

func mockTAPDTaskGateway(t *testing.T, respond func(string, map[string]any) string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Arguments struct {
					ToolName string         `json:"tool_name"`
					ToolArgs map[string]any `json:"tool_args"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"mock-task","version":"1"}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			payload := respond(req.Params.Arguments.ToolName, req.Params.Arguments.ToolArgs)
			escaped, _ := json.Marshal(payload)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":%s}]}}`, req.ID, escaped)
		default:
			t.Errorf("unexpected MCP method %q", req.Method)
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
}
