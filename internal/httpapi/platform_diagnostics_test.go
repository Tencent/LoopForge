package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"agentflow/internal/domain"
)

// TestPlatformDiagnosticsAPI covers the read-only deployment diagnostics used
// by 平台管理 → 运维诊断: admins get checks, other members are rejected.
func TestPlatformDiagnosticsAPI(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	admin := cvBNewHTTPFixture(t)
	if _, err := admin.s.GrantPlatformAdmin(admin.user.LoginName); err != nil {
		t.Fatal(err)
	}
	member := admin
	user := domain.User{LoginName: "diagnostics-member"}
	if err := admin.s.PutUser(user); err != nil {
		t.Fatal(err)
	}
	member.user, _ = admin.s.UserByLoginName(user.LoginName)

	if resp := cvBRequest(t, member, http.MethodGet, "/api/platform/diagnostics", nil, true); resp.Code != http.StatusForbidden {
		t.Fatalf("member: HTTP %d, want 403 (%s)", resp.Code, resp.Body.String())
	}
	resp := cvBRequest(t, admin, http.MethodGet, "/api/platform/diagnostics", nil, true)
	if resp.Code != http.StatusOK {
		t.Fatalf("admin: HTTP %d (%s)", resp.Code, resp.Body.String())
	}
	var view platformDiagnosticsView
	if err := json.Unmarshal(resp.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.GeneratedAt == "" || len(view.Checks) == 0 {
		t.Fatalf("empty diagnostics view: %+v", view)
	}
	seen := map[string]bool{}
	for _, check := range view.Checks {
		seen[check.ID] = true
		if check.Title == "" {
			t.Fatalf("check %s has no title", check.ID)
		}
		switch check.Status {
		case diagnosticStatusOK, diagnosticStatusWarn, diagnosticStatusError, diagnosticStatusInfo:
		default:
			t.Fatalf("check %s has unexpected status %q", check.ID, check.Status)
		}
	}
	for _, want := range []string{"database.connection", "database.schema", "artifacts.size_limit", "browser.agent_browser", "runtime.docker"} {
		if !seen[want] {
			t.Fatalf("missing check %q in %v", want, seen)
		}
	}
}
