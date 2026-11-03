package tracker

import (
	"strings"
	"testing"

	"agentflow/internal/domain"
)

type projectAuthFixture struct {
	projects map[string]domain.Project
	users    map[int64]domain.User
	servers  []domain.MCPServerDefinition
	repos    []domain.Repository
}

func (f projectAuthFixture) Projects() []domain.Project {
	out := make([]domain.Project, 0, len(f.projects))
	for _, project := range f.projects {
		out = append(out, project)
	}
	return out
}

func (f projectAuthFixture) Project(id string) (domain.Project, bool) {
	project, ok := f.projects[id]
	return project, ok
}

func (f projectAuthFixture) Repositories() []domain.Repository { return f.repos }

func (f projectAuthFixture) User(id int64) (domain.User, bool) {
	user, ok := f.users[id]
	return user, ok
}

func (f projectAuthFixture) MCPServers() []domain.MCPServerDefinition { return f.servers }

func TestBindTrackerMCPPrefersCatalogHeadersOverEnvToken(t *testing.T) {
	cfg := domain.SyncConfig{
		GongfengMCPURL: "https://mcpgw.knot.example.com/gongfeng",
		TAPDMCPURL:     "https://mcpgw.knot.example.com/tapd/",
	}
	servers := []domain.MCPServerDefinition{{
		ID:        "gongfeng-delivery",
		Transport: "http",
		URL:       "https://mcpgw.knot.example.com/gongfeng",
		Headers:   map[string]string{"Authorization": "Bearer catalog-pat"},
	}}
	gf := bindTrackerMCP("gongfeng", cfg, servers, "expired-env-token", "Bearer expired-oauth")
	if gf.Source != "catalog:gongfeng-delivery" {
		t.Fatalf("gongfeng source=%q", gf.Source)
	}
	if gf.Headers["Authorization"] != "Bearer catalog-pat" {
		t.Fatalf("gongfeng auth=%q", gf.Headers["Authorization"])
	}
	tapd := bindTrackerMCP("tapd", cfg, servers, "expired-env-token", "Bearer expired-oauth")
	if tapd.Source != "catalog:gongfeng-delivery+tapd-url" {
		t.Fatalf("tapd source=%q", tapd.Source)
	}
	if tapd.URL != cfg.TAPDMCPURL {
		t.Fatalf("tapd url=%q", tapd.URL)
	}
	if tapd.Headers["Authorization"] != "Bearer catalog-pat" {
		t.Fatalf("tapd should reuse gongfeng catalog auth, got %q", tapd.Headers["Authorization"])
	}
}

func TestBindTrackerMCPUsesDedicatedTAPDCatalog(t *testing.T) {
	cfg := domain.SyncConfig{TAPDMCPURL: "https://mcpgw.knot.example.com/tapd/"}
	servers := []domain.MCPServerDefinition{
		{ID: "gongfeng-delivery", Transport: "http", URL: "https://mcpgw.knot.example.com/gongfeng", Headers: map[string]string{"Authorization": "Bearer gf"}},
		{ID: "tapd-delivery", Transport: "http", URL: "https://mcpgw.knot.example.com/tapd/", Headers: map[string]string{"Authorization": "Bearer tapd"}},
	}
	got := bindTrackerMCP("tapd", cfg, servers, "env", "")
	if got.Source != "catalog:tapd-delivery" || got.Headers["Authorization"] != "Bearer tapd" {
		t.Fatalf("got %+v", got)
	}
}

func TestBindTrackerMCPFallsBackWhenCatalogMissing(t *testing.T) {
	cfg := domain.SyncConfig{GongfengMCPURL: "https://mcpgw.knot.example.com/gongfeng"}
	got := bindTrackerMCP("gongfeng", cfg, nil, "env-token", "")
	if got.Source != "env:AGENTFLOW_MCP_TOKEN" {
		t.Fatalf("source=%q", got.Source)
	}
	if got.Headers["Authorization"] != "Bearer env-token" {
		t.Fatalf("auth=%q", got.Headers["Authorization"])
	}
	got = bindTrackerMCP("gongfeng", cfg, nil, "", "Bearer oauth")
	if got.Source != "codebuddy:mcp.json" || !strings.Contains(got.Headers["Authorization"], "oauth") {
		t.Fatalf("got %+v", got)
	}
}

func TestBindProjectOwnerTrackerMCPUsesOnlyTargetProjectCredential(t *testing.T) {
	cfg := domain.SyncConfig{GongfengMCPURL: "https://fallback.example/gongfeng"}
	servers := []domain.MCPServerDefinition{
		{ID: "mcp-gongfeng-project-a", MarketID: "gongfeng", ProjectID: "a", Scope: "project", Transport: "http", URL: "https://a.example/gongfeng", Headers: map[string]string{"Authorization": "Bearer token-a"}},
		{ID: "mcp-gongfeng-project-b", MarketID: "gongfeng", ProjectID: "b", Scope: "project", Transport: "http", URL: "https://b.example/gongfeng", Headers: map[string]string{"Authorization": "Bearer token-b"}},
	}
	got, err := bindProjectOwnerTrackerMCP("gongfeng", cfg, servers, "b", domain.User{LoginName: "owner-b"})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://b.example/gongfeng" {
		t.Fatalf("url=%q", got.URL)
	}
	if got.Headers["Authorization"] != "Bearer token-b" {
		t.Fatalf("authorization=%q", got.Headers["Authorization"])
	}
	if got.Source != "project-owner:owner-b" {
		t.Fatalf("source=%q", got.Source)
	}
}

func TestBindProjectOwnerTrackerMCPRejectsConfiguredURLWithoutCredential(t *testing.T) {
	cfg := domain.SyncConfig{TAPDMCPURL: "https://mcp.example.com/tapd/"}
	got, err := bindProjectOwnerTrackerMCP("tapd", cfg, nil, "dns", domain.User{LoginName: "owner"})
	if err == nil || !strings.Contains(err.Error(), "TAPD MCP 未配置认证") {
		t.Fatalf("err=%v", err)
	}
	if got.URL != cfg.TAPDMCPURL {
		t.Fatalf("got %+v", got)
	}
}

func TestBindProjectOwnerTrackerMCPUsesMarketNumericID(t *testing.T) {
	cfg := domain.SyncConfig{TAPDMCPURL: "https://fallback.example/tapd"}
	got, err := bindProjectOwnerTrackerMCP("tapd", cfg, []domain.MCPServerDefinition{{
		ID: "mcp-70-project-dns", MarketID: "70", Name: "TAPD MCP", ProjectID: "dns", Scope: "project",
		Transport: "http", URL: "https://mcp.example.com/tapd/", Headers: map[string]string{"Authorization": "Bearer tapd-token"},
	}}, "dns", domain.User{LoginName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://mcp.example.com/tapd/" || got.Headers["Authorization"] != "Bearer tapd-token" {
		t.Fatalf("got %+v", got)
	}
}

func TestBindProjectOwnerTrackerMCPRequiresProjectCredential(t *testing.T) {
	_, err := bindProjectOwnerTrackerMCP("gongfeng", domain.SyncConfig{}, []domain.MCPServerDefinition{{
		ID: "mcp-61-project-dns", MarketID: "61", ProjectID: "dns", Scope: "project", Transport: "http", URL: "https://mcp.example.com/gongfeng",
	}}, "dns", domain.User{LoginName: "owner"})
	if err == nil || !strings.Contains(err.Error(), "工蜂 MCP 未配置认证") || !strings.Contains(err.Error(), "Owner owner") {
		t.Fatalf("err=%v", err)
	}
}

func TestServiceResolvesDifferentCredentialPerProject(t *testing.T) {
	fixture := projectAuthFixture{
		projects: map[string]domain.Project{
			"a": {ID: "a", OwnerID: 1},
			"b": {ID: "b", OwnerID: 2},
		},
		users: map[int64]domain.User{
			1: {ID: 1, LoginName: "owner-a"},
			2: {ID: 2, LoginName: "owner-b"},
		},
		servers: []domain.MCPServerDefinition{
			{ID: "mcp-61-project-a", MarketID: "61", ProjectID: "a", Scope: "project", Transport: "http", URL: "https://mcp.example.com/gongfeng", Headers: map[string]string{"Authorization": "Bearer token-a"}},
			{ID: "mcp-61-project-b", MarketID: "61", ProjectID: "b", Scope: "project", Transport: "http", URL: "https://mcp.example.com/gongfeng", Headers: map[string]string{"Authorization": "Bearer token-b"}},
		},
	}
	svc := NewService()
	svc.ConfigureWithStore(domain.SyncConfig{GongfengMCPURL: "https://mcp.example.com/gongfeng"}, fixture)
	clientA, _, err := svc.trackerClientForProject("gongfeng", "a")
	if err != nil {
		t.Fatal(err)
	}
	clientB, _, err := svc.trackerClientForProject("gongfeng", "b")
	if err != nil {
		t.Fatal(err)
	}
	if clientA.Header("Authorization") != "Bearer token-a" || clientB.Header("Authorization") != "Bearer token-b" {
		t.Fatalf("unexpected project auth: a=%q b=%q", clientA.Header("Authorization"), clientB.Header("Authorization"))
	}
}
