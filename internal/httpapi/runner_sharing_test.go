package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"agentflow/internal/domain"
)

func TestRunnerProjectSharing(t *testing.T) {
	owner := cvBNewHTTPFixture(t)
	member := owner
	member.user = domain.User{ID: 91, LoginName: "runner-share-member"}
	if err := owner.s.PutUser(member.user); err != nil {
		t.Fatal(err)
	}
	for _, project := range []domain.Project{
		{ID: "one", Name: "One", OwnerID: owner.user.ID, Members: []domain.ProjectMember{{UserID: member.user.ID, Role: "member"}}},
		{ID: "two", Name: "Two", OwnerID: member.user.ID, Members: []domain.ProjectMember{{UserID: owner.user.ID, Role: "maintainer"}}},
		{ID: "other", Name: "Other", OwnerID: member.user.ID},
		{ID: "read-only", Name: "Read only", OwnerID: owner.user.ID, Members: []domain.ProjectMember{{UserID: member.user.ID, Role: "viewer"}}},
	} {
		if err := owner.s.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	runner := domain.Runner{
		ID: "shared-runner", Name: "Shared runner", OwnerUserID: owner.user.ID,
		Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionConversationCLI,
		Runtimes: []domain.RunnerRuntime{{Provider: "codex", BinaryPath: "/private/bin/codex", Status: "online", AuthMode: "local"}},
	}
	if err := owner.s.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	check := func(f cvBHTTPFixture, method, path string, body any, status int) []byte {
		t.Helper()
		rec := cvBRequest(t, f, method, path, body, true)
		if rec.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, rec.Code, status, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	sharing := "/api/runners/" + runner.ID + "/shared-projects"
	grant := func(ids ...string) map[string]any { return map[string]any{"projectIds": ids} }
	check(member, http.MethodPut, sharing, grant("one"), 403)
	check(owner, http.MethodPut, sharing, grant("other"), 403)
	check(owner, http.MethodPut, sharing, grant("read-only", "one", "two"), 200)

	for _, tc := range []struct {
		query   string
		visible bool
	}{
		{"?projectId=one", true}, {"?projectId=two", true}, {"?projectId=other", false},
		{"?projectId=read-only", false}, {"?mine=1&projectId=one", false}, {"", false},
	} {
		var rows []domain.Runner
		if err := json.Unmarshal(check(member, http.MethodGet, "/api/runners"+tc.query, nil, 200), &rows); err != nil {
			t.Fatal(err)
		}
		if !tc.visible && len(rows) != 0 {
			t.Fatalf("%s exposed %+v", tc.query, rows)
		}
		if tc.visible && (len(rows) != 1 || rows[0].ID != runner.ID || len(rows[0].SharedProjectIDs) != 0 ||
			len(rows[0].Runtimes) != 1 || rows[0].Runtimes[0].BinaryPath != "" || rows[0].Runtimes[0].AuthMode != "personal") {
			t.Fatalf("%s returned an invalid shared machine view: %+v", tc.query, rows)
		}
	}
	selection := map[string]string{"runnerId": runner.ID}
	check(member, http.MethodPut, "/api/runners/default", selection, 403)
	check(member, http.MethodPut, "/api/runners/default?projectId=other", selection, 403)
	check(member, http.MethodPut, "/api/runners/default?projectId=read-only", selection, 403)
	check(member, http.MethodPut, "/api/runners/default?projectId=one", selection, 200)
	if got := owner.s.DefaultRunnerForProject(member.user.ID, "one"); got != runner.ID {
		t.Fatalf("selected project default = %q", got)
	}
	if got := owner.s.DefaultRunnerForProject(member.user.ID, "two"); got != "" {
		t.Fatalf("default leaked to another project: %q", got)
	}

	draft := func(project string) map[string]any {
		return map[string]any{"projectId": project, "executionTarget": domain.AssistantExecutionTarget{
			Pool: domain.ExecutionModeRunner, RunnerID: runner.ID,
		}}
	}
	check(member, http.MethodPut, "/api/assistant/sessions/shared-runner-one", draft("one"), 200)
	check(member, http.MethodPut, "/api/assistant/sessions/shared-runner-other", draft("other"), 400)
	check(member, http.MethodPut, "/api/assistant/sessions/shared-runner-personal", draft(""), 400)
	check(member, http.MethodPut, "/api/runners/"+runner.ID+"/run-limit", map[string]int{"runLimit": 2}, 403)

	check(owner, http.MethodPut, sharing, grant("two"), 200)
	check(member, http.MethodPut, "/api/assistant/sessions/shared-runner-revoked", draft("one"), 400)
	check(member, http.MethodPut, "/api/assistant/sessions/shared-runner-two", draft("two"), 200)
	if got := owner.s.DefaultRunnerForProject(member.user.ID, "one"); got != "" {
		t.Fatalf("revoked project default = %q", got)
	}

	register := func(f cvBHTTPFixture) string {
		t.Helper()
		var code struct{ Code string }
		if err := json.Unmarshal(check(f, http.MethodPost, "/api/runners/bind-codes", nil, 200), &code); err != nil {
			t.Fatal(err)
		}
		rec := requestWithHeaders(t, f.h, http.MethodPost, "/api/runners/register", selection,
			map[string]string{"X-Bootstrap-Token": code.Code})
		var result struct{ RunnerID string }
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != 200 || result.RunnerID == "" {
			t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
		}
		return result.RunnerID
	}
	if got := register(member); got == runner.ID {
		t.Fatal("another project member re-registered the machine's existing ID")
	}
	if got := register(owner); got != runner.ID {
		t.Fatalf("owner re-registered as %s, want %s", got, runner.ID)
	}
	if stored, ok := owner.s.Runner(runner.ID); !ok || !stored.SharedWithProject("two") || stored.SharedWithProject("one") {
		t.Fatalf("re-registration changed the grants: %+v", stored)
	}
}
