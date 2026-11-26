package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/runtime"
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

func TestConversationContinuesWhenProjectCredentialsChange(t *testing.T) {
	t.Setenv("AGENTFLOW_PROJECT_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	h, user, target, workspaceID := workspaceStartupFixture(t, domain.RunnerVersionProjectCredentials)
	session := domain.AssistantSession{ID: "credential-change", ProjectID: "workbench", WorkspaceRunID: workspaceID, ExecutionTarget: &target}
	if err := h.repo.PutUser(domain.User{LoginName: "credential-project-owner"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := h.repo.UserByLoginName("credential-project-owner")
	setRole := func(t *testing.T, role string) {
		t.Helper()
		project, _ := h.repo.Project(session.ProjectID)
		project.OwnerID = owner.ID
		project.Members = []domain.ProjectMember{{UserID: user.ID, Role: role}}
		if err := h.repo.PutProject(project); err != nil {
			t.Fatal(err)
		}
	}
	setRole(t, "member")
	// A platform Runner stays available after the member loses the credential grant.
	runner, _ := h.repo.Runner(target.RunnerID)
	runner.OwnerUserID = 0
	if err := h.repo.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	save := func(id, name, value string, create, disabled bool) {
		t.Helper()
		if _, err := h.repo.SaveProjectCredential(t.Context(), session.ProjectID, id, create, domain.ProjectCredentialInput{
			Name: id, Kind: domain.ProjectCredentialToken, Disabled: disabled,
			Fields: []domain.ProjectCredentialVariableInput{{Name: name, Value: &value}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	save("rotated", "ROTATED_TOKEN", "fixture-original", true, false)
	save("deleted", "DELETED_TOKEN", "fixture-deleted", true, false)
	save("disabled", "DISABLED_TOKEN", "fixture-disabled", true, false)
	// This binding was removed before the deployment ever observed its name.
	session.ProjectCredentialIDs = []string{"rotated", "deleted", "disabled", "legacy-missing"}
	if err := h.repo.PutAssistantSession(user.ID, session); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetAssistantRuntimeMode(user.ID, session.ID, session.ProjectID, domain.AssistantModeWorkspace); err != nil {
		t.Fatal(err)
	}
	starts := make(chan runtime.ConversationCommand, 4)
	prompts := make(chan string, 4)
	var input strings.Builder
	var inputComplete bool
	var rejectNextResume atomic.Bool
	var rejectedResume bool
	fakeRunnerConnection(t, h.runnerControls, target.RunnerID, func(envelope domain.RunnerWorkspaceRequest) domain.RunnerWorkspaceResponse {
		var req runtime.ConversationProcessRequest
		if err := json.Unmarshal(envelope.Conversation, &req); err != nil {
			return domain.RunnerWorkspaceResponse{Status: http.StatusBadRequest, Error: err.Error()}
		}
		result := runtime.ConversationProcessResult{ID: req.ID, Root: req.SourceRoot, PromptInputFiles: true}
		switch req.Operation {
		case "start":
			if envelope.RunID != workspaceID || req.Command == nil {
				return domain.RunnerWorkspaceResponse{Status: http.StatusBadRequest, Error: "unexpected workspace or command"}
			}
			input.Reset()
			inputComplete = false
			rejectedResume = slices.Contains(req.Command.Argv, "--resume") && rejectNextResume.CompareAndSwap(true, false)
			if !rejectedResume {
				starts <- *req.Command
			}
		case "write":
			input.WriteString(req.Data)
			if req.EOF {
				if !rejectedResume {
					prompts <- input.String()
				}
				inputComplete = true
			}
		case "poll":
			if inputComplete {
				result.Done = true
				result.Sequence = 1
				if !rejectedResume {
					result.Events = []domain.TelemetryEvent{{Name: "process.stdout", Message: `{"type":"result","result":"你好。"}`}}
				}
			}
		}
		payload, _ := json.Marshal(result)
		return domain.RunnerWorkspaceResponse{Status: http.StatusOK, Payload: payload}
	})
	turn := func(t *testing.T, wantEnv map[string]string, wantNote ...string) string {
		t.Helper()
		old, _ := h.repo.AssistantSession(user.ID, session.ID)
		messages := old.Messages
		if len(messages) == 0 || messages[len(messages)-1].Role != "user" {
			messages = append(messages, assistantMessage{Role: "user", Content: "你好"})
		}
		body, err := json.Marshal(assistantRequest{
			ProjectID: session.ProjectID, SessionID: session.ID,
			Messages: messages,
		})
		if err != nil {
			t.Fatal(err)
		}
		c, rec := workbenchContext(user, http.MethodPost, "/api/assistant/generations", bytes.NewBuffer(body))
		h.startGeneration(c)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
		}
		g := waitGeneration(t, h, user.ID, session.ID, func(g domain.AssistantGeneration) bool { return g.Status != "running" }, 10*time.Second)
		if g.Status != "completed" || !strings.Contains(string(g.Reply), "你好。") {
			t.Fatalf("credential change prevented a reply: %s (%s)", g.Status, g.Error)
		}
		var command runtime.ConversationCommand
		select {
		case command = <-starts:
		default:
			t.Fatal("no workspace CLI invocation")
		}
		if !maps.Equal(command.ProjectCredentialEnv, wantEnv) {
			t.Fatal("workspace CLI received the wrong credential environment")
		}
		var prompt string
		select {
		case prompt = <-prompts:
		default:
			t.Fatal("workspace CLI did not receive turn input")
		}
		for _, note := range wantNote {
			if !strings.Contains(prompt, note) {
				t.Errorf("missing internal prompt note %q", note)
			}
		}
		saved, _ := h.repo.AssistantSession(user.ID, session.ID)
		public, _ := json.Marshal(saved)
		if len(saved.Messages) != len(messages)+1 || bytes.Contains(public, []byte("projectCredentialObservations")) ||
			bytes.Contains(public, []byte("本轮不可用")) || bytes.Contains(public, []byte("fixture-")) {
			t.Fatal("credential context or values leaked into the transcript or public session")
		}
		return prompt
	}
	quietTurn := func(t *testing.T, wantEnv map[string]string) {
		t.Helper()
		if prompt := turn(t, wantEnv); strings.Contains(prompt, "项目凭据") {
			t.Error("unchanged credentials were announced again")
		}
	}
	editLastTurn := func(t *testing.T) {
		t.Helper()
		old, _ := h.repo.AssistantSession(user.ID, session.ID)
		if err := h.repo.ReplaceAssistantTurn(old, len(old.Messages)-2, assistantMessage{Role: "user", Content: "你好，编辑后重发"}); err != nil {
			t.Fatal(err)
		}
	}
	initialEnv := map[string]string{"ROTATED_TOKEN": "fixture-original", "DELETED_TOKEN": "fixture-deleted", "DISABLED_TOKEN": "fixture-disabled"}
	const initialVars = "DELETED_TOKEN、DISABLED_TOKEN、ROTATED_TOKEN"
	turn(t, initialEnv, initialVars, "有已绑定的项目凭据本轮不可用", "继续处理用户请求")
	quietTurn(t, initialEnv)
	t.Run("fresh CLI after editing a turn", func(t *testing.T) {
		editLastTurn(t)
		turn(t, initialEnv, initialVars)
		quietTurn(t, initialEnv)
	})
	t.Run("fresh CLI after a failed resume", func(t *testing.T) {
		rejectNextResume.Store(true)
		turn(t, initialEnv, initialVars)
		if rejectNextResume.Load() {
			t.Fatal("fixture did not attempt to resume the native session")
		}
		quietTurn(t, initialEnv)
	})
	save("rotated", "ROTATED_TOKEN", "fixture-replacement", false, false)
	save("disabled", "DISABLED_TOKEN", "fixture-disabled", false, true)
	if err := h.repo.DeleteProjectCredential(t.Context(), session.ProjectID, "deleted"); err != nil {
		t.Fatal(err)
	}
	rotatedEnv := map[string]string{"ROTATED_TOKEN": "fixture-replacement"}
	turn(t, rotatedEnv, "ROTATED_TOKEN 的配置已更新", "DELETED_TOKEN、DISABLED_TOKEN 本轮已不可用")
	quietTurn(t, rotatedEnv)
	save("disabled", "DISABLED_TOKEN", "fixture-disabled", false, false)
	availableEnv := map[string]string{"ROTATED_TOKEN": "fixture-replacement", "DISABLED_TOKEN": "fixture-disabled"}
	turn(t, availableEnv, "DISABLED_TOKEN、ROTATED_TOKEN")
	quietTurn(t, availableEnv)
	t.Run("viewer downgrade", func(t *testing.T) {
		setRole(t, "viewer")
		prompt := turn(t, nil, "本轮无权使用已绑定的项目凭据", "继续处理用户请求")
		for _, name := range []string{"ROTATED_TOKEN", "DELETED_TOKEN", "DISABLED_TOKEN"} {
			if strings.Contains(prompt, name) {
				t.Errorf("viewer CLI received a credential variable name %q", name)
			}
		}
		quietTurn(t, nil)
		editLastTurn(t)
		quietTurn(t, nil)
		saved, _ := h.repo.AssistantSession(user.ID, session.ID)
		if !slices.Equal(saved.ProjectCredentialIDs, session.ProjectCredentialIDs) {
			t.Fatal("viewer turn changed the stored bindings")
		}
		setRole(t, "member")
		turn(t, availableEnv, "DISABLED_TOKEN、ROTATED_TOKEN")
		quietTurn(t, availableEnv)
	})
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
