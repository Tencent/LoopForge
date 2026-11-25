package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func newMCPCloneTestServer(t *testing.T) (*repo.Store, *catalogHandlers) {
	t.Helper()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	for _, project := range []string{"source", "target"} {
		if err := s.PutProject(domain.Project{ID: project, Name: project}); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{deps: &deps{repo: s}}
	return s, &catalogHandlers{deps: server.deps}
}

// A shared role may depend on a market MCP installed in a project the caller
// cannot read. The market entry holds no project settings, so the clone
// installs it from the catalog instead of dropping the dependency.
func TestCloneAgentMCPServersInstallsMarketEntryWithoutSourceAccess(t *testing.T) {
	s, catalog := newMCPCloneTestServer(t)
	for _, server := range []domain.MCPServerDefinition{
		{ID: "mcp-github-project-source", MarketID: "github", Name: "GitHub", Scope: "project", ProjectID: "source", Transport: "http", URL: "https://example.com/github",
			Headers: map[string]string{"Authorization": "Bearer source-project-secret"}},
		{ID: "custom-source", Name: "Custom", Scope: "project", ProjectID: "source", Transport: "http", URL: "https://example.com/custom"},
	} {
		if err := s.PutMCPServer(server); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := catalog.cloneAgentMCPServers([]string{"mcp-github-project-source", "custom-source"}, "target", 3, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("cloned ids = %#v, want only the market entry", ids)
	}
	installed, ok := s.MCPServer(ids[0])
	if !ok || installed.MarketID != "github" || installed.Scope != "project" || installed.ProjectID != "target" {
		t.Fatalf("market install = %+v", installed)
	}
	if strings.Contains(installed.Headers["Authorization"], "source-project-secret") {
		t.Fatalf("market install copied the source project's settings: %+v", installed.Headers)
	}
	again, err := catalog.cloneAgentMCPServers([]string{"mcp-github-project-source"}, "target", 3, func(string) bool { return false })
	if err != nil || len(again) != 1 || again[0] != ids[0] {
		t.Fatalf("second clone = %#v, %v; want the existing install %s", again, err, ids[0])
	}
}

// Install IDs are predictable. A definition another user created under such
// an ID first must not block market installs, custom installs or role clones
// for the destination it names.
func TestMCPInstallIDsIgnorePredictableSquatters(t *testing.T) {
	s, catalog := newMCPCloneTestServer(t)
	if err := s.PutMCPServer(domain.MCPServerDefinition{ID: "role-source", Name: "Role MCP", Scope: "project", ProjectID: "source", Transport: "http", URL: "https://example.com/role"}); err != nil {
		t.Fatal(err)
	}
	squatted := []string{
		mcpInstallID("github", "project", "target"),
		mcpInstallID("clone-role-source", "project", "target"),
		mcpInstallID("custom-tool", "project", "target"),
	}
	for _, id := range squatted {
		if err := s.PutMCPServer(domain.MCPServerDefinition{ID: id, Name: "squatter", Scope: "personal", OwnerUserID: 9, Transport: "http", URL: "https://example.com/squat"}); err != nil {
			t.Fatal(err)
		}
	}

	installed, created, err := installMarketMCP(s, "github", "target")
	if err != nil || !created || installed.ProjectID != "target" || installed.Scope != "project" || installed.ID == squatted[0] {
		t.Fatalf("market install = %+v created=%v err=%v", installed, created, err)
	}
	if _, created, err := installMarketMCP(s, "github", "target"); err != nil || created {
		t.Fatalf("repeated market install created=%v err=%v", created, err)
	}

	ids, err := catalog.cloneAgentMCPServers([]string{"role-source"}, "target", 3, func(project string) bool { return project == "source" })
	if err != nil || len(ids) != 1 || ids[0] == squatted[1] {
		t.Fatalf("clone ids = %#v err=%v", ids, err)
	}
	clone, ok := s.MCPServer(ids[0])
	if !ok || clone.Scope != "project" || clone.ProjectID != "target" || clone.URL != "https://example.com/role" {
		t.Fatalf("clone = %+v", clone)
	}
	again, err := catalog.cloneAgentMCPServers([]string{"role-source"}, "target", 3, func(project string) bool { return project == "source" })
	if err != nil || len(again) != 1 || again[0] != ids[0] {
		t.Fatalf("repeated clone = %#v err=%v; want the existing copy %s", again, err, ids[0])
	}
	if first, second := destinationMCPInstallID(s, []byte("key"), "custom-tool", "project", "target"), destinationMCPInstallID(s, []byte("key"), "custom-tool", "project", "target"); first != second {
		t.Fatalf("keyed alternate ids differ: %q %q", first, second)
	}
	if keyed, other := destinationMCPInstallID(s, []byte("key"), "custom-tool", "project", "target"), destinationMCPInstallID(s, []byte("other-key"), "custom-tool", "project", "target"); keyed == other {
		t.Fatalf("alternate id does not depend on the key: %q", keyed)
	}

	if id := destinationMCPInstallID(s, []byte("key"), "custom-tool", "project", "target"); id == squatted[2] || !strings.HasPrefix(id, "mcp-custom-tool-") {
		t.Fatalf("custom install id = %q", id)
	}
	// The destination's own definition keeps its deterministic ID, so
	// repeated installs still find it.
	if id := destinationMCPInstallID(s, []byte("key"), "github", "personal", "9"); id != mcpInstallID("github", "personal", "9") {
		t.Fatalf("unused id = %q", id)
	}
	for _, id := range squatted {
		if squatter, ok := s.MCPServer(id); !ok || squatter.OwnerUserID != 9 || squatter.Scope != "personal" {
			t.Fatalf("squatter %s changed: %+v", id, squatter)
		}
	}
}

// HTTP coverage for the ownership boundaries: an import that touches another
// user's personal MCP is rejected as a whole, and a role that its project did
// not share cannot be cloned by someone outside that project.
func TestMCPImportRollbackAndUnsharedRoleCloneHTTP(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutUser(domain.User{LoginName: "outsider"}); err != nil {
		t.Fatal(err)
	}
	outsider, ok := f.s.UserByLoginName("outsider")
	if !ok {
		t.Fatal("outsider missing")
	}
	if err := f.s.PutProject(domain.Project{ID: "source", Name: "Source", Members: []domain.ProjectMember{{UserID: f.user.ID, Role: "member"}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutProject(domain.Project{ID: "target", Name: "Target", Members: []domain.ProjectMember{{UserID: outsider.ID, Role: "member"}}}); err != nil {
		t.Fatal(err)
	}
	owned := domain.MCPServerDefinition{ID: "owned-personal", Name: "Owned", Scope: "personal", OwnerUserID: f.user.ID, Transport: "http", URL: "https://example.test/owned"}
	if err := f.s.PutMCPServer(owned); err != nil {
		t.Fatal(err)
	}
	if err := f.s.PutAgent(domain.AgentDefinition{ID: "private-role", Name: "Private", ProjectID: "source", Prompt: "private"}); err != nil {
		t.Fatal(err)
	}
	as := func(user domain.User, method, path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, authedRequest(t, req, user))
		return rec
	}

	hijack := owned
	hijack.URL = "https://evil.example.test"
	w := as(outsider, http.MethodPost, "/api/mcp-servers/import", []domain.MCPServerDefinition{
		{ID: "target-tool", Name: "Target tool", Scope: "project", ProjectID: "target", Transport: "http", URL: "https://example.test/target"},
		hijack,
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("import touching another user's MCP: %d %s", w.Code, w.Body.String())
	}
	if _, found := f.s.MCPServer("target-tool"); found {
		t.Fatal("rejected import saved its first entry")
	}
	if stored, _ := f.s.MCPServer(owned.ID); stored.URL != owned.URL || stored.OwnerUserID != f.user.ID {
		t.Fatalf("personal MCP changed: %+v", stored)
	}

	w = as(outsider, http.MethodPost, "/api/agents/private-role/clone", map[string]string{"id": "copied-role", "name": "Copied", "projectId": "target"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("clone of an unshared role: %d %s", w.Code, w.Body.String())
	}
	if _, found := f.s.Agent("copied-role"); found {
		t.Fatal("forbidden clone saved a role")
	}
}

// Squatting the base ID and the first keyed alternate must still give the
// destination one stable ID: repeated clones reuse the copy and a repeated
// custom install conflicts instead of adding a row under a random ID. A copy
// the destination already holds under an alternate also wins once the base
// ID is free again.
func TestMCPInstallIDsStayStableWhenAlternatesAreSquatted(t *testing.T) {
	s, _ := newMCPCloneTestServer(t)
	key := []byte("key")
	if err := s.PutMCPServer(domain.MCPServerDefinition{ID: "role-source", Name: "Role MCP", Scope: "project", ProjectID: "source", Transport: "http", URL: "https://example.com/role"}); err != nil {
		t.Fatal(err)
	}
	base := mcpInstallID("clone-role-source", "project", "target")
	squat := func(id string) {
		t.Helper()
		if err := s.PutMCPServer(domain.MCPServerDefinition{ID: id, Name: "squatter", Scope: "personal", OwnerUserID: 9, Transport: "http", URL: "https://example.com/squat"}); err != nil {
			t.Fatal(err)
		}
	}
	squat(base)
	firstAlternate := destinationMCPInstallID(s, key, "clone-role-source", "project", "target")
	squat(firstAlternate)

	id := destinationMCPInstallID(s, key, "clone-role-source", "project", "target")
	if id == base || id == firstAlternate {
		t.Fatalf("install id = %q, want a free alternate", id)
	}
	if again := destinationMCPInstallID(s, key, "clone-role-source", "project", "target"); again != id {
		t.Fatalf("free alternate is not stable: %q then %q", id, again)
	}
	if err := s.PutMCPServer(domain.MCPServerDefinition{ID: id, Name: "copy", Scope: "project", ProjectID: "target", Transport: "http", URL: "https://example.com/role"}); err != nil {
		t.Fatal(err)
	}
	if again := destinationMCPInstallID(s, key, "clone-role-source", "project", "target"); again != id {
		t.Fatalf("repeated install = %q, want the destination's copy %q", again, id)
	}
	// The squatters go away: the existing copy still wins over the free base.
	for _, squatted := range []string{base, firstAlternate} {
		if err := s.DeleteMCPServer(squatted); err != nil {
			t.Fatal(err)
		}
	}
	if again := destinationMCPInstallID(s, key, "clone-role-source", "project", "target"); again != id {
		t.Fatalf("after squatters left = %q, want the destination's copy %q", again, id)
	}
}
