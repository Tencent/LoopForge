package httpapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestProjectEnvironmentAssetsIncludeSharedGlobalDefaults(t *testing.T) {
	profiles := []domain.SandboxProfile{
		{ID: "own", ProjectID: "project"},
		{ID: "foreign-shared", ProjectID: "other", Shared: true},
		{ID: "foreign-private", ProjectID: "other"},
		{ID: "global-shared", Shared: true},
		{ID: "global-private"},
	}
	visible := filterEnvironmentAssets(profiles, "project", func(profile domain.SandboxProfile) (string, bool) {
		return profile.ProjectID, profile.Shared
	})
	var ids []string
	for _, profile := range visible {
		ids = append(ids, profile.ID)
	}
	if !reflect.DeepEqual(ids, []string{"own", "foreign-shared", "global-shared"}) {
		t.Fatalf("incorrect project visibility: %v", ids)
	}
}

func TestDefaultSandboxWithoutCatalogRowsSupportsSOPCreation(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	if err := f.s.DB.Exec("DELETE FROM sandboxes").Error; err != nil {
		t.Fatal(err)
	}
	const projectID = "builtin-default-project"
	if err := f.s.PutProject(domain.Project{ID: projectID, Name: "Default sandbox check", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	bootstrap := cvBRequest(t, f, http.MethodGet, "/api/bootstrap", nil, true)
	var catalog struct {
		Sandboxes []domain.SandboxProfile `json:"sandboxes"`
	}
	if bootstrap.Code != http.StatusOK || json.Unmarshal(bootstrap.Body.Bytes(), &catalog) != nil || len(catalog.Sandboxes) != 1 || catalog.Sandboxes[0].ID != repo.GenericDockerSandboxID {
		t.Fatalf("bootstrap does not expose the default: %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	listed := cvBRequest(t, f, http.MethodGet, "/api/sandboxes?projectId="+projectID, nil, true)
	var profiles []domain.SandboxProfile
	if listed.Code != http.StatusOK || json.Unmarshal(listed.Body.Bytes(), &profiles) != nil || len(profiles) != 1 || profiles[0].ID != repo.GenericDockerSandboxID {
		t.Fatalf("project cannot select the default: %d %s", listed.Code, listed.Body.String())
	}
	created := cvBRequest(t, f, http.MethodPut, "/api/workflows", map[string]any{
		"id": "builtin-default-sop", "name": "Blank SOP", "projectId": projectID,
		"status": "draft", "sandboxProfileId": repo.GenericDockerSandboxID, "nodes": []any{}, "edges": []any{},
	}, true)
	if created.Code != http.StatusOK {
		t.Fatalf("blank SOP creation failed: %d %s", created.Code, created.Body.String())
	}
	var count int64
	if err := f.s.DB.Table("sandboxes").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("API requests seeded rows: rows=%d err=%v", count, err)
	}
}

func TestOnlyPlatformAdminsMayWriteDefaultSandbox(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	f := cvBNewHTTPFixture(t)
	if err := f.s.PutProject(domain.Project{ID: "platform", Name: "Named platform", OwnerID: f.user.ID}); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"id": "GENERIC-DOCKER", "name": "Project override", "projectId": "platform", "driver": "native", "shared": true,
	}
	rec := cvBRequest(t, f, http.MethodPut, "/api/sandboxes", body, true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("project owner could modify the platform default: %d %s", rec.Code, rec.Body.String())
	}
	if err := f.s.DB.Exec("DELETE FROM projects WHERE id = ?", "platform").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.s.DB.Exec("DELETE FROM sandboxes WHERE id = ?", repo.DefaultSandboxProfileID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GrantPlatformAdmin(f.user.LoginName); err != nil {
		t.Fatal(err)
	}
	for _, projectID := range []string{"platform", ""} {
		body["projectId"] = projectID
		rec = cvBRequest(t, f, http.MethodPut, "/api/sandboxes", body, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("platform admin could not configure the default with projectId %q: %d %s", projectID, rec.Code, rec.Body.String())
		}
	}
	if profile, ok := f.s.Sandbox(repo.DefaultSandboxProfileID); !ok || profile.Driver != domain.SandboxDriverNative {
		t.Fatalf("platform override was not saved under the canonical ID: %+v", profile)
	}
}
