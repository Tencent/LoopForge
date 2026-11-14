package tracker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"agentflow/internal/domain"
)

func clarificationGateway(t *testing.T, check func(string, map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			check(req.Params.Name, req.Params.Arguments)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"{}"}]}}`, req.ID)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
}

func TestWriteBackClarificationToGongfengIssue(t *testing.T) {
	gateway := clarificationGateway(t, func(name string, args map[string]any) {
		if name != "create_issue_note" || args["project_id"] != "org/repo" || args["issue_id"] != float64(88) {
			t.Fatalf("unexpected gongfeng call %s %#v", name, args)
		}
	})
	defer gateway.Close()
	svc := NewService()
	svc.gongfeng = NewMCPClient(gateway.URL, nil)
	st := &memIssueStore{repositories: map[string]domain.Repository{
		"repo": {ID: "repo", Provider: "gongfeng", GongfengProjectID: "org/repo"},
	}}
	issue := domain.Issue{ID: "gf-repo-7", Source: "gongfeng", RepositoryID: "repo", ExternalRef: &domain.ExternalRef{ExternalID: "88", ExternalIID: 7}}
	if err := svc.WriteBackClarification(st, issue, "## AgentFlow 澄清结论\n\n结论"); err != nil {
		t.Fatal(err)
	}
}

func TestWriteBackClarificationToTAPDStory(t *testing.T) {
	gateway := clarificationGateway(t, func(name string, args map[string]any) {
		if name != "proxy_execute_tool" || args["tool_name"] != "comments_create" {
			t.Fatalf("unexpected TAPD call %s %#v", name, args)
		}
		toolArgs, ok := args["tool_args"].(map[string]any)
		if !ok || toolArgs["workspace_id"] != "101" || toolArgs["entry_type"] != "story" || toolArgs["entry_id"] != "202" {
			t.Fatalf("unexpected TAPD args %#v", args["tool_args"])
		}
	})
	defer gateway.Close()
	svc := NewService()
	svc.tapd = NewMCPClient(gateway.URL, nil)
	st := &memIssueStore{projects: map[string]domain.Project{
		"alpha": {ID: "alpha", TAPDWorkspaceID: "101"},
	}}
	issue := domain.Issue{ID: "tapd-202", ProjectID: "alpha", Source: "tapd", ExternalRef: &domain.ExternalRef{ExternalID: "202"}}
	if err := svc.WriteBackClarification(st, issue, "## AgentFlow 澄清结论\n\n结论"); err != nil {
		t.Fatal(err)
	}
}

func TestWriteBackClarificationActsAsAssigneeNotOwner(t *testing.T) {
	svc := NewService()
	svc.ConfigureWithStore(domain.SyncConfig{TAPDMCPURL: "https://mcpgw.example/tapd"}, projectAuthFixture{
		projects: map[string]domain.Project{"alpha": {ID: "alpha", OwnerID: 1, TAPDWorkspaceID: "101"}},
		users: map[int64]domain.User{
			1: {ID: 1, LoginName: "owner"},
			2: {ID: 2, LoginName: "assignee"},
		},
		servers: []domain.MCPServerDefinition{{ID: "mcp-70-project-alpha", MarketID: "70", ProjectID: "alpha", Scope: "project", Transport: "http", URL: "https://mcpgw.example/tapd", Headers: map[string]string{"Authorization": "Bearer tapd-token"}}},
	})
	client, source, err := svc.trackerClientForWriteBack("tapd", "alpha", domain.Issue{ProjectID: "alpha", AssigneeUserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if client.Header("Authorization") != "Bearer tapd-token" {
		t.Fatalf("authorization=%q", client.Header("Authorization"))
	}
	if source != "project-user:assignee" {
		t.Fatalf("source=%q", source)
	}
}

func TestWriteBackClarificationFallsBackToOwnerWithoutAssignee(t *testing.T) {
	svc := NewService()
	svc.ConfigureWithStore(domain.SyncConfig{TAPDMCPURL: "https://mcpgw.example/tapd"}, projectAuthFixture{
		projects: map[string]domain.Project{"alpha": {ID: "alpha", OwnerID: 1, TAPDWorkspaceID: "101"}},
		users: map[int64]domain.User{
			1: {ID: 1, LoginName: "owner"},
		},
		servers: []domain.MCPServerDefinition{{ID: "mcp-70-project-alpha", MarketID: "70", ProjectID: "alpha", Scope: "project", Transport: "http", URL: "https://mcpgw.example/tapd", Headers: map[string]string{"Authorization": "Bearer tapd-token"}}},
	})
	client, source, err := svc.trackerClientForWriteBack("tapd", "alpha", domain.Issue{ProjectID: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if client.Header("Authorization") != "Bearer tapd-token" {
		t.Fatalf("authorization=%q", client.Header("Authorization"))
	}
	if source != "project-owner:owner" {
		t.Fatalf("source=%q", source)
	}
}

func TestWriteBackClarificationToGitHubIssue(t *testing.T) {
	var got map[string]string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/repos/example/repo/issues/12/comments" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer owner-token" {
			t.Errorf("comment must use the project owner's Git token")
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()
	repository := domain.Repository{ID: "gh-repo", ProjectID: "alpha", Provider: "github", GongfengProjectID: "github:example/repo"}
	svc := NewService()
	svc.ConfigureWithStore(domain.SyncConfig{}, projectAuthFixture{
		projects: map[string]domain.Project{"alpha": {ID: "alpha", OwnerID: 1}},
		users:    map[int64]domain.User{1: {ID: 1, LoginName: "owner", GitToken: "owner-token"}},
		repos:    []domain.Repository{repository},
	})
	svc.githubAPIBase = srv.URL
	st := &memIssueStore{repositories: map[string]domain.Repository{"gh-repo": repository}}
	issue := domain.Issue{ID: "gh-gh-repo-12", Source: "github", RepositoryID: "gh-repo", ExternalRef: &domain.ExternalRef{Source: "github", ExternalID: "9001", ExternalIID: 12}}
	if err := svc.WriteBackClarification(st, issue, "## AgentFlow 澄清结论\n\n结论"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got["body"] != "## AgentFlow 澄清结论\n\n结论" {
		t.Fatalf("calls=%d body=%#v", calls, got)
	}

	missingNumber := issue
	missingNumber.ExternalRef = &domain.ExternalRef{Source: "github", ExternalID: "9001"}
	if err := svc.WriteBackClarification(st, missingNumber, "x"); err == nil {
		t.Fatal("GitHub write-back without an issue number must fail")
	}
	unbound := issue
	unbound.RepositoryID = "missing"
	if err := svc.WriteBackClarification(st, unbound, "x"); err == nil {
		t.Fatal("GitHub write-back without a bound GitHub repository must fail")
	}
	if calls != 1 {
		t.Fatalf("invalid write-backs must not call GitHub, calls=%d", calls)
	}
}
