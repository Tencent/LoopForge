package engine

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

func TestSOPScriptUsesProjectCredentials(t *testing.T) {
	if !runtime.NativeSandboxAvailable(domain.SandboxDriverNative) {
		t.Skip("native sandbox unavailable")
	}
	t.Setenv("AGENTFLOW_PROJECT_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.PutUser(domain.User{LoginName: "credential-owner"}); err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByLoginName("credential-owner")
	project := domain.Project{ID: "credential-project", Name: "Credential project", OwnerID: user.ID}
	if err := st.PutProject(project); err != nil {
		t.Fatal(err)
	}
	const credentialID = "registry-token"
	secret := "fixture-private-value"
	if _, err := st.SaveProjectCredential(t.Context(), project.ID, credentialID, true, domain.ProjectCredentialInput{
		Name: "Registry token", Kind: domain.ProjectCredentialToken,
		Fields: []domain.ProjectCredentialVariableInput{{Name: "PROJECT_TEST_TOKEN", Value: &secret}},
	}); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{
		ID: "credential-sop", Name: "Credential SOP", ProjectID: project.ID, SandboxProfileID: repo.TestNativeSandboxID,
		ProjectCredentialIDs: []string{credentialID},
		Nodes: []domain.WorkflowNode{{ID: "script", Name: "Script", Type: "script", MaxAttempts: 1,
			Script: `test -n "$PROJECT_TEST_TOKEN" && printf '%s\n' "$PROJECT_TEST_TOKEN"`}},
	}
	if err := st.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	eng := New(st, runtime.NewManagerWithLimit(t.TempDir(), 1))
	scope := domain.TelemetryContext{ProjectID: project.ID, ResponsibleUserID: user.ID}
	run, err := eng.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, st, run.ID, "succeeded")
	finished, _ := st.Run(run.ID)
	saved, _ := json.Marshal(finished)
	if !strings.Contains(finished.Nodes["script"].Output, "[redacted]") || strings.Contains(string(saved), secret) {
		t.Fatal("script did not read the credential with redacted output")
	}
	scope.ProjectID = "different-project"
	if _, err := eng.StartWithExecutionContext(wf.ID, "", domain.RepositoryContext{}, scope); err == nil {
		t.Fatal("a SOP resolved credentials outside its project")
	}
}
