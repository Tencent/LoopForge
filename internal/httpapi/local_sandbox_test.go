package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

type localSandboxRunnerMap map[string]domain.Runner

func (m localSandboxRunnerMap) Runner(id string) (domain.Runner, bool) {
	runner, ok := m[id]
	return runner, ok
}

func TestLocalSandboxOwnerAndRunnerChecks(t *testing.T) {
	runners := localSandboxRunnerMap{
		"mine":  {ID: "mine", Name: "mac", OwnerUserID: 1, RunnerVersion: domain.RunnerVersionLocalWorktree},
		"old":   {ID: "old", Name: "old", OwnerUserID: 1, RunnerVersion: domain.RunnerVersionGitSSHKnownHosts},
		"other": {ID: "other", Name: "peer", OwnerUserID: 2, RunnerVersion: domain.RunnerVersionLocalWorktree},
	}
	local := func(runnerID string) domain.SandboxProfile {
		return domain.SandboxProfile{ID: "box", Driver: domain.SandboxDriverLocal, RunnerID: runnerID}
	}
	if err := localSandboxOwner(runners, local("mine"), 1); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"unbound":       localSandboxOwner(runners, local(""), 1),
		"missing":       localSandboxOwner(runners, local("gone"), 1),
		"other's":       localSandboxOwner(runners, local("other"), 1),
		"anonymous":     localSandboxOwner(runners, local("mine"), 0),
		"wrong machine": localSandboxRunner(runners, local("mine"), "other"),
		"old runner":    localSandboxRunner(runners, local("old"), "old"),
	} {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := localSandboxRunner(runners, local("mine"), "mine"); err != nil {
		t.Fatal(err)
	}
	native := domain.SandboxProfile{ID: "n", Driver: domain.SandboxDriverNative}
	if localSandboxOwner(runners, native, 0) != nil || localSandboxRunner(runners, native, "") != nil {
		t.Fatal("non-local sandboxes need no machine")
	}
}

// Only the owner of the bound machine saves, rebinds or deletes a local
// sandbox, and no SOP may select one.
func TestLocalSandboxCatalogBelongsToTheMachineOwner(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	for _, runner := range []domain.Runner{
		{ID: "mine", Name: "mac", OwnerUserID: f.user.ID, RunnerVersion: domain.RunnerVersionLocalWorktree},
		{ID: "other", Name: "peer", OwnerUserID: f.user.ID + 1, RunnerVersion: domain.RunnerVersionLocalWorktree},
	} {
		if err := f.s.PutRunner(runner); err != nil {
			t.Fatal(err)
		}
	}
	save := func(runnerID string) *http.Response {
		rec := cvBRequest(t, f, http.MethodPut, "/api/sandboxes", map[string]any{
			"id": "my-local", "name": "My machine", "projectId": "platform", "driver": "local", "runnerId": runnerID,
			"repositoryPaths": map[string]string{"repo-1": "~/src/service"},
		}, true)
		return rec.Result()
	}
	if res := save("other"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("binding another user's machine = %d", res.StatusCode)
	}
	if res := save("mine"); res.StatusCode != http.StatusOK {
		t.Fatalf("binding own machine = %d", res.StatusCode)
	}
	if profile, ok := f.s.Sandbox("my-local"); !ok || profile.RunnerID != "mine" || profile.RepositoryPaths["repo-1"] != "~/src/service" {
		t.Fatalf("stored local sandbox = %+v", profile)
	}
	rec := cvBRequest(t, f, http.MethodPut, "/api/workflows", map[string]any{
		"id": "local-sop", "name": "Local SOP", "projectId": "platform", "sandboxProfileId": "my-local", "nodes": []any{}, "edges": []any{},
	}, true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "SOP 不能使用 local") {
		t.Fatalf("SOP on local sandbox = %d %s", rec.Code, rec.Body.String())
	}

	profile, _ := f.s.Sandbox("my-local")
	profile.RunnerID = "other"
	if err := f.s.PutSandbox(profile); err != nil {
		t.Fatal(err)
	}
	if res := save("mine"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("taking over another user's local sandbox = %d", res.StatusCode)
	}
	if rec := cvBRequest(t, f, http.MethodDelete, "/api/sandboxes?id=my-local", nil, true); rec.Code != http.StatusForbidden {
		t.Fatalf("deleting another user's local sandbox = %d %s", rec.Code, rec.Body.String())
	}
	profile.RunnerID = "mine"
	if err := f.s.PutSandbox(profile); err != nil {
		t.Fatal(err)
	}
	if rec := cvBRequest(t, f, http.MethodDelete, "/api/sandboxes?id=my-local", nil, true); rec.Code != http.StatusNoContent {
		t.Fatalf("deleting own local sandbox = %d %s", rec.Code, rec.Body.String())
	}
}
