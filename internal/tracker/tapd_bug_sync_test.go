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

func TestParseTAPDBugURLDetailPage(t *testing.T) {
	ref, err := ParseTAPDStoryURL("https://tapd.example.com/tapd_fe/10000000/bug/detail/1010000000163775911")
	if err != nil {
		t.Fatal(err)
	}
	if ref.WorkspaceID != "10000000" || ref.StoryID != "1010000000163775911" {
		t.Fatalf("ref=%+v", ref)
	}
	if ref.Kind != "bug" {
		t.Fatalf("kind=%q want bug", ref.Kind)
	}
}

func TestParseTAPDStoryURLKindDefaultsToStory(t *testing.T) {
	ref, err := ParseTAPDStoryURL("https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000118483431")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Kind != "story" {
		t.Fatalf("kind=%q want story", ref.Kind)
	}
}

func TestParseTAPDBugs(t *testing.T) {
	payload := `{"count":2,"data":[
		{"id":"1010000000163775911","title":"国际站dnssec调用digns服务失败","description":"## 问题描述\n连接异常","status":"new","current_owner":"jerryyaohu","priority":"low","iteration_id":"1010000000002246938","created":"2026-09-10 10:11:11","modified":"2026-09-10 10:11:11"},
		{"id":"1010000000163775895","title":"国际站Batch.Search报错","description":"","status":"resolved","current_owner":"","priority":"middle","created":"2026-09-10 10:10:59","modified":"2026-09-10 10:10:59"}
	]}`
	bugs, err := parseTAPDBugs(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(bugs) != 2 {
		t.Fatalf("len=%d want 2", len(bugs))
	}
	if bugs[0].Name != "国际站dnssec调用digns服务失败" {
		t.Fatalf("name=%q want title fallback", bugs[0].Name)
	}
	if bugs[0].Description == "" || bugs[0].CurrentOwner != "jerryyaohu" {
		t.Fatalf("bug fields not mapped: %+v", bugs[0])
	}
}

func TestTapdBugStatusToStatus(t *testing.T) {
	cases := map[string]string{
		"new":       "todo",
		"New":       "todo",
		"resolved":  "in_review",
		"verified":  "in_review",
		"closed":    "closed",
		"reopened":  "in_progress",
		"rejected":  "cancelled",
		"postponed": "backlog",
		"":          "backlog",
	}
	for in, want := range cases {
		if got := tapdBugStatusToStatus(in); got != want {
			t.Errorf("tapdBugStatusToStatus(%q)=%q want %q", in, got, want)
		}
	}
}

func TestUpsertTAPDBug(t *testing.T) {
	svc := NewService()
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp", Name: "DNSPod"}}}
	issue, err := svc.upsertTAPDBug(store, "dp", "10000000", tapdStory{
		ID: "1010000000163775911", Name: "国际站dnssec调用digns服务失败",
		Description: "## 问题描述\n连接异常", Status: "new", Priority: "low",
		CurrentOwner: "jerryyaohu",
	}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Kind != "bug" {
		t.Fatalf("kind=%q want bug", issue.Kind)
	}
	if issue.Status != "todo" {
		t.Fatalf("status=%q want todo", issue.Status)
	}
	if issue.Description == "" {
		t.Fatal("description must be stored")
	}
	if issue.ExternalRef == nil || issue.ExternalRef.ExternalType != "bug" {
		t.Fatalf("externalRef=%+v", issue.ExternalRef)
	}
	if got := issue.ExternalRef.ExternalURL; !strings.Contains(got, "/bug/detail/1010000000163775911") {
		t.Fatalf("url=%q", got)
	}
}

// mockTAPDMultiGateway serves both stories_get and bugs_get and records the
// tool_args of each call so tests can assert the "fields" argument.
func mockTAPDMultiGateway(t *testing.T, storiesPayload, bugsPayload string, calls *atomic.Int32, lastArgs *map[string]any) *httptest.Server {
	t.Helper()
	var initialized atomic.Bool
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
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
			var wrapper struct {
				ToolName string         `json:"tool_name"`
				ToolArgs map[string]any `json:"tool_args"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &wrapper)
			if lastArgs != nil {
				*lastArgs = wrapper.ToolArgs
			}
			if calls != nil {
				calls.Add(1)
			}
			payload := storiesPayload
			if wrapper.ToolName == "bugs_get" {
				payload = bugsPayload
			}
			escaped, _ := json.Marshal(payload)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":%s}]}}`, req.ID, escaped)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
}

func TestPullTAPDPoolPullsStoriesAndBugs(t *testing.T) {
	storiesPayload := `{"count":1,"data":[{"id":"1001","name":"需求A","description":"需求正文","status":"planned","owner":"zhangsan(low)","priority":"middle","iteration_id":"it1","created":"2026-09-10 10:00:00","modified":"2026-09-10 10:00:00"}]}`
	bugsPayload := `{"count":1,"data":[{"id":"2001","title":"缺陷B","description":"缺陷正文","status":"new","current_owner":"lisi","severity":"normal","priority":"low","iteration_id":"it1","created":"2026-09-10 10:00:00","modified":"2026-09-10 10:00:00"}]}`
	var calls atomic.Int32
	var lastArgs map[string]any
	gateway := mockTAPDMultiGateway(t, storiesPayload, bugsPayload, &calls, &lastArgs)
	defer gateway.Close()

	svc := NewService()
	svc.Configure(domain.SyncConfig{TAPDMCPURL: gateway.URL})
	store := &memIssueStore{projects: map[string]domain.Project{"dp": {ID: "dp", Name: "DNSPod", TAPDWorkspaceID: "10000000", TAPDIterationID: "it1"}}}
	client, _, err := svc.trackerClientForProject("tapd", "dp")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	count, err := svc.pullTAPDPool(client, store, tapdPool{ProjectID: "dp", WorkspaceID: "10000000", IterationID: "it1"}, seen)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("count=%d want 2 (1 story + 1 bug)", count)
	}
	if !seen["1001"] || !seen["2001"] {
		t.Fatalf("seen=%v", seen)
	}
	// Both stories_get and bugs_get must request description explicitly,
	// otherwise the gateway trims the field out of the response.
	if !strings.Contains(fmt.Sprint(lastArgs["fields"]), "description") {
		t.Fatalf("fields arg missing description: %v", lastArgs)
	}
	if calls.Load() < 2 {
		t.Fatalf("expected at least one stories_get and one bugs_get call, got %d", calls.Load())
	}

	story, ok := store.Issue("tapd-1001")
	if !ok || story.Kind != "requirement" || story.Description != "需求正文" {
		t.Fatalf("story issue=%+v ok=%v", story, ok)
	}
	bug, ok := store.Issue("tapd-2001")
	if !ok || bug.Kind != "bug" || bug.Description != "缺陷正文" {
		t.Fatalf("bug issue=%+v ok=%v", bug, ok)
	}
	if bug.Status != "todo" {
		t.Fatalf("bug status=%q want todo", bug.Status)
	}
}
