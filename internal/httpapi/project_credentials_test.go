package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"github.com/gin-gonic/gin"
)

func TestProjectCredentialsHTTPAndSessionBinding(t *testing.T) {
	t.Setenv("AGENTFLOW_PROJECT_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	h, owner, session := chatSandboxFixture(t)
	handlers := &projectCredentialsHandlers{deps: h.deps}
	if err := h.repo.PutUser(domain.User{LoginName: "unrelated-project-owner"}); err != nil {
		t.Fatal(err)
	}
	other, _ := h.repo.UserByLoginName("unrelated-project-owner")
	if err := h.repo.PutProject(domain.Project{ID: "unrelated", Name: "Unrelated", OwnerID: other.ID}); err != nil {
		t.Fatal(err)
	}
	const secret = "test-only-project-credential"
	save := func(user domain.User, project string) (domain.ProjectCredential, int) {
		input := bytes.NewBufferString(`{"name":"Artifact registry","kind":"token","fields":[{"name":"ARTIFACT_TOKEN","value":"` + secret + `"}]}`)
		c, rec := workbenchContext(user, "POST", "/", input)
		c.Params = gin.Params{{Key: "id", Value: project}}
		handlers.save(c)
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatal("credential value returned from save")
		}
		var result domain.ProjectCredential
		_ = json.Unmarshal(rec.Body.Bytes(), &result)
		return result, rec.Code
	}
	credential, code := save(owner, session.ProjectID)
	if code != http.StatusCreated || credential.ID == "" {
		t.Fatalf("owner create: %d", code)
	}
	if _, code := save(other, session.ProjectID); code != http.StatusForbidden {
		t.Fatalf("cross-project create: %d", code)
	}
	foreign, code := save(other, "unrelated")
	if code != http.StatusCreated {
		t.Fatalf("other project create: %d", code)
	}
	for _, actor := range []domain.User{owner, other} {
		c, rec := workbenchContext(actor, "GET", "/", &bytes.Buffer{})
		c.Params = gin.Params{{Key: "id", Value: session.ProjectID}}
		handlers.list(c)
		if actor.ID == owner.ID && (rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), secret)) {
			t.Fatalf("owner list: %d", rec.Code)
		}
		if actor.ID == other.ID && rec.Code != http.StatusForbidden {
			t.Fatalf("cross-project list: %d", rec.Code)
		}
	}
	bind := func(ids ...string) int {
		data, _ := json.Marshal(map[string][]string{"projectCredentialIds": ids})
		c, rec := workbenchContext(owner, "PUT", "/", bytes.NewBuffer(data))
		c.Params = gin.Params{{Key: "id", Value: session.ChatSessionID}}
		h.sessionProjectCredentials(c)
		return rec.Code
	}
	if code := bind(foreign.ID); code != http.StatusBadRequest {
		t.Fatalf("cross-project binding: %d", code)
	}
	if code := bind(credential.ID); code != http.StatusOK {
		t.Fatalf("bind credential: %d", code)
	}
	// Transcript updates must preserve the server-owned selection.
	if err := h.repo.PutAssistantSession(owner.ID, domain.AssistantSession{
		ID: session.ChatSessionID, ProjectID: session.ProjectID, Title: "Renamed",
	}); err != nil {
		t.Fatal(err)
	}
	stored, _ := h.repo.AssistantSession(owner.ID, session.ChatSessionID)
	if len(stored.ProjectCredentialIDs) != 1 || stored.ProjectCredentialIDs[0] != credential.ID {
		t.Fatal("session lost the credential selection")
	}
	if err := h.repo.BeginAssistantGeneration(domain.AssistantGeneration{
		ID: "project-credential-generation", UserID: owner.ID, SessionID: session.ChatSessionID, Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if code := bind([]string{}...); code != http.StatusConflict {
		t.Fatalf("changing credentials during generation: %d", code)
	}
}

func TestSessionOwnerCanClearProjectCredentialsAfterLosingAccess(t *testing.T) {
	t.Setenv("AGENTFLOW_PROJECT_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	h, owner := workbenchFixture(t)
	if err := h.repo.PutUser(domain.User{LoginName: "credential-session-member"}); err != nil {
		t.Fatal(err)
	}
	member, _ := h.repo.UserByLoginName("credential-session-member")
	const projectID = "workbench"
	const credentialID = "registry"
	value := "fixture-token"
	if _, err := h.repo.SaveProjectCredential(t.Context(), projectID, credentialID, true, domain.ProjectCredentialInput{
		Name: "Registry", Kind: domain.ProjectCredentialToken,
		Fields: []domain.ProjectCredentialVariableInput{{Name: "SERVICE_TOKEN", Value: &value}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"viewer", "removed"} {
		t.Run(role, func(t *testing.T) {
			project, _ := h.repo.Project(projectID)
			project.Members = []domain.ProjectMember{{UserID: member.ID, Role: "member"}}
			if err := h.repo.PutProject(project); err != nil {
				t.Fatal(err)
			}
			session := domain.AssistantSession{ID: "credential-chat-" + role, ProjectID: project.ID}
			if err := h.repo.PutAssistantSession(member.ID, session); err != nil {
				t.Fatal(err)
			}
			bind := func(user domain.User, ids []string) int {
				data, _ := json.Marshal(map[string][]string{"projectCredentialIds": ids})
				c, rec := workbenchContext(user, http.MethodPut, "/", bytes.NewBuffer(data))
				c.Params = gin.Params{{Key: "id", Value: session.ID}}
				h.sessionProjectCredentials(c)
				return rec.Code
			}
			if code := bind(member, []string{credentialID}); code != http.StatusOK {
				t.Fatalf("bind as member: %d", code)
			}
			project, _ = h.repo.Project(projectID)
			project.Members = []domain.ProjectMember{}
			if role == "viewer" {
				project.Members = []domain.ProjectMember{{UserID: member.ID, Role: role}}
			}
			if err := h.repo.PutProject(project); err != nil {
				t.Fatal(err)
			}
			if code := bind(member, []string{credentialID}); code != http.StatusForbidden {
				t.Fatalf("binding without permission: %d", code)
			}
			if code := bind(owner, []string{}); code != http.StatusNotFound {
				t.Fatalf("clear another user's session: %d", code)
			}
			if code := bind(member, []string{}); code != http.StatusOK {
				t.Fatalf("clear own session without project credential access: %d", code)
			}
			stored, _ := h.repo.AssistantSession(member.ID, session.ID)
			if len(stored.ProjectCredentialIDs) != 0 {
				t.Fatal("credential binding was not cleared")
			}
		})
	}
}

func TestProjectCredentialsRunnerClaimScope(t *testing.T) {
	t.Setenv("AGENTFLOW_PROJECT_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	h, user, session := chatSandboxFixture(t)
	secret := "test-only-runner-credential"
	credential, err := h.repo.SaveProjectCredential(t.Context(), session.ProjectID, "test-credential", true, domain.ProjectCredentialInput{
		Name: "Release", Kind: domain.ProjectCredentialToken,
		Fields: []domain.ProjectCredentialVariableInput{{Name: "RELEASE_TOKEN", Value: &secret}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := domain.Runner{ID: "credential-runner", RunnerVersion: domain.RunnerVersionProjectCredentials}
	run := domain.Run{ID: "credential-run", Status: "running",
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "credential-sop", ProjectID: session.ProjectID, ProjectCredentialIDs: []string{credential.ID}},
		ExecutionContext: domain.TelemetryContext{ProjectID: session.ProjectID, ResponsibleUserID: user.ID, ExecutionRunnerID: runner.ID},
	}
	if err := h.repo.PutRun(run); err != nil {
		t.Fatal(err)
	}
	task := domain.RunnerTask{ID: "credential-task", RunID: run.ID, RunnerID: runner.ID, LeaseOwner: runner.ID, Provider: "shell", Status: domain.RunnerTaskRunning}
	task.ProjectCredentialIDs = []string{credential.ID}
	runners := &runnersHandlers{deps: h.deps}
	env, err := runners.runnerTaskProjectCredentialEnv(t.Context(), task, runner)
	if err != nil || env["RELEASE_TOKEN"] != secret {
		t.Fatalf("authorized runner claim: %v", err)
	}
	task.ProjectCredentialEnv = env
	if err := h.repo.PutRunnerTask(task); err != nil {
		t.Fatal(err)
	}
	stored, found := h.repo.RunnerTask(task.ID)
	if !found || len(stored.ProjectCredentialEnv) != 0 || len(stored.ProjectCredentialIDs) != 1 {
		t.Fatal("runner payload persisted credentials")
	}
	for _, runner := range []domain.Runner{
		{ID: "unrelated-runner", RunnerVersion: domain.RunnerVersionProjectCredentials},
		{ID: runner.ID, RunnerVersion: domain.RunnerVersionRepositoryCache},
	} {
		if _, err := runners.runnerTaskProjectCredentialEnv(t.Context(), stored, runner); err == nil {
			t.Fatal("unauthorized or incompatible runner received credentials")
		}
	}
}
