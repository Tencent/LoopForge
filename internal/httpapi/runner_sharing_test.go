package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestConversationRunnerDefaultSkipsMachinesThatOnlySupportSOPs(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	project := "runner-compatibility"
	if err := f.s.PutProject(domain.Project{ID: project, Name: "Runner compatibility", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	for _, runner := range []domain.Runner{
		{ID: "sop-only", OwnerUserID: f.user.ID, Status: domain.RunnerStatusOnline, RunnerVersion: "0.9.2"},
		{ID: "chat-ready", OwnerUserID: f.user.ID, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionConversationCLI},
	} {
		if err := f.s.PutRunner(runner); err != nil {
			t.Fatal(err)
		}
		if err := f.s.SetRunnerSharedProjects(runner.ID, f.user.ID, []string{project}); err != nil {
			t.Fatal(err)
		}
	}
	putDefault := func(path, id string) {
		t.Helper()
		rec := cvBRequest(t, f, http.MethodPut, path, map[string]string{"runnerId": id}, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("set %s: %d %s", id, rec.Code, rec.Body.String())
		}
	}
	path := "/api/runners/default?projectId=" + project
	check := func(path, expected string) {
		t.Helper()
		var row struct{ RunnerID string }
		rec := cvBRequest(t, f, http.MethodGet, path, nil, true)
		if err := json.Unmarshal(rec.Body.Bytes(), &row); err != nil || rec.Code != http.StatusOK || row.RunnerID != expected {
			t.Errorf("%s: got %d %s, want runner %q", path, rec.Code, rec.Body.String(), expected)
		}
	}
	putDefault(path, "sop-only")
	putDefault("/api/runners/default", "chat-ready")
	check(path, "sop-only")
	check(path+"&conversation=1", "chat-ready")
	rec := cvBRequest(t, f, http.MethodGet, "/api/runners?projectId="+project, nil, true)
	var rows []struct {
		ID                   string
		SupportsConversation *bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || rec.Code != http.StatusOK || len(rows) != 2 {
		t.Fatalf("runner list: %d %s", rec.Code, rec.Body.String())
	}
	for _, row := range rows {
		if row.SupportsConversation == nil || *row.SupportsConversation != (row.ID == "chat-ready") {
			t.Errorf("wrong chat capability in runner list: %s", rec.Body.String())
		}
	}
	if err := f.s.SetRunnerSharedProjects("chat-ready", f.user.ID, nil); err != nil {
		t.Fatal(err)
	}
	check(path, "sop-only")
	check(path+"&conversation=1", "")
	putDefault("/api/runners/default", "sop-only")
	check("/api/runners/default?conversation=1", "")
}

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
		{ID: "unassigned", Name: "Unassigned", OwnerID: owner.user.ID},
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
		var rows []repo.RunnerProjectView
		if err := json.Unmarshal(check(member, http.MethodGet, "/api/runners"+tc.query, nil, 200), &rows); err != nil {
			t.Fatal(err)
		}
		if !tc.visible && len(rows) != 0 {
			t.Fatalf("%s exposed %+v", tc.query, rows)
		}
		if tc.visible && (len(rows) != 1 || rows[0].ID != runner.ID || !rows[0].CanUseInProject || len(rows[0].SharedProjectIDs) != 0 ||
			len(rows[0].Runtimes) != 1 || rows[0].Runtimes[0].BinaryPath != "" || rows[0].Runtimes[0].AuthMode != "personal") {
			t.Fatalf("%s returned an invalid shared machine view: %+v", tc.query, rows)
		}
	}
	for _, project := range []string{"one", "other", "unassigned"} {
		var rows []repo.RunnerProjectView
		if err := json.Unmarshal(check(owner, http.MethodGet, "/api/runners?projectId="+project, nil, 200), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].ID != runner.ID || rows[0].CanUseInProject != (project == "one") {
			t.Fatalf("owner's management view for %s: %+v", project, rows)
		}
	}
	selection := map[string]string{"runnerId": runner.ID}
	check(owner, http.MethodPut, "/api/runners/default", selection, 200)
	checkDefault := func(f cvBHTTPFixture, project, effective, personal, projectDefault string) {
		t.Helper()
		path := "/api/runners/default"
		if project != "" {
			path += "?projectId=" + project
		}
		var result struct {
			RunnerID         string        `json:"runnerId"`
			Runner           domain.Runner `json:"runner"`
			PersonalRunnerID string        `json:"personalRunnerId"`
			ProjectRunnerID  string        `json:"projectRunnerId"`
			Online           bool          `json:"online"`
		}
		body := check(f, http.MethodGet, path, nil, 200)
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		if result.RunnerID != effective || result.Runner.ID != effective || result.PersonalRunnerID != personal ||
			result.ProjectRunnerID != projectDefault || result.Online != (effective != "") {
			t.Fatalf("default for %q: %s, want effective=%q personal=%q project=%q", project, body, effective, personal, projectDefault)
		}
	}
	checkDefault(owner, "", runner.ID, "", "")
	checkDefault(owner, "one", runner.ID, runner.ID, "")
	checkDefault(owner, "unassigned", "", runner.ID, "")
	check(member, http.MethodPut, "/api/runners/default", selection, 403)
	check(member, http.MethodPut, "/api/runners/default?projectId=other", selection, 403)
	check(member, http.MethodPut, "/api/runners/default?projectId=read-only", selection, 403)
	check(member, http.MethodPut, "/api/runners/default?projectId=one", selection, 200)
	checkDefault(member, "one", runner.ID, "", runner.ID)
	if got := owner.s.ResolveRunnerDefaults(member.user.ID, "one").Runner.ID; got != runner.ID {
		t.Fatalf("selected project default = %q", got)
	}
	if got := owner.s.ResolveRunnerDefaults(member.user.ID, "two").Runner.ID; got != "" {
		t.Fatalf("default leaked to another project: %q", got)
	}

	draft := func(project string) map[string]any {
		return map[string]any{"projectId": project, "executionTarget": domain.AssistantExecutionTarget{
			Pool: domain.ExecutionModeRunner, RunnerID: runner.ID,
		}}
	}
	check(owner, http.MethodPut, "/api/assistant/sessions/owner-runner-unassigned", draft("unassigned"), 400)
	check(owner, http.MethodPut, "/api/assistant/sessions/owner-runner-personal", draft(""), 200)
	check(owner, http.MethodPut, "/api/assistant/sessions/owner-runner-one", draft("one"), 200)
	check(member, http.MethodPut, "/api/assistant/sessions/shared-runner-one", draft("one"), 200)
	check(member, http.MethodPut, "/api/assistant/sessions/shared-runner-other", draft("other"), 400)
	check(member, http.MethodPut, "/api/assistant/sessions/shared-runner-personal", draft(""), 400)
	check(member, http.MethodPut, "/api/runners/"+runner.ID+"/run-limit", map[string]int{"runLimit": 2}, 403)

	check(owner, http.MethodPut, sharing, grant("two"), 200)
	checkDefault(owner, "one", "", runner.ID, "")
	checkDefault(owner, "two", runner.ID, runner.ID, "")
	check(owner, http.MethodPut, "/api/assistant/sessions/owner-runner-one", draft(""), 400)
	staleDraft := assistantRequest{
		SessionID: "owner-runner-one", ProjectID: "one",
		Messages: []assistantMessage{{Role: "user", Content: "hello"}},
	}
	for _, path := range []string{"/api/assistant/chat", "/api/assistant/generations"} {
		var outcome struct{ Error string }
		if err := json.Unmarshal(check(owner, http.MethodPost, path, staleDraft, 400), &outcome); err != nil {
			t.Fatal(err)
		}
		if outcome.Error != "无权使用此执行机器" {
			t.Fatalf("%s returned %q for a revoked draft machine", path, outcome.Error)
		}
	}
	check(member, http.MethodPut, "/api/assistant/sessions/shared-runner-revoked", draft("one"), 400)
	check(member, http.MethodPut, "/api/assistant/sessions/shared-runner-two", draft("two"), 200)
	if got := owner.s.ResolveRunnerDefaults(member.user.ID, "one").Runner.ID; got != "" {
		t.Fatalf("revoked project default = %q", got)
	}

	check(member, http.MethodPut, "/api/runners/default?projectId=two", selection, 200)
	checkDefault(member, "two", runner.ID, "", runner.ID)
	for _, members := range [][]domain.ProjectMember{
		{{UserID: owner.user.ID, Role: "viewer"}},
		{},
	} {
		project, ok := owner.s.Project("two")
		if !ok {
			t.Fatal("project two not found")
		}
		project.Members = members
		if err := owner.s.PutProject(project); err != nil {
			t.Fatal(err)
		}
		var rows []repo.RunnerProjectView
		if err := json.Unmarshal(check(member, http.MethodGet, "/api/runners?projectId=two", nil, 200), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatalf("project with machine owner membership %+v exposed %+v", members, rows)
		}
		checkDefault(member, "two", "", "", "")
		check(member, http.MethodPut, "/api/runners/default?projectId=two", selection, 403)
		check(member, http.MethodPut, "/api/assistant/sessions/shared-runner-owner-revoked", draft("two"), 400)
	}
	checkDefault(owner, "", runner.ID, "", "")

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
