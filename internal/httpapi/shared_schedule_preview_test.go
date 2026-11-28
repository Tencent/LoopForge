package httpapi

import (
	"net/http"
	"path/filepath"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestSharedSchedulePublishPreviewListsAssetsWithoutSharing(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	t.Setenv("AGENTFLOW_SESSION_SECRET", "shared-schedule-preview-secret")
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(st.PutUser(domain.User{LoginName: "preview-owner", CodeBuddyAPIKey: "test-only"}))
	owner, _ := st.UserByLoginName("preview-owner")
	check(st.PutUser(domain.User{LoginName: "preview-viewer", CodeBuddyAPIKey: "test-only"}))
	viewer, _ := st.UserByLoginName("preview-viewer")
	check(st.PutProject(domain.Project{ID: "preview-src", Name: "Preview", OwnerID: owner.ID, Members: []domain.ProjectMember{{UserID: viewer.ID, Role: "viewer"}}}))
	check(st.PutRule(domain.RuleDefinition{ID: "pv-rule", Name: "私有规则", ProjectID: "preview-src"}))
	check(st.PutSkill(domain.SkillDefinition{ID: "pv-skill", Name: "私有技能", ProjectID: "preview-src"}))
	check(st.PutAgent(domain.AgentDefinition{ID: "pv-agent", Name: "私有角色", ProjectID: "preview-src", Rules: []string{"pv-rule"}, Skills: []string{"pv-skill"}}))
	check(st.PutSkill(domain.SkillDefinition{ID: "pv-shared-skill", Name: "已共享技能", ProjectID: "preview-src", Shared: true}))
	check(st.PutSandbox(domain.SandboxProfile{ID: "pv-sandbox", Name: "私有沙箱", ProjectID: "preview-src", Driver: domain.SandboxDriverNative}))
	check(st.PutWorkflow(domain.WorkflowDefinition{ID: "pv-sop", Name: "私有SOP", ProjectID: "preview-src", SandboxProfileID: "pv-sandbox", Nodes: []domain.WorkflowNode{{ID: "n1", Name: "N1", Type: "approval"}}}))
	job := domain.ScheduleJob{ID: "pv-job", Name: "预检任务", ProjectID: "preview-src", SkipDispatch: true, AssistantPrompt: "inspect", OwnerUserID: owner.ID, References: []domain.AssistantReference{{Type: "agent", ID: "pv-agent"}, {Type: "skill", ID: "pv-shared-skill"}, {Type: "sop", ID: "pv-sop"}}}
	check(st.PutScheduleJob(job))
	server := New(st, engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")

	requestJSONAuthed[domain.SharedSchedulePublishPreview](t, server, viewer, http.MethodGet, "/api/schedule-jobs/pv-job/publish-preview", nil, 403)

	got := requestJSONAuthed[domain.SharedSchedulePublishPreview](t, server, owner, http.MethodGet, "/api/schedule-jobs/pv-job/publish-preview", nil, 200)
	gotShare := map[string]bool{}
	for _, a := range got.WillShare {
		gotShare[a.Type+"/"+a.ID] = true
	}
	for _, key := range []string{"agent/pv-agent", "rule/pv-rule", "skill/pv-skill"} {
		if !gotShare[key] {
			t.Fatalf("preview did not list %s as newly shared: %+v", key, got)
		}
	}
	if gotShare["skill/pv-shared-skill"] || gotShare["sop/pv-sop"] {
		t.Fatalf("preview listed an already-shared or blocked asset: %+v", got)
	}
	if len(got.Blocked) != 1 || got.Blocked[0].Type != "sop" || got.Blocked[0].ID != "pv-sop" || got.Blocked[0].Reason == "" {
		t.Fatalf("blocked references: %+v", got.Blocked)
	}

	// The preview is read-only: nothing became shared and no version exists.
	if agent, _ := st.Agent("pv-agent"); agent.Shared {
		t.Fatal("preview shared the agent")
	}
	if rule, _ := st.Rule("pv-rule"); rule.Shared {
		t.Fatal("preview shared the agent rule")
	}
	if skill, _ := st.Skill("pv-skill"); skill.Shared {
		t.Fatal("preview shared the agent skill")
	}
	if workflow, _ := st.Workflow("pv-sop"); workflow.Shared {
		t.Fatal("preview shared the SOP")
	}
	if tasks, _ := st.SharedScheduleTasks(); len(tasks) != 0 {
		t.Fatal("preview created a shared version")
	}

	// Once the sandbox is shared the SOP becomes publishable and the preview
	// lists it as newly shared; after publishing nothing is new anymore.
	sandbox, _ := st.Sandbox("pv-sandbox")
	sandbox.Shared = true
	check(st.PutSandbox(sandbox))
	got = requestJSONAuthed[domain.SharedSchedulePublishPreview](t, server, owner, http.MethodGet, "/api/schedule-jobs/pv-job/publish-preview", nil, 200)
	if len(got.Blocked) != 0 {
		t.Fatalf("blocked references after sharing the sandbox: %+v", got.Blocked)
	}
	gotShare = map[string]bool{}
	for _, a := range got.WillShare {
		gotShare[a.Type+"/"+a.ID] = true
	}
	for _, key := range []string{"agent/pv-agent", "rule/pv-rule", "skill/pv-skill", "sop/pv-sop"} {
		if !gotShare[key] {
			t.Fatalf("preview did not list %s as newly shared: %+v", key, got)
		}
	}
	requestJSONAuthed[domain.SharedScheduleVersion](t, server, owner, http.MethodPost, "/api/schedule-jobs/pv-job/publish", map[string]any{"baseVersion": 0, "name": "共享预检", "prompt": "inspect"}, 201)
	got = requestJSONAuthed[domain.SharedSchedulePublishPreview](t, server, owner, http.MethodGet, "/api/schedule-jobs/pv-job/publish-preview", nil, 200)
	if len(got.WillShare) != 0 || len(got.Blocked) != 0 {
		t.Fatalf("preview after publishing: %+v", got)
	}
}
