package tracker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/domain"
)

func TestImportTAPDStoryFromDetailURL(t *testing.T) {
	gateway := mockTAPDStoriesGateway(t, `{"data":[{"id":"1010000000100000555","name":"国际站取回绑定套餐域名逻辑对齐国内站","status":"status_38","priority":"3","created":"2024-07-10 17:06:47"}]}`)
	defer gateway.Close()

	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp", Name: "DNSPod"}}}
	issue, err := svc.ImportTAPDStory(store, "dp", "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100000555", 0)
	if err != nil {
		t.Fatal(err)
	}
	if issue.ID != "tapd-1010000000100000555" || issue.Source != "tapd" || issue.ProjectID != "dp" {
		t.Fatalf("issue=%+v", issue)
	}
	if issue.Title != "国际站取回绑定套餐域名逻辑对齐国内站" {
		t.Fatalf("title=%q", issue.Title)
	}
	if issue.Clarification.Passed || issue.Clarification.Status != domain.ClarificationNeedsUser || !issue.Clarification.Gated {
		t.Fatalf("thin import must need clarification: %+v", issue.Clarification)
	}
	if len(issue.Clarification.Dimensions) == 0 {
		t.Fatal("import score must not return empty dimensions")
	}
	if issue.ExternalRef == nil || issue.ExternalRef.ExternalID != "1010000000100000555" {
		t.Fatalf("external=%+v", issue.ExternalRef)
	}
	if !issue.ExternalRef.Imported {
		t.Fatal("imported TAPD story must be marked so iteration sync will not delete it")
	}
	stored, ok := store.Issue(issue.ID)
	if !ok || stored.Title != issue.Title {
		t.Fatalf("store=%+v ok=%v", stored, ok)
	}
}

func TestImportTAPDStoryFromTitleAndLink(t *testing.T) {
	gateway := mockTAPDStoriesGateway(t, `{"data":[{"id":"1010000000100001554","name":"TAPD 中的最新标题","status":"open"}]}`)
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp"}}}
	const link = "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001554"
	issue, err := svc.ImportTAPDStory(store, "dp", "【DP】用户复制的标题\n"+link, 0)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Title != "TAPD 中的最新标题" {
		t.Fatalf("title should come from TAPD, got %q", issue.Title)
	}
	if issue.ExternalRef == nil || issue.ExternalRef.ExternalURL != link {
		t.Fatalf("external reference should contain only the canonical URL, got %+v", issue.ExternalRef)
	}
	stored, ok := store.Issue(issue.ID)
	if !ok || stored.ExternalRef == nil || stored.ExternalRef.ExternalURL != link {
		t.Fatalf("stored issue has invalid external reference: %+v", stored)
	}
}

func TestImportTAPDStoryRejectsMultipleLinksBeforeCallingTracker(t *testing.T) {
	// No MCP endpoint is configured: ambiguity must fail before any external
	// request or issue write, rather than importing the first pasted record.
	svc := NewService()
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp"}}}
	_, err := svc.ImportTAPDStory(store, "dp", "https://tapd.example.com/tapd_fe/10000000/story/detail/1001\nhttps://tapd.example.com/tapd_fe/10000000/story/detail/1002", 0)
	if err == nil || !strings.Contains(err.Error(), "多个不同") {
		t.Fatalf("expected explicit ambiguity error, got %v", err)
	}
	if len(store.Issues()) != 0 {
		t.Fatal("ambiguous input must not create an issue")
	}
}

func TestImportTAPDStoryRefreshesExisting(t *testing.T) {
	gateway := mockTAPDStoriesGateway(t, `{"data":[{"id":"1001","name":"updated title","status":"open"}]}`)
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{
		projects: map[string]domain.Project{"dp": {ID: "dp"}},
		issues: map[string]domain.Issue{
			"tapd-1001": {ID: "tapd-1001", ProjectID: "dp", Title: "old", Source: "tapd", WorkflowID: "wf-1", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "1001"}},
		},
	}
	issue, err := svc.ImportTAPDStory(store, "dp", "https://tapd.example.com/tapd_fe/10000000/story/detail/1001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Title != "updated title" || issue.WorkflowID != "wf-1" {
		t.Fatalf("issue=%+v", issue)
	}
}

func TestFirstTAPDOwnerSelectsFirstHandler(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"胡锦康;", "胡锦康"},
		{"胡锦康;张三;", "胡锦康"},
		{" example-maintainer ", "example-maintainer"},
		{"; ;alice;bob", "alice"},
		{"", ""},
		{";", ""},
	}
	for _, tc := range cases {
		if got := firstTAPDOwner(tc.in); got != tc.want {
			t.Fatalf("firstTAPDOwner(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestImportTAPDStoryAssignsCreatorNotHandler(t *testing.T) {
	gateway := mockTAPDStoriesGateway(t, `{"data":[{"id":"2001","name":"handler story","status":"open","creator":"胡锦康","owner":"张三;"}]}`)
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{
		projects: map[string]domain.Project{"dp": {ID: "dp", OwnerID: 42}},
		users: []domain.User{
			{ID: 7, LoginName: "example-maintainer", ChineseName: "胡锦康"},
			{ID: 8, LoginName: "zhangsan", ChineseName: "张三"},
			{ID: 42, LoginName: "owner", ChineseName: "项目负责人"},
		},
	}
	issue, err := svc.ImportTAPDStory(store, "dp", "https://tapd.example.com/tapd_fe/10000000/story/detail/2001", 99)
	if err != nil {
		t.Fatal(err)
	}
	if issue.AssigneeUserID != 7 {
		t.Fatalf("assignee=%d want TAPD creator 7, not handler 8", issue.AssigneeUserID)
	}
}

func TestImportTAPDStoryMatchesHandlerLoginInParentheses(t *testing.T) {
	gateway := mockTAPDStoriesGateway(t, `{"data":[{"id":"2002","name":"login story","status":"open","creator":"胡锦康(example-maintainer)","owner":"alice;"}]}`)
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{
		projects: map[string]domain.Project{"dp": {ID: "dp", OwnerID: 42}},
		users:    []domain.User{{ID: 7, LoginName: "example-maintainer", ChineseName: "胡锦康"}},
	}
	issue, err := svc.ImportTAPDStory(store, "dp", "https://tapd.example.com/tapd_fe/10000000/story/detail/2002", 99)
	if err != nil {
		t.Fatal(err)
	}
	if issue.AssigneeUserID != 7 {
		t.Fatalf("assignee=%d want login matched creator 7", issue.AssigneeUserID)
	}
}

func TestImportTAPDStoryFallsBackToImporterWhenHandlerMissing(t *testing.T) {
	gateway := mockTAPDStoriesGateway(t, `{"data":[{"id":"2003","name":"unknown handler","status":"open","creator":"ghost","owner":"胡锦康;"}]}`)
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{
		projects: map[string]domain.Project{"dp": {ID: "dp", OwnerID: 42}},
		users: []domain.User{
			{ID: 7, LoginName: "example-maintainer", ChineseName: "胡锦康"},
			{ID: 42, LoginName: "owner", ChineseName: "项目负责人"},
		},
	}
	issue, err := svc.ImportTAPDStory(store, "dp", "https://tapd.example.com/tapd_fe/10000000/story/detail/2003", 99)
	if err != nil {
		t.Fatal(err)
	}
	if issue.AssigneeUserID != 99 {
		t.Fatalf("assignee=%d want importer fallback 99, not the TAPD handler", issue.AssigneeUserID)
	}
}

func TestSyncTAPDStoryFallsBackToProjectOwnerWhenHandlerMissing(t *testing.T) {
	gateway := mockTAPDStoriesGateway(t, `{"data":[{"id":"2004","name":"sync unknown","status":"open","creator":"ghost","owner":"胡锦康;"}]}`)
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{
		projects: map[string]domain.Project{"dp": {ID: "dp", OwnerID: 42}},
		users: []domain.User{
			{ID: 7, LoginName: "example-maintainer", ChineseName: "胡锦康"},
			{ID: 42, LoginName: "owner", ChineseName: "项目负责人"},
		},
	}
	issue, err := svc.ImportTAPDStory(store, "dp", "https://tapd.example.com/tapd_fe/10000000/story/detail/2004", 0)
	if err != nil {
		t.Fatal(err)
	}
	if issue.AssigneeUserID != 42 {
		t.Fatalf("assignee=%d want project owner 42, not the TAPD handler", issue.AssigneeUserID)
	}
}

func TestImportTAPDStoryKeepsExistingAssignee(t *testing.T) {
	gateway := mockTAPDStoriesGateway(t, `{"data":[{"id":"2005","name":"updated","status":"open","creator":"胡锦康","owner":"张三;"}]}`)
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{
		projects: map[string]domain.Project{"dp": {ID: "dp", OwnerID: 42}},
		users:    []domain.User{{ID: 7, LoginName: "example-maintainer", ChineseName: "胡锦康"}},
		issues: map[string]domain.Issue{
			"tapd-2005": {ID: "tapd-2005", ProjectID: "dp", Title: "old", Source: "tapd", AssigneeUserID: 5, ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "2005"}},
		},
	}
	issue, err := svc.ImportTAPDStory(store, "dp", "https://tapd.example.com/tapd_fe/10000000/story/detail/2005", 99)
	if err != nil {
		t.Fatal(err)
	}
	if issue.AssigneeUserID != 5 {
		t.Fatalf("assignee=%d want preserved 5", issue.AssigneeUserID)
	}
}

func TestImportTAPDStoryScoresCompleteDescription(t *testing.T) {
	payload := `{"data":[{"id":"1002","name":"国际站绑定对齐","status":"open","description":"<p>目标是解决国际站绑定套餐域名和国内站不一致的问题，预期产生对齐全站业务价值。</p><p>用户是国际站套餐管理员，在续费场景下使用。</p><p>本次范围包含取回绑定逻辑，不包含计费边界。</p><p>验收标准：国内站与国际站绑定结果一致，相关接口测试通过即可判断完成。</p><p>约束是必须兼容现有套餐依赖，本迭代截止前交付，注意回滚风险。</p>"}]}`
	gateway := mockTAPDStoriesGateway(t, payload)
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp", Name: "DNSPod"}}}
	issue, err := svc.ImportTAPDStory(store, "dp", "https://tapd.example.com/tapd_fe/10000000/story/detail/1002", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !issue.Clarification.Passed || issue.Clarification.Score < 80 {
		t.Fatalf("complete description should pass: %+v", issue.Clarification)
	}
	if issue.Clarification.Status == domain.ClarificationNeedsUser {
		t.Fatal("passed import must not ask the user to clarify")
	}
}

func TestSyncTAPDStoryDoesNotScoreOrGate(t *testing.T) {
	gateway := mockTAPDStoriesGateway(t, `{"data":[{"id":"3001","name":"【国内站】mq_flush_dns命令CMQ SDK连接异常","status":"open"}]}`)
	defer gateway.Close()
	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp", Name: "DNSPod"}}}
	issue, err := svc.upsertTAPDStory(store, "dp", "10000000", tapdStory{ID: "3001", Name: "【国内站】mq_flush_dns命令CMQ SDK连接异常", Status: "open"}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Clarification.Gated || issue.Clarification.Score != 0 || len(issue.Clarification.Dimensions) != 0 {
		t.Fatalf("sync must not score: %+v", issue.Clarification)
	}
}

func mockTAPDStoriesGateway(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	var initialized atomic.Bool
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"mock","version":"1"}}}`, req.ID)
		case "notifications/initialized":
			initialized.Store(true)
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if !initialized.Load() {
				t.Fatal("tool called before initialized")
			}
			escaped, _ := json.Marshal(payload)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":%s}]}}`, req.ID, escaped)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
}
