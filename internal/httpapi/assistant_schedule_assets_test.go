package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestSharedSchedulePinsDirectReferencesAndKeepsNestedBindingsScoped(t *testing.T) {
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	h, _ := workbenchFixture(t)
	st := h.repo
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(st.PutUser(domain.User{LoginName: "target-owner", CodeBuddyAPIKey: "target-key"}))
	owner, _ := st.UserByLoginName("target-owner")
	check(st.PutProject(domain.Project{ID: "target", Name: "Target", OwnerID: owner.ID}))
	check(st.PutRule(domain.RuleDefinition{ID: "source-rule", Name: "Rule", ProjectID: "workbench", Content: "bound rule"}))
	check(st.PutSkill(domain.SkillDefinition{ID: "source-skill", Name: "Skill", ProjectID: "workbench", Content: "bound skill"}))
	role := domain.AgentDefinition{ID: "source-role", Name: "Source role", ProjectID: "workbench", Shared: true, Prompt: "shared through task", Rules: []string{"source-rule"}, Skills: []string{"source-skill"}}
	check(st.PutAgent(role))
	check(st.PutAgent(domain.AgentDefinition{ID: "unrelated-role", Name: "Unrelated", ProjectID: "workbench"}))
	wf := domain.WorkflowDefinition{ID: "source-sop", Name: "SOP", ProjectID: "workbench", Shared: true, SandboxProfileID: repo.TestNativeSandboxID, Nodes: []domain.WorkflowNode{{ID: "approve", Type: "approval", Name: "Approve"}}}
	check(st.PutWorkflow(wf))
	job := domain.ScheduleJob{ID: "shared-source", Name: "Shared", ProjectID: "workbench", SkipDispatch: true, AssistantPrompt: "inspect", References: []domain.AssistantReference{{Type: "agent", ID: role.ID}, {Type: "sop", ID: wf.ID}}}
	check(st.PutScheduleJob(job))
	v, err := st.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Shared", Prompt: "inspect"}, 0)
	check(err)
	h.engine = engine.New(st, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	h.api = New(st, h.engine, tracker.NewService(), "", "")
	newRequest := func(refs []domain.AssistantReference) assistantRequest {
		return assistantRequest{ProjectID: "target", Messages: []assistantMessage{{Role: "user", Content: "inspect", References: refs}}}
	}
	c, _ := workbenchContext(owner, http.MethodPost, "/api/assistant/chat", bytes.NewBuffer(nil))
	ordinary := newRequest(v.References)
	if err := h.prepareWorkbenchContext(c, &ordinary, owner); err != nil {
		t.Fatalf("ordinary chat could not use globally shared assets: %v", err)
	}
	if len(ordinary.roles) != 1 || len(ordinary.roles[0].Rules) != 0 || len(ordinary.roles[0].Skills) != 0 {
		t.Fatalf("nested private bindings crossed the project boundary: %+v", ordinary.roles)
	}
	scope := scheduledAssets{ProjectID: "target", Version: v}
	ctx := context.WithValue(c.Request.Context(), scheduledAssetsKey{}, scope)
	c.Request = c.Request.WithContext(ctx)
	scheduled := newRequest(v.References)
	check(h.prepareWorkbenchContext(c, &scheduled, owner))
	if len(scheduled.roles) != 1 || len(scheduled.roles[0].Rules) != 0 || len(scheduled.roles[0].Skills) != 0 || !strings.Contains(scheduled.contextText, role.Prompt) {
		t.Fatalf("nested bindings crossed the task grant: %+v", scheduled.roles)
	}
	forged := newRequest([]domain.AssistantReference{{Type: "agent", ID: "unrelated-role", ProjectID: "target"}})
	if err := h.prepareWorkbenchContext(c, &forged, owner); err == nil {
		t.Fatal("task grant opened the whole source project")
	}
	wrongProject := newRequest(v.References)
	wrongProject.ProjectID = "workbench"
	if err := h.prepareWorkbenchContext(c, &wrongProject, owner); err == nil {
		t.Fatal("grant bypassed target project membership")
	}
	if scheduledAssetRead(ctx, "target", "agents_save", role.ID) || scheduledAssetRead(ctx, "target", "agents_get", "unrelated-role") {
		t.Fatal("grant allowed unrelated assets or writes")
	}
	if _, err := h.callAssistantPlatform(context.Background(), owner, "target", assistantMCPCall{Tool: "agents_get", Path: map[string]any{"id": role.ID}}); err == nil {
		t.Fatal("ordinary platform tool call bypassed the current-project boundary")
	}
	if _, err := h.callAssistantPlatform(ctx, owner, "target", assistantMCPCall{Tool: "agents_get", Path: map[string]any{"id": role.ID}}); err != nil {
		t.Fatal(err)
	}

	// A scheduled SOP executes in the target project with the target actor.
	ctx = withAssistantExecMode(ctx, assistantExecModeForApproval(AssistantApprovalAuto))
	result, err := h.callAssistantPlatform(ctx, owner, "target", assistantMCPCall{Tool: "runs_start", Body: map[string]any{"workflowId": wf.ID, "projectId": "target", "input": "inspect"}})
	check(err)
	var run domain.Run
	check(json.Unmarshal([]byte(result), &run))
	t.Cleanup(func() { _ = h.engine.Cancel(run.ID) })
	stored, ok := st.RunState(run.ID)
	if !ok || stored.ExecutionContext.ProjectID != "target" || stored.ExecutionContext.ResponsibleUserID != owner.ID || stored.ExecutionContext.SharedScheduleID != v.ID {
		t.Fatalf("incorrect SOP execution scope: %+v", stored.ExecutionContext)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, _ = st.RunState(run.ID)
		if stored.Status == "waiting" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stored.Status != "waiting" {
		t.Fatalf("scoped SOP failed: %s", stored.Status)
	}
	// The directly referenced SOP is shared and can be started through the
	// ordinary run API. Its nested dependencies are not task-owned.
	requestJSONAuthed[map[string]any](t, h.api, owner, http.MethodPost, "/api/runs", map[string]any{"workflowId": wf.ID, "projectId": "target", "sharedScheduleId": v.ID, "sharedScheduleVersion": 1}, 201)
	role.Skills = append(role.Skills, "new-private-binding")
	check(st.PutSkill(domain.SkillDefinition{ID: "new-private-binding", Name: "Later", ProjectID: "workbench"}))
	check(st.PutAgent(role))
	if err := h.validateScheduledAssets(ctx, "target"); err != nil {
		t.Fatalf("nested binding change invalidated direct version: %v", err)
	}
}
