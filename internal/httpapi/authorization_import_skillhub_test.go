package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

// authorizationTestServer wires a full API server with authentication turned
// on, plus an owner/member/viewer/outsider/admin roster on project "auth-p".
func authorizationTestServer(t *testing.T) (*repo.Store, http.Handler) {
	t.Helper()
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "authorization-regression-test-secret")
	t.Setenv("AGENTFLOW_SCHEDULER_ENABLED", "false")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	users := map[string]domain.User{}
	for _, login := range []string{"auth-owner", "auth-member", "auth-viewer", "auth-outsider", "auth-admin"} {
		u := domain.User{LoginName: login}
		if login == "auth-admin" {
			u.PlatformRole = domain.PlatformRoleAdmin
		}
		if err := s.PutUser(u); err != nil {
			t.Fatal(err)
		}
		users[login], _ = s.UserByLoginName(login)
	}
	project := domain.Project{
		ID:      "auth-p",
		Name:    "授权项目",
		OwnerID: users["auth-owner"].ID,
		Members: []domain.ProjectMember{
			{UserID: users["auth-member"].ID, Role: domain.ProjectRoleMember},
			{UserID: users["auth-viewer"].ID, Role: domain.ProjectRoleViewer},
		},
	}
	if err := s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	return s, h
}

// authedStatus sends a request signed as user and returns only the status code,
// so a test can assert "not forbidden" without pinning an unrelated handler error.
func authedStatus(t *testing.T, h http.Handler, user domain.User, method, path string, body any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	token, err := auth.NewClientFromEnv().SignUser(&auth.User{ID: user.ID, LoginName: user.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func syncUser(t *testing.T, s *repo.Store, login string) domain.User {
	t.Helper()
	u, ok := s.UserByLoginName(login)
	if !ok {
		t.Fatalf("user %s not found", login)
	}
	return u
}

func TestImportTAPDRequiresProjectWriter(t *testing.T) {
	s, h := authorizationTestServer(t)
	payload := map[string]any{
		"projectId": "auth-p",
		"url":       "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100000666",
	}
	owner, member, viewer, outsider, admin := syncUser(t, s, "auth-owner"), syncUser(t, s, "auth-member"), syncUser(t, s, "auth-viewer"), syncUser(t, s, "auth-outsider"), syncUser(t, s, "auth-admin")

	// Viewers and non-members must not be able to import requirements.
	for _, user := range []domain.User{viewer, outsider} {
		if status := authedStatus(t, h, user, http.MethodPost, "/api/issues/import-tapd", payload); status != http.StatusForbidden {
			t.Fatalf("%s import-tapd status=%d, want 403", user.LoginName, status)
		}
	}
	// Owners, writable members and platform admins pass the authorization gate.
	// They then fail validation (no repository selected) with 400, not 403.
	for _, user := range []domain.User{owner, member, admin} {
		if status := authedStatus(t, h, user, http.MethodPost, "/api/issues/import-tapd", payload); status == http.StatusForbidden {
			t.Fatalf("%s import-tapd was forbidden but should be allowed past the writer gate", user.LoginName)
		}
	}
}

func TestSkillHubInstallRequiresProjectWriter(t *testing.T) {
	s, h := authorizationTestServer(t)
	payload := map[string]any{"slug": "demo-skill", "projectId": "auth-p"}
	member, viewer, outsider, admin := syncUser(t, s, "auth-member"), syncUser(t, s, "auth-viewer"), syncUser(t, s, "auth-outsider"), syncUser(t, s, "auth-admin")

	for _, user := range []domain.User{viewer, outsider} {
		if status := authedStatus(t, h, user, http.MethodPost, "/api/skillhub/install", payload); status != http.StatusForbidden {
			t.Fatalf("%s skillhub/install status=%d, want 403", user.LoginName, status)
		}
	}
	for _, user := range []domain.User{member, admin} {
		if status := authedStatus(t, h, user, http.MethodPost, "/api/skillhub/install", payload); status == http.StatusForbidden {
			t.Fatalf("%s skillhub/install was forbidden but should be allowed past the writer gate", user.LoginName)
		}
	}
}

func TestSkillHubUpdateRequiresProjectWriter(t *testing.T) {
	s, h := authorizationTestServer(t)
	if err := s.PutSkill(domain.SkillDefinition{
		ID: "hub-skill", Name: "Hub Skill", ProjectID: "auth-p",
		Source: &domain.SkillSource{Provider: domain.SkillSourceSkillHub, Namespace: "ns", Slug: "demo-skill", Version: "1.0.0"},
	}); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"skillId": "hub-skill", "version": "1.0.1"}
	member, viewer, outsider, admin := syncUser(t, s, "auth-member"), syncUser(t, s, "auth-viewer"), syncUser(t, s, "auth-outsider"), syncUser(t, s, "auth-admin")

	for _, user := range []domain.User{viewer, outsider} {
		if status := authedStatus(t, h, user, http.MethodPost, "/api/skillhub/update", payload); status != http.StatusForbidden {
			t.Fatalf("%s skillhub/update status=%d, want 403", user.LoginName, status)
		}
	}
	for _, user := range []domain.User{member, admin} {
		if status := authedStatus(t, h, user, http.MethodPost, "/api/skillhub/update", payload); status == http.StatusForbidden {
			t.Fatalf("%s skillhub/update was forbidden but should be allowed past the writer gate", user.LoginName)
		}
	}
}

func TestSkillHubUpdatePlatformSkillRequiresAdmin(t *testing.T) {
	s, h := authorizationTestServer(t)
	if err := s.PutSkill(domain.SkillDefinition{
		ID: "platform-hub-skill", Name: "Platform Hub Skill", ProjectID: "",
		Source: &domain.SkillSource{Provider: domain.SkillSourceSkillHub, Namespace: "ns", Slug: "demo-skill", Version: "1.0.0"},
	}); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"skillId": "platform-hub-skill", "version": "1.0.1"}
	outsider, admin := syncUser(t, s, "auth-outsider"), syncUser(t, s, "auth-admin")
	if status := authedStatus(t, h, outsider, http.MethodPost, "/api/skillhub/update", payload); status != http.StatusForbidden {
		t.Fatalf("outsider skillhub/update of platform skill status=%d, want 403", status)
	}
	if status := authedStatus(t, h, admin, http.MethodPost, "/api/skillhub/update", payload); status == http.StatusForbidden {
		t.Fatalf("admin skillhub/update of platform skill was forbidden but should be allowed")
	}
}
