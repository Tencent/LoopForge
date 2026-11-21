package httpapi

import (
	"net/http"
	"testing"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

func TestSharedAgentReferences(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	agents := []domain.AgentDefinition{
		{ID: "sar-shared", Name: "Shared", Version: 1, Shared: true, Rules: []string{"sar-rule-a", "sar-rule-b"}, Skills: []string{"sar-skill-a"}},
		{ID: "sar-private", Name: "Private", Version: 1, ProjectID: "sar-p", Rules: []string{"sar-rule-private"}, Skills: []string{"sar-skill-private"}},
	}
	for _, a := range agents {
		if err := s.PutAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	h := &catalogHandlers{deps: &deps{repo: s}}
	cases := []struct {
		kind, id string
		want     bool
	}{
		{"rule", "sar-rule-a", true},
		{"rule", "sar-rule-b", true},
		{"skill", "sar-skill-a", true},
		{"rule", "sar-skill-a", false},
		{"skill", "sar-rule-a", false},
		{"rule", "sar-rule-private", false},
		{"skill", "sar-skill-private", false},
		{"rule", "sar-missing", false},
	}
	for _, c := range cases {
		if got := h.sharedAgentReferences(c.kind, c.id); got != c.want {
			t.Errorf("sharedAgentReferences(%q, %q) = %v, want %v", c.kind, c.id, got, c.want)
		}
	}
}

func TestSharedScheduleDirectReferenceUnshareAndRestoreGuards(t *testing.T) {
	f := cvBNewHTTPFixture(t)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(f.s.PutProject(domain.Project{ID: "direct-refs", Name: "References", OwnerID: f.user.ID}))
	agent := domain.AgentDefinition{ID: "direct-agent", Name: "Agent", ProjectID: "direct-refs"}
	check(f.s.PutAgent(agent))
	agent.Shared = true
	check(f.s.PutAgent(agent))
	workflow := domain.WorkflowDefinition{ID: "direct-sop", Name: "SOP", ProjectID: "direct-refs", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval"}}}
	check(f.s.PutWorkflow(workflow))
	workflow, _ = f.s.Workflow(workflow.ID)
	workflow.Shared = true
	check(f.s.PutWorkflow(workflow))
	skill := domain.SkillDefinition{ID: "direct-skill", Name: "Skill", ProjectID: "direct-refs", Shared: true}
	check(f.s.PutSkill(skill))
	job := domain.ScheduleJob{ID: "direct-job", Name: "Job", ProjectID: "direct-refs", SkipDispatch: true, AssistantPrompt: "inspect", References: []domain.AssistantReference{{Type: "agent", ID: agent.ID}, {Type: "skill", ID: skill.ID}, {Type: "sop", ID: workflow.ID}}}
	check(f.s.PutScheduleJob(job))
	version, err := f.s.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Shared", Prompt: "inspect"}, 0)
	check(err)
	for _, tc := range []struct {
		path string
		body any
	}{
		{"/api/agents", domain.AgentDefinition{ID: agent.ID, Name: agent.Name, ProjectID: agent.ProjectID}},
		{"/api/skills", domain.SkillDefinition{ID: skill.ID, Name: skill.Name, ProjectID: skill.ProjectID}},
		{"/api/workflows", domain.WorkflowDefinition{ID: workflow.ID, Name: workflow.Name, ProjectID: workflow.ProjectID, SandboxProfileID: "local"}},
	} {
		if rec := cvBRequest(t, f, http.MethodPut, tc.path, tc.body, true); rec.Code != http.StatusConflict {
			t.Fatalf("unshare %s = %d: %s", tc.path, rec.Code, rec.Body.String())
		}
	}
	for _, tc := range []struct{ kind, id, path string }{
		{"agent", agent.ID, "/api/agents/" + agent.ID},
		{"workflow", workflow.ID, "/api/workflows/" + workflow.ID},
	} {
		revisions := f.s.Revisions(tc.kind, tc.id)
		if len(revisions) != 1 {
			t.Fatalf("expected one private revision for %s: %+v", tc.kind, revisions)
		}
		if rec := cvBRequest(t, f, http.MethodPost, tc.path+"/revisions/"+revisions[0].ID+"/restore", nil, true); rec.Code != http.StatusConflict {
			t.Fatalf("restore %s = %d: %s", tc.kind, rec.Code, rec.Body.String())
		}
	}
	check(f.s.ValidateSharedScheduleAssets(version))
}

func TestSharedScheduleReferences(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutSkill(domain.SkillDefinition{ID: "ssr-skill", Name: "Shared skill", ProjectID: "ssr-project", Shared: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(domain.AgentDefinition{ID: "ssr-agent", Name: "Shared agent", ProjectID: "ssr-project", Shared: true}); err != nil {
		t.Fatal(err)
	}
	job := domain.ScheduleJob{ID: "ssr-job", Name: "Shared task", ProjectID: "ssr-project", SkipDispatch: true, AssistantPrompt: "inspect", References: []domain.AssistantReference{{Type: "skill", ID: "ssr-skill"}}}
	if err := s.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Shared", Prompt: "inspect"}, 0); err != nil {
		t.Fatal(err)
	}
	job.References = nil
	if err := s.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Updated", Prompt: "inspect"}, 1); err != nil {
		t.Fatal(err)
	}
	job.ID = "ssr-agent-job"
	job.References = []domain.AssistantReference{{Type: "agent", ID: "ssr-agent"}}
	if err := s.PutScheduleJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishSharedSchedule(job.ID, job.ProjectID, domain.SharedScheduleVersion{Name: "Agent", Prompt: "inspect"}, 0); err != nil {
		t.Fatal(err)
	}
	h := &catalogHandlers{deps: &deps{repo: s}}
	if !h.sharedScheduleReferences("skill", "ssr-skill") {
		t.Fatal("shared schedule reference was not detected")
	}
	if h.sharedScheduleReferences("rule", "ssr-skill") || h.sharedScheduleReferences("skill", "missing") || !h.sharedScheduleReferences("agent", "ssr-agent") {
		t.Fatal("unrelated shared schedule reference was detected")
	}
}
