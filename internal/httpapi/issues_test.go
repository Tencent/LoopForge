package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentflow/internal/auth"
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/tracker"
)

func TestIssueDispatchCreatesTaskAndRun(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "issue-test", Name: "Issue test", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")

	if err := s.PutIssue(domain.Issue{ID: "iss-audit", ProjectID: "platform", Title: "Implement audit log", Description: "Record mutations", Status: "todo", Priority: "high", AssigneeType: "agent", AssigneeID: "developer", WorkflowID: "issue-test"}); err != nil {
		t.Fatal(err)
	}
	created, ok := s.Issue("iss-audit")
	if !ok || created.ID == "" {
		t.Fatalf("unexpected issue: %+v", created)
	}

	task := requestEmptyJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+created.ID+"/dispatch", http.StatusCreated)
	if task.IssueID != created.ID || task.RunID == "" || task.Status != "running" || task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() {
		t.Fatalf("unexpected task: %+v", task)
	}
	stored, ok := s.Issue(created.ID)
	if !ok || stored.Status != "in_progress" || stored.LatestTaskID != task.ID {
		t.Fatalf("issue was not linked to task: %+v", stored)
	}
	run, ok := s.Run(task.RunID)
	if !ok {
		t.Fatal("dispatch did not create workflow run")
	}
	if !strings.Contains(run.Input, "需求身份（平台权威，禁止忽略）") || !strings.Contains(run.Input, "平台工作项：iss-audit") || !strings.Contains(run.Input, "来源：本地") || strings.Contains(run.Input, "回写") {
		t.Fatalf("local issue identity missing from input: %s", run.Input)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, _ := s.Run(task.RunID)
		if run.Status == "waiting" {
			_ = requestJSON[struct {
				Sequence int64                   `json:"sequence"`
				Events   []domain.TelemetryEvent `json:"events"`
			}](t, h, http.MethodGet, "/api/runs/"+task.RunID+"/telemetry?after=0", nil, http.StatusOK)
			requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/"+created.ID+"/dispatch", map[string]any{}, http.StatusConflict)
			rerun := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+created.ID+"/dispatch", map[string]any{"rerun": true}, http.StatusCreated)
			if rerun.ID == task.ID || rerun.DispatchMode != "rerun" {
				t.Fatalf("unexpected rerun task: %+v", rerun)
			}
			t.Cleanup(func() { _ = e.Cancel(rerun.RunID) })
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("workflow did not reach its approval gate")
}

func TestDispatchTAPDIssuePutsStoryIdentityInInput(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "tapd-identity", Name: "TAPD identity", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	issue := domain.Issue{
		ID: "tapd-1010000000100001887", ProjectID: "platform", Kind: "requirement", Source: "tapd",
		Title: "在soul广场页面新增一个banner", Description: "纯前端写死", Status: "todo",
		WorkflowID: "tapd-identity", Clarification: domain.ClarificationAssessment{Passed: true, Score: 90, Status: "ready"},
		ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "1010000000100001887", ExternalType: "story", ExternalURL: "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001887"},
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	task := requestEmptyJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", http.StatusCreated)
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
	run, ok := s.Run(task.RunID)
	if !ok {
		t.Fatal("dispatch did not create run")
	}
	for _, want := range []string{
		"需求身份（平台权威，禁止忽略）",
		"外部 ID：1010000000100001887",
		"https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001887",
	} {
		if !strings.Contains(run.Input, want) {
			t.Fatalf("TAPD identity missing %q in input:\n%s", want, run.Input)
		}
	}
	if strings.Contains(run.Input, "回写") {
		t.Fatalf("dispatch input must not instruct write-back:\n%s", run.Input)
	}
}

func TestCreateIssueDefaultsAssigneeToCurrentUser(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "create-assignee-session-secret")
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	creator := domain.User{ID: 88, LoginName: "creator", ChineseName: "建单人"}
	if err := s.PutUser(creator); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	created := requestJSONAuthed[domain.Issue](t, h, creator, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "platform", "title": "新人需求", "kind": "requirement",
	}, http.StatusOK)
	if created.AssigneeUserID != 88 {
		t.Fatalf("assignee=%d want creator 88, not owner", created.AssigneeUserID)
	}
}

func TestPlatformRequirementOptionalTapdMetricBinding(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	issue := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "platform", "title": "平台需求", "kind": "requirement", "syncTarget": "local",
	}, http.StatusOK)
	if issue.Source != "local" || issue.TapdID != "" || issue.ExternalRef != nil {
		t.Fatalf("unbound platform demand: %+v", issue)
	}
	bound := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": issue.ID, "projectId": "platform", "title": issue.Title, "tapdId": " 1001 ",
	}, http.StatusOK)
	if bound.Source != "local" || bound.TapdID != "1001" || bound.ExternalRef != nil || bound.RequirementTAPDID() != "1001" {
		t.Fatalf("manual binding changed tracker identity: %+v", bound)
	}
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/sync/create", map[string]any{
		"issueId": issue.ID, "target": "tapd",
	}, http.StatusConflict)
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/sync/create", map[string]any{
		"issueId": issue.ID, "target": "gongfeng",
	}, http.StatusConflict)
	preserved := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": issue.ID, "projectId": "platform", "title": issue.Title,
	}, http.StatusOK)
	if preserved.TapdID != "1001" {
		t.Fatalf("unrelated update cleared TAPD ID: %+v", preserved)
	}
	cleared := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": issue.ID, "projectId": "platform", "title": issue.Title, "tapdId": "",
	}, http.StatusOK)
	if cleared.TapdID != "" || cleared.Source != "local" {
		t.Fatalf("explicit clear failed: %+v", cleared)
	}
	requestJSON[map[string]any](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": issue.ID, "projectId": "platform", "title": issue.Title, "tapdId": "not-a-number",
	}, http.StatusBadRequest)
}

func TestIssueClarificationStartPersistsAcrossEdit(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	started := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	issue := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "platform", "title": "澄清后开发", "kind": "requirement", "syncTarget": "local",
		"clarification": map[string]any{"startedAt": started.Format(time.RFC3339)},
	}, http.StatusOK)
	if !issue.Clarification.StartedAt.Equal(started) {
		t.Fatalf("clarification start not saved: %+v", issue.Clarification)
	}
	updated := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": issue.ID, "projectId": "platform", "title": "澄清后开发（修改）",
	}, http.StatusOK)
	if !updated.Clarification.StartedAt.Equal(started) {
		t.Fatalf("unrelated edit erased clarification start: %+v", updated.Clarification)
	}
	requestJSON[map[string]any](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id": issue.ID, "projectId": "platform", "title": issue.Title,
		"clarification": map[string]any{"startedAt": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)},
	}, http.StatusBadRequest)
}

func TestIssueDispatchUsesTriggerUserCredentials(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "dispatch-trigger-session-secret")
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	operator := domain.User{ID: 88, LoginName: "operator", ChineseName: "点运行"}
	if err := s.PutUser(operator); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "issue-trigger", Name: "Trigger", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	if err := s.PutIssue(domain.Issue{ID: "iss-trigger", ProjectID: "platform", Title: "Run me", WorkflowID: "issue-trigger"}); err != nil {
		t.Fatal(err)
	}
	task := requestJSONAuthed[domain.Task](t, h, operator, http.MethodPost, "/api/issues/iss-trigger/dispatch", map[string]any{}, http.StatusCreated)
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
	run, ok := s.Run(task.RunID)
	if !ok || run.ExecutionContext.ResponsibleUserID != 88 {
		t.Fatalf("run responsible=%d want trigger user 88", run.ExecutionContext.ResponsibleUserID)
	}
}

func TestIssueDispatchRejectsMissingOwnerAndResponsibleUser(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "ownerless", Name: "Ownerless"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "ownerless-issue", ProjectID: "ownerless", Title: "Cannot run", WorkflowID: "issue-fix-production"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")
	result := requestJSON[map[string]string](t, h, http.MethodPost, "/api/issues/ownerless-issue/dispatch", map[string]any{}, http.StatusConflict)
	if !strings.Contains(result["error"], "项目未配置 Owner") {
		t.Fatalf("unexpected error: %q", result["error"])
	}
}

func TestExternalIssueDispatchKeepsExplicitWorkflow(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	workflow := domain.WorkflowDefinition{
		ID: "gongfeng-bwrap-override", Name: "Gongfeng bwrap override", Version: 1, SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}},
	}
	if err := s.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	issue := domain.Issue{
		ID: "gf-custom-flow", ProjectID: "platform", Source: "gongfeng", Kind: "issue", Title: "Use custom flow",
		WorkflowID: workflow.ID, ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalID: "99", ExternalIID: 22},
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Issue(issue.ID)
	if stored.WorkflowID != workflow.ID {
		t.Fatalf("explicit workflow was overwritten on save: %q", stored.WorkflowID)
	}

	task := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]any{}, http.StatusCreated)
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
	if task.WorkflowID != workflow.ID {
		t.Fatalf("dispatch used workflow %q, want %q", task.WorkflowID, workflow.ID)
	}
	run, ok := s.Run(task.RunID)
	if !ok || run.WorkflowID != workflow.ID {
		t.Fatalf("run did not use explicit workflow: %+v", run)
	}
}

func TestIssueDispatchSecondLaneKeepsDefaultSOP(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	backend := domain.WorkflowDefinition{ID: "wf-backend-lane", Name: "Backend", SOPLane: "backend", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	frontend := domain.WorkflowDefinition{ID: "wf-frontend-lane", Name: "Frontend", SOPLane: "frontend", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(backend); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(frontend); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	if err := s.PutIssue(domain.Issue{ID: "iss-lanes", ProjectID: "platform", Kind: "requirement", Title: "Two lanes", Status: "todo", WorkflowID: backend.ID}); err != nil {
		t.Fatal(err)
	}
	first := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/iss-lanes/dispatch", map[string]any{}, http.StatusCreated)
	t.Cleanup(func() { _ = e.Cancel(first.RunID) })
	if first.WorkflowID != backend.ID {
		t.Fatalf("first task workflow=%q", first.WorkflowID)
	}
	second := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/iss-lanes/dispatch", map[string]any{"workflowId": frontend.ID}, http.StatusCreated)
	t.Cleanup(func() { _ = e.Cancel(second.RunID) })
	if second.WorkflowID != frontend.ID || second.ID == first.ID {
		t.Fatalf("second task=%#v", second)
	}
	stored, _ := s.Issue("iss-lanes")
	if stored.WorkflowID != backend.ID {
		t.Fatalf("default SOP overwritten: %q", stored.WorkflowID)
	}
	snap := requestJSON[domain.RequirementSnapshot](t, h, http.MethodGet, "/api/issues/iss-lanes/requirement", nil, http.StatusOK)
	if snap.Overall.RunCount < 2 {
		t.Fatalf("snapshot runs=%d", snap.Overall.RunCount)
	}
	var sawBackend, sawFrontend bool
	for _, lane := range snap.Lanes {
		if lane.Lane == "backend" && lane.Started {
			sawBackend = true
		}
		if lane.Lane == "frontend" && lane.Started {
			sawFrontend = true
		}
	}
	if !sawBackend || !sawFrontend {
		t.Fatalf("lanes=%#v", snap.Lanes)
	}
}

func TestIssueSOPBindingsRoundTripAndDispatchOwner(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "sop-bind-session-secret")
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	owner := domain.User{ID: 7, LoginName: "backend-owner", ChineseName: "后端责任人"}
	operator := domain.User{ID: 88, LoginName: "operator", ChineseName: "点运行"}
	if err := s.PutUser(owner); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUser(operator); err != nil {
		t.Fatal(err)
	}
	design := domain.WorkflowDefinition{ID: "wf-design-bind", Name: "Design", SOPLane: "design", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	backend := domain.WorkflowDefinition{ID: "wf-backend-bind", Name: "Backend", SOPLane: "backend", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(design); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(backend); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	if err := s.PutIssue(domain.Issue{ID: "iss-sops", ProjectID: "platform", Kind: "requirement", Title: "Orchestrate", Status: "todo", WorkflowID: backend.ID}); err != nil {
		t.Fatal(err)
	}
	saved := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues/iss-sops/sops", map[string]any{
		"bindings": []map[string]any{
			{"workflowId": design.ID, "order": 1, "assigneeUserId": 7},
			{"workflowId": backend.ID, "order": 2, "assigneeUserId": 7, "handoff": []string{"design.md"}},
		},
	}, http.StatusOK)
	if saved.WorkflowID != design.ID || len(saved.SOPBindings) != 2 {
		t.Fatalf("saved=%#v", saved)
	}
	task := requestJSONAuthed[domain.Task](t, h, operator, http.MethodPost, "/api/issues/iss-sops/dispatch", map[string]any{"workflowId": backend.ID}, http.StatusCreated)
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
	run, ok := s.Run(task.RunID)
	if !ok || run.ExecutionContext.ResponsibleUserID != 7 {
		t.Fatalf("responsible=%d want step owner 7, run=%+v", run.ExecutionContext.ResponsibleUserID, run.ExecutionContext)
	}
	if !strings.Contains(run.Input, "design.md") {
		t.Fatalf("handoff missing from input: %s", run.Input)
	}
}

func TestRequirementInheritsProjectPlaybook(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	product := domain.WorkflowDefinition{ID: "wf-play-product", Name: "Product", SOPLane: "product", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	backend := domain.WorkflowDefinition{ID: "wf-play-backend", Name: "Backend", SOPLane: "backend", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(product); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWorkflow(backend); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUser(domain.User{ID: 3, LoginName: "owner", ChineseName: "项目Owner"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProject(domain.Project{ID: "playbook", Key: "PB", Name: "Playbook", OwnerID: 3, SOPBindings: []domain.IssueSOPBinding{
		{WorkflowID: product.ID, Order: 1},
		{WorkflowID: backend.ID, Order: 2},
	}}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	created := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", domain.Issue{
		ProjectID: "playbook", Kind: "requirement", Title: "Inherit playbook", Status: "todo", AssigneeUserID: 3,
	}, http.StatusOK)
	if created.WorkflowID != product.ID {
		t.Fatalf("new issue workflow=%q want first playbook step", created.WorkflowID)
	}
	if created.SOPBindings != nil {
		t.Fatalf("issue must inherit live, not copy playbook: %#v", created.SOPBindings)
	}
	if created.PlaybookID != "default" {
		t.Fatalf("legacy wrap playbookId=%q", created.PlaybookID)
	}
	snap := requestJSON[domain.RequirementSnapshot](t, h, http.MethodGet, "/api/issues/"+created.ID+"/requirement", nil, http.StatusOK)
	if len(snap.Lanes) != 1 || snap.Lanes[0].WorkflowID != product.ID {
		t.Fatalf("bound lanes=%#v", snap.Lanes)
	}
	task := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+created.ID+"/dispatch", map[string]any{"workflowId": backend.ID}, http.StatusCreated)
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
	if task.WorkflowID != backend.ID {
		t.Fatalf("dispatch workflow=%q", task.WorkflowID)
	}
}

func TestRequirementPicksNamedPlaybook(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	product := domain.WorkflowDefinition{ID: "wf-combo-product", Name: "Product", SOPLane: "product", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	frontend := domain.WorkflowDefinition{ID: "wf-combo-frontend", Name: "Frontend", SOPLane: "frontend", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	backend := domain.WorkflowDefinition{ID: "wf-combo-backend", Name: "Backend", SOPLane: "backend", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	for _, wf := range []domain.WorkflowDefinition{product, frontend, backend} {
		if err := s.PutWorkflow(wf); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutUser(domain.User{ID: 3, LoginName: "owner", ChineseName: "项目Owner"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProject(domain.Project{ID: "combos", Key: "CB", Name: "Combos", OwnerID: 3, SOPPlaybooks: []domain.SOPPlaybook{
		{ID: "full", Name: "完整需求", Default: true, Bindings: []domain.IssueSOPBinding{
			{WorkflowID: product.ID, Order: 1},
			{WorkflowID: frontend.ID, Order: 2},
			{WorkflowID: backend.ID, Order: 3},
		}},
		{ID: "be", Name: "仅后端", Bindings: []domain.IssueSOPBinding{
			{WorkflowID: backend.ID, Order: 1},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	created := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", domain.Issue{
		ProjectID: "combos", Kind: "requirement", Title: "Backend only", Status: "todo", AssigneeUserID: 3, PlaybookID: "be",
	}, http.StatusOK)
	if created.PlaybookID != "be" {
		t.Fatalf("playbookId=%q", created.PlaybookID)
	}
	if created.WorkflowID != backend.ID {
		t.Fatalf("workflow=%q want backend", created.WorkflowID)
	}
	if created.SOPBindings != nil {
		t.Fatalf("must inherit named playbook live: %#v", created.SOPBindings)
	}
	snap := requestJSON[domain.RequirementSnapshot](t, h, http.MethodGet, "/api/issues/"+created.ID+"/requirement", nil, http.StatusOK)
	if len(snap.Lanes) != 1 || snap.Lanes[0].WorkflowID != backend.ID {
		t.Fatalf("named lanes=%#v", snap.Lanes)
	}
	switched := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues/"+created.ID+"/sops", map[string]any{"playbookId": "full"}, http.StatusOK)
	if switched.PlaybookID != "full" || switched.SOPBindings != nil {
		t.Fatalf("switch=%#v", switched)
	}
	if switched.WorkflowID != product.ID {
		t.Fatalf("switch workflow=%q", switched.WorkflowID)
	}
	snap = requestJSON[domain.RequirementSnapshot](t, h, http.MethodGet, "/api/issues/"+created.ID+"/requirement", nil, http.StatusOK)
	if len(snap.Lanes) != 1 || snap.Lanes[0].WorkflowID != product.ID {
		t.Fatalf("switched lanes=%#v", snap.Lanes)
	}
}

func TestWorkflowAPIRequiresExistingWorkflowSandboxProfile(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")

	requestJSON[map[string]string](t, h, http.MethodPut, "/api/workflows", map[string]any{"id": "missing", "name": "Missing", "nodes": []any{}, "edges": []any{}}, http.StatusBadRequest)
	requestJSON[map[string]string](t, h, http.MethodPut, "/api/workflows", map[string]any{"id": "unknown", "name": "Unknown", "sandboxProfileId": "does-not-exist", "nodes": []any{}, "edges": []any{}}, http.StatusBadRequest)
	created := requestJSON[domain.WorkflowDefinition](t, h, http.MethodPut, "/api/workflows", map[string]any{"id": "valid", "name": "Valid", "sandboxProfileId": "local", "nodes": []any{}, "edges": []any{}}, http.StatusOK)
	if created.SandboxProfileID != "local" {
		t.Fatalf("sandboxProfileId = %q", created.SandboxProfileID)
	}
}

func TestAssetAPIAcceptsSandboxEnvironmentButExecutorsAreReadOnly(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")

	requestJSON[map[string]string](t, h, http.MethodPut, "/api/executors", map[string]any{"id": "legacy", "name": "Legacy", "kind": "mock"}, http.StatusMethodNotAllowed)
	executors := requestEmptyJSON[[]domain.ExecutorDefinition](t, h, http.MethodGet, "/api/executors", http.StatusOK)
	if len(executors) != 3 || executors[0].ID != "codebuddy" || executors[1].ID != "codex" || executors[2].ID != "claude" {
		t.Fatalf("built-in CLI catalog = %+v", executors)
	}
	requestJSON[map[string]bool](t, h, http.MethodPut, "/api/sandboxes", map[string]any{"id": "configured", "name": "Configured", "projectId": "platform", "driver": "local", "environment": map[string]string{"TOOL_MODE": "test"}}, http.StatusOK)
	profile, ok := s.Sandbox("configured")
	if !ok || profile.Environment["TOOL_MODE"] != "test" {
		t.Fatalf("sandbox environment was not persisted: %+v", profile)
	}
}

func TestAgentAPIOnlyAcceptsBuiltInCLIs(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces"))), tracker.NewService(), "", "")

	requestJSON[map[string]string](t, h, http.MethodPut, "/api/agents", map[string]any{"id": "custom-cli", "name": "Custom CLI", "projectId": "platform", "executorId": "custom"}, http.StatusBadRequest)
	for _, executorID := range []string{"codebuddy", "codex", "claude"} {
		created := requestJSON[domain.AgentDefinition](t, h, http.MethodPut, "/api/agents", map[string]any{"id": "agent-" + executorID, "name": executorID, "projectId": "platform", "executorId": executorID}, http.StatusOK)
		if created.ExecutorID != executorID {
			t.Fatalf("agent CLI = %q, want %q", created.ExecutorID, executorID)
		}
	}
}

func TestAssetAPIManagesDockerfilesEnvironmentsAndDockerSandbox(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")

	requestJSON[map[string]string](t, h, http.MethodPut, "/api/dockerfiles", map[string]any{"id": "slim", "name": "Slim", "projectId": "platform"}, http.StatusBadRequest)
	saved := requestJSON[map[string]bool](t, h, http.MethodPut, "/api/dockerfiles", map[string]any{"id": "slim", "name": "Slim", "projectId": "platform", "content": "FROM debian:bookworm-slim"}, http.StatusOK)
	if !saved["ok"] {
		t.Fatalf("dockerfile save = %+v", saved)
	}
	requestJSON[map[string]bool](t, h, http.MethodPut, "/api/environments", map[string]any{"id": "php8-test", "name": "PHP 8 test", "projectId": "platform", "variables": map[string]string{"PHP_VERSION": "8.3.0"}, "pathEntries": []string{"bin"}, "installScript": "mkdir -p \"$PREFIX/bin\""}, http.StatusOK)
	requestJSON[map[string]string](t, h, http.MethodPut, "/api/sandboxes", map[string]any{"id": "container-new", "name": "Container", "projectId": "platform", "driver": "docker"}, http.StatusBadRequest)
	requestJSON[map[string]bool](t, h, http.MethodPut, "/api/sandboxes", map[string]any{"id": "container-new", "name": "Container", "projectId": "platform", "driver": "docker", "dockerfileId": "slim"}, http.StatusOK)
	profile, ok := s.Sandbox("container-new")
	if !ok || profile.DockerfileID != "slim" {
		t.Fatalf("docker sandbox should store dockerfileId: %+v", profile)
	}
	boot := requestJSON[map[string]any](t, h, http.MethodGet, "/api/bootstrap", nil, http.StatusOK)
	if boot["dockerfiles"] == nil || boot["environments"] == nil {
		t.Fatalf("bootstrap missing dockerfile/environment catalogs: %+v", boot)
	}
}

func TestManualRunKeepsSelectedProjectContext(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutProject(domain.Project{ID: "project-a", Key: "PA", Name: "Project A", OwnerID: 1}); err != nil {
		t.Fatal(err)
	}
	workflow := domain.WorkflowDefinition{
		ID: "manual-project-run", Name: "Manual project run", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}},
	}
	if err := s.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")

	run := requestJSON[domain.Run](t, h, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflow.ID,
		"input":      "manual smoke",
		"projectId":  "project-a",
	}, http.StatusCreated)
	t.Cleanup(func() { _ = e.Cancel(run.ID) })
	if run.ExecutionContext.ProjectID != "project-a" {
		t.Fatalf("manual run project context = %q", run.ExecutionContext.ProjectID)
	}
	if run.ExecutionContext.ResponsibleUserID != 1 {
		t.Fatalf("manual run responsible user = %d, want project owner 1", run.ExecutionContext.ResponsibleUserID)
	}
	requestJSON[map[string]string](t, h, http.MethodPost, "/api/runs", map[string]any{
		"workflowId": workflow.ID,
		"projectId":  "missing-project",
	}, http.StatusBadRequest)
	listed := requestJSON[[]domain.Run](t, h, http.MethodGet, "/api/runs?projectId=project-a", nil, http.StatusOK)
	if len(listed) != 1 || listed[0].ID != run.ID {
		t.Fatalf("project-a run list = %+v", listed)
	}
	other := requestJSON[[]domain.Run](t, h, http.MethodGet, "/api/runs?projectId=platform", nil, http.StatusOK)
	for _, item := range other {
		if item.ID == run.ID {
			t.Fatal("manual run leaked into another project list")
		}
	}
}

func TestRepositoryBindingIsValidatedAndFrozenAtDispatch(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "repo-flow", Name: "Repo flow", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{"projectId": "platform", "name": "AgentFlow", "gongfengProjectId": "team/agentflow", "webUrl": "https://git.example.com/team/agentflow", "localPath": source, "defaultBranch": "main"}, http.StatusOK)
	mustRunWorkspaceGit(t, source, "init", "-b", "main")
	mustRunWorkspaceGit(t, source, "config", "user.name", "Test")
	mustRunWorkspaceGit(t, source, "config", "user.email", "test@example.com")
	mustRunWorkspaceGit(t, source, "add", "README.md")
	mustRunWorkspaceGit(t, source, "commit", "-m", "baseline")
	mustRunWorkspaceGit(t, source, "branch", "release/1.0")
	repo.CloneURL = ""
	if err := s.PutRepository(repo); err != nil {
		t.Fatal(err)
	}
	releaseRepo := requestJSON[domain.Repository](t, h, http.MethodPost, "/api/repositories", map[string]any{"projectId": "platform", "name": "AgentFlow Release", "gongfengProjectId": "team/agentflow-release", "defaultBranch": "release/2026", "integrateBranch": "agentflow/release-integration"}, http.StatusOK)
	repositories := requestJSON[[]domain.Repository](t, h, http.MethodGet, "/api/repositories", nil, http.StatusOK)
	if len(repositories) < 2 || repo.DefaultBranch != "main" || repo.IntegrateBranch != "" || releaseRepo.DefaultBranch != "release/2026" || releaseRepo.IntegrateBranch != "agentflow/release-integration" {
		t.Fatalf("project repositories did not keep independent branches: %+v", repositories)
	}
	project, _ := s.Project("platform")
	project.DefaultRepositoryID = repo.ID
	savedProject := requestJSON[domain.Project](t, h, http.MethodPut, "/api/projects", project, http.StatusOK)
	if savedProject.GitToken != "" || savedProject.GitTokenConfigured {
		t.Fatalf("projects must not expose project-level credentials: %+v", savedProject)
	}
	issue := domain.Issue{ID: "iss-bound", ProjectID: "platform", Title: "Bound work", AssigneeType: "agent", AssigneeID: "developer", WorkflowID: "repo-flow", RepositoryID: repo.ID, BaseBranch: "release/1.0"}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	task := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]any{}, http.StatusCreated)
	if task.Repository.RepositoryID != repo.ID || task.Repository.BaseBranch != "main" || task.Repository.WorkBranch != "release/1.0" || !task.Repository.RequireExistingWorkBranch || task.Repository.GongfengProjectID != "team/agentflow" {
		t.Fatalf("task repository was not frozen: %+v", task.Repository)
	}
	run, _ := s.Run(task.RunID)
	if run.Repository != task.Repository || !bytes.Contains([]byte(run.Input), []byte("Repository context (frozen)")) {
		t.Fatalf("run repository context missing: %+v input=%q", run.Repository, run.Input)
	}
	if strings.TrimSpace(run.Repository.WorkspaceDir) == "" {
		t.Fatalf("single-repository run must assign a first-level WorkspaceDir: %+v", run.Repository)
	}
	if !bytes.Contains([]byte(run.Input), []byte("local directory: ./"+run.Repository.WorkspaceDir)) || !bytes.Contains([]byte(run.Input), []byte("SOP deliverables")) || !bytes.Contains([]byte(run.Input), []byte("workspace root")) {
		t.Fatalf("single-repository prompt did not describe subdirectory checkout and run-level deliverables: %q", run.Input)
	}
	if bytes.Contains([]byte(run.Input), []byte(source)) || bytes.Contains([]byte(run.Input), []byte("local path:")) {
		t.Fatalf("host localPath leaked into agent-visible input: %q", run.Input)
	}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("repo_secret_token")) {
		t.Fatalf("git token leaked into run snapshot: %s", raw)
	}
	if run.ExecutionContext.ProjectID != "platform" || run.ExecutionContext.IssueID != issue.ID || run.ExecutionContext.TaskID != task.ID || run.ExecutionContext.TraceID != run.ID {
		t.Fatalf("three-layer execution context was not frozen: %+v", run.ExecutionContext)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, _ = s.Run(task.RunID)
		if run.Status == "waiting" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "waiting" {
		t.Fatalf("run did not settle at approval gate after source checkout: %s", run.Status)
	}

	if err := s.PutProject(domain.Project{ID: "other", Key: "OTHER", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues", map[string]any{"projectId": "other", "title": "Wrong repo", "repositoryId": repo.ID}, http.StatusBadRequest)
}

func TestSecurityHeadersAndSameOriginCORS(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")

	req := httptest.NewRequest(http.MethodGet, "http://agentflow.example/api/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("missing security headers: status=%d headers=%v", rec.Code, rec.Header())
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "worker-src 'self' blob:") {
		t.Fatalf("workspace editor workers are not allowed by CSP: %q", rec.Header().Get("Content-Security-Policy"))
	}

	foreign := httptest.NewRequest(http.MethodOptions, "http://agentflow.example/api/issues", nil)
	foreign.Header.Set("Origin", "https://evil.example")
	foreignRec := httptest.NewRecorder()
	h.ServeHTTP(foreignRec, foreign)
	if foreignRec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin should be denied, got %d", foreignRec.Code)
	}

	sameOrigin := httptest.NewRequest(http.MethodOptions, "http://agentflow.example/api/issues", nil)
	sameOrigin.Header.Set("Origin", "http://agentflow.example")
	sameOriginRec := httptest.NewRecorder()
	h.ServeHTTP(sameOriginRec, sameOrigin)
	if sameOriginRec.Code != http.StatusNoContent || sameOriginRec.Header().Get("Access-Control-Allow-Origin") != "http://agentflow.example" {
		t.Fatalf("same origin preflight failed: status=%d headers=%v", sameOriginRec.Code, sameOriginRec.Header())
	}
}

func TestSyncConfigUpdatePreservesOperationalStatus(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	lastSync := time.Now().UTC().Add(-time.Minute)
	if err := s.PutSyncConfig(domain.SyncConfig{LastSyncAt: lastSync, LastSyncStatus: "failed", LastSyncError: "upstream timeout"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")

	updated := requestJSON[domain.SyncConfig](t, h, http.MethodPut, "/api/sync/config", map[string]any{
		"gongfengProjectId": "new/project", "autoSyncIntervalSec": 120, "autoSyncEnabled": false,
	}, http.StatusOK)
	if updated.LastSyncStatus != "failed" || updated.LastSyncError != "upstream timeout" || !updated.LastSyncAt.Equal(lastSync) {
		t.Fatalf("sync operational status was lost: %+v", updated)
	}
}

func TestRunSummaryOmitsHeavyAndSensitiveBodies(t *testing.T) {
	run := domain.Run{
		ID:        "run-summary",
		Events:    []domain.RunEvent{{ID: "event"}},
		Telemetry: []domain.TelemetryEvent{{ID: "telemetry"}},
		Artifacts: []domain.Artifact{{ID: "artifact", Content: "private artifact body", Size: 21}},
	}
	summary := runSummaries([]domain.Run{run})[0]
	if len(summary.Events) != 0 || len(summary.Telemetry) != 0 || len(summary.Artifacts) != 0 || summary.ArtifactCount != 1 {
		t.Fatalf("run summary still contains heavy bodies: %+v", summary)
	}
	if run.Artifacts[0].Content == "" {
		t.Fatal("summary mutated the source run")
	}
	detail := publicRun(run, false)
	if len(detail.Events) != 1 || len(detail.Telemetry) != 0 || len(detail.Logs) != 0 || detail.Artifacts[0].Content != "" {
		t.Fatalf("run detail sanitization is incorrect: %+v", detail)
	}
}

func TestPublicLiveRunKeepsFinishedNodeOutput(t *testing.T) {
	run := domain.Run{
		ID:     "run-live",
		Events: []domain.RunEvent{{ID: "event"}},
		Nodes: map[string]*domain.NodeRun{
			"analyze": {NodeID: "analyze", Status: "succeeded", Output: "fix-plan ready", Attempt: 2},
		},
		Artifacts: []domain.Artifact{{ID: "artifact", Name: "fix-plan.md", NodeID: "analyze", Size: 21, Content: "private"}},
		Usage:     domain.TokenUsage{TotalTokens: 2100},
		ToolCalls: 76,
	}
	live := publicLiveRun(run)
	if live.Nodes["analyze"] == nil || live.Nodes["analyze"].Output != "fix-plan ready" {
		t.Fatalf("live stream stripped node output: %+v", live.Nodes["analyze"])
	}
	if len(live.Events) != 0 || live.Artifacts[0].Content != "" || live.Artifacts[0].Name != "fix-plan.md" {
		t.Fatalf("live stream sanitization is incorrect: events=%d artifact=%+v", len(live.Events), live.Artifacts[0])
	}
	if live.ToolCalls != 76 || live.Usage.TotalTokens != 2100 {
		t.Fatalf("live stream dropped usage: %+v", live)
	}
	summary := publicRun(run, true)
	if summary.Nodes["analyze"].Output != "" {
		t.Fatal("run list summary should still omit node output")
	}
}

func TestPublicRunOmitsLogsAndTelemetry(t *testing.T) {
	run := domain.Run{ID: "run-early-logs"}
	for i := 0; i < 200; i++ {
		run.Logs = append(run.Logs, domain.LogRecord{ID: "c-" + strconv.Itoa(i), Sequence: int64(i + 1), Body: "comments log", Context: domain.TelemetryContext{NodeID: "comments"}})
		run.Telemetry = append(run.Telemetry, domain.TelemetryEvent{ID: "ct-" + strconv.Itoa(i), Sequence: int64(i + 1), NodeID: "comments", Name: "tool.call"})
	}
	detail := publicRun(run, false)
	if len(detail.Logs) != 0 || len(detail.Telemetry) != 0 {
		t.Fatalf("run detail still embeds logs/telemetry: logs=%d telemetry=%d", len(detail.Logs), len(detail.Telemetry))
	}
}

func TestPublicRunStripsWorkflowScripts(t *testing.T) {
	run := domain.Run{ID: "run-slim-wf", WorkflowSnapshot: domain.WorkflowDefinition{ID: "wf", Name: "SOP", Nodes: []domain.WorkflowNode{{ID: "fix", Name: "修复", Script: "secret script"}}}}
	detail := publicRun(run, false)
	if detail.WorkflowSnapshot.Nodes[0].Script != "" {
		t.Fatalf("workflow script leaked into public run: %+v", detail.WorkflowSnapshot.Nodes[0])
	}
	if detail.WorkflowSnapshot.Nodes[0].Name != "修复" {
		t.Fatalf("node name stripped: %+v", detail.WorkflowSnapshot.Nodes[0])
	}
}

func TestSlimTelemetryPreservesSafeHookIdentity(t *testing.T) {
	event := domain.TelemetryEvent{
		Message:    "Audit · hook: session stop",
		Source:     domain.TelemetrySource{Layer: "executor", Adapter: "agent-hooks-v1", Provider: "codex", Protocol: "hooks"},
		Attributes: map[string]any{"hook": "Stop", "hookId": "audit", "hookName": "Audit", "tool": "", "durationMs": int64(17), "exitCode": int64(0), "stdout": "done", "input": "secret"},
	}
	got := slimTelemetryEvent(event)
	if got.Source.Layer != "executor" || got.Source.Provider != "codex" || got.Attributes["hookId"] != "audit" {
		t.Fatalf("hook identity stripped: %+v", got)
	}
	if got.Attributes["durationMs"] != int64(17) || got.Attributes["stdout"] != "done" {
		t.Fatalf("hook result stripped: %+v", got)
	}
	if _, ok := got.Attributes["input"]; ok {
		t.Fatalf("unsafe hook input was retained: %+v", got.Attributes)
	}
}

func TestRunLogsArePagedAndOmittedFromDetail(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	run := domain.Run{ID: "run-page", WorkflowID: "wf", Status: "succeeded", Nodes: map[string]*domain.NodeRun{"fix": {NodeID: "fix", Status: "succeeded"}}, LogSequence: 150}
	for i := 1; i <= 150; i++ {
		run.Logs = append(run.Logs, domain.LogRecord{ID: "l" + strconv.Itoa(i), Sequence: int64(i), Body: "line", Context: domain.TelemetryContext{NodeID: "fix"}})
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	detail := requestJSON[domain.Run](t, h, http.MethodGet, "/api/runs/run-page", nil, http.StatusOK)
	if len(detail.Logs) != 0 {
		t.Fatalf("run detail still embeds logs: %d", len(detail.Logs))
	}
	page := requestJSON[struct {
		Logs       []domain.LogRecord `json:"logs"`
		HasMore    bool               `json:"hasMore"`
		NextBefore int64              `json:"nextBefore"`
	}](t, h, http.MethodGet, "/api/runs/run-page/logs?nodeId=fix&limit=100", nil, http.StatusOK)
	if !page.HasMore || len(page.Logs) != 100 || page.Logs[0].Sequence != 51 || page.Logs[99].Sequence != 150 {
		t.Fatalf("unexpected first page: n=%d hasMore=%v first=%v last=%v", len(page.Logs), page.HasMore, page.Logs, page.NextBefore)
	}
	older := requestJSON[struct {
		Logs    []domain.LogRecord `json:"logs"`
		HasMore bool               `json:"hasMore"`
	}](t, h, http.MethodGet, "/api/runs/run-page/logs?nodeId=fix&limit=100&before="+strconv.FormatInt(page.NextBefore, 10), nil, http.StatusOK)
	if older.HasMore || len(older.Logs) != 50 || older.Logs[0].Sequence != 1 {
		t.Fatalf("unexpected older page: n=%d hasMore=%v first=%d", len(older.Logs), older.HasMore, older.Logs[0].Sequence)
	}
}

func TestRunLogsDumpExportsAllNodes(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	run := domain.Run{ID: "run-dump", WorkflowID: "wf", Status: "succeeded", Nodes: map[string]*domain.NodeRun{"analyze": {NodeID: "analyze"}, "fix": {NodeID: "fix"}}, LogSequence: 3}
	run.Logs = []domain.LogRecord{
		{Sequence: 1, Timestamp: time.Date(2026, 8, 19, 15, 0, 0, 0, time.Local), Stream: "worker", Body: "analyze start", Context: domain.TelemetryContext{NodeID: "analyze"}},
		{Sequence: 2, Timestamp: time.Date(2026, 8, 19, 15, 1, 0, 0, time.Local), Stream: "worker", Body: "fix bash git status", Context: domain.TelemetryContext{NodeID: "fix"}},
		{Sequence: 3, Timestamp: time.Date(2026, 8, 19, 15, 2, 0, 0, time.Local), Stream: "worker", Body: "fix done", Context: domain.TelemetryContext{NodeID: "fix"}},
	}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/runs/run-dump/logs.txt", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "analyze start") || !strings.Contains(body, "fix bash git status") || !strings.Contains(body, "count=3") {
		t.Fatalf("dump missing logs:\n%s", body)
	}
	fixOnly := httptest.NewRecorder()
	h.ServeHTTP(fixOnly, httptest.NewRequest(http.MethodGet, "/api/runs/run-dump/logs.txt?nodeId=fix", nil))
	if strings.Contains(fixOnly.Body.String(), "analyze start") || !strings.Contains(fixOnly.Body.String(), "fix done") {
		t.Fatalf("node filter failed:\n%s", fixOnly.Body.String())
	}
}

func TestGzipJSONResponses(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("missing gzip: %v", rec.Header())
	}
}

func TestStandardWorkerIngressSeparatesRealtimeLogs(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "signal-test", Name: "Signal test", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "gate", Name: "Gate", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/runs/"+run.ID+"/signals", domain.WorkerEnvelope{SpecVersion: domain.ObservabilitySpecVersion, Type: "log", Log: &domain.LogRecord{Level: "info", Stream: "stdout", Body: "worker ready", Source: domain.TelemetrySource{Layer: "executor", Adapter: "remote-sdk-v1"}, Context: domain.TelemetryContext{NodeID: "gate"}}}, http.StatusAccepted)
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/runs/"+run.ID+"/signals", domain.WorkerEnvelope{SpecVersion: domain.ObservabilitySpecVersion, Type: "observation", Observation: &domain.TelemetryEvent{Category: "delivery", Name: "mr", Phase: "finish", Message: "MR created", Attributes: map[string]any{"stage": "mr", "status": "passed", "url": "https://git.example.test/repo/merge_requests/1", "sha": "abc123"}, Source: domain.TelemetrySource{Layer: "executor", Adapter: "gongfeng-v1"}, Context: domain.TelemetryContext{NodeID: "gate"}}}, http.StatusAccepted)
	logs := requestJSON[struct {
		Sequence int64              `json:"sequence"`
		Logs     []domain.LogRecord `json:"logs"`
	}](t, h, http.MethodGet, "/api/runs/"+run.ID+"/logs?after=0", nil, http.StatusOK)
	var ready, delivery bool
	for _, log := range logs.Logs {
		ready = ready || log.Body == "worker ready" && log.Context.TraceID == run.ID
		delivery = delivery || log.Attributes["category"] == "delivery" && log.Body == "MR created"
	}
	if logs.Sequence < 2 || !ready || !delivery {
		t.Fatalf("standard log ingress failed: %+v", logs)
	}
	spec := requestJSON[domain.ObservabilitySpec](t, h, http.MethodGet, "/api/observability/spec", nil, http.StatusOK)
	hasDelivery := false
	for _, category := range spec.Categories {
		hasDelivery = hasDelivery || category == "delivery"
	}
	if spec.Version != domain.ObservabilitySpecVersion || len(spec.Adapters) < 3 || !hasDelivery {
		t.Fatalf("observability spec endpoint incomplete: %+v", spec)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := s.Run(run.ID)
		if current.Status == "waiting" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("signal test run did not settle")
}

func TestIssueStatusFromLatestTask(t *testing.T) {
	manuallyClosed := domain.Issue{Status: "closed", DeliveryState: "merged", DeliveryMRState: "closed"}
	if got := issueStatusFromLatestTask(manuallyClosed, "running"); got != "closed" {
		t.Fatalf("manually closed issue must stay closed, got %q", got)
	}
	closed := domain.Issue{Source: "gongfeng", Status: "blocked", ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalState: "closed"}}
	if got := issueStatusFromLatestTask(closed, "completed"); got != "closed" {
		t.Fatalf("closed Gongfeng issue should stay closed, got %q", got)
	}
	tapdClosed := domain.Issue{Source: "tapd", Status: "blocked", ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalState: "已关闭"}}
	if got := issueStatusFromLatestTask(tapdClosed, "running"); got != "closed" {
		t.Fatalf("closed TAPD issue should stay closed, got %q", got)
	}
	open := domain.Issue{Status: "blocked"}
	if got := issueStatusFromLatestTask(open, "completed"); got != "in_review" {
		t.Fatalf("latest succeeded SOP should move blocked issue to in_review, got %q", got)
	}
	if got := issueStatusFromLatestTask(domain.Issue{Status: "todo"}, "failed"); got != "blocked" {
		t.Fatalf("latest failed SOP should block, got %q", got)
	}
	if got := issueStatusFromLatestTask(domain.Issue{Status: "blocked"}, "running"); got != "in_progress" {
		t.Fatalf("latest running SOP should be in_progress, got %q", got)
	}
	locked := domain.Issue{Status: "done", DeliveryState: "merged", DeliveryMRState: "integrated"}
	if got := issueStatusFromLatestTask(locked, "cancelled"); got != "done" {
		t.Fatalf("baseline-locked issue must stay done after cancelled task, got %q", got)
	}
}

func TestTaskStatusFromRun(t *testing.T) {
	for _, tc := range []struct{ run, task string }{
		{"queued", "queued"},
		{"running", "running"},
		{"waiting", "running"},
		{"succeeded", "completed"},
		{"failed", "failed"},
		{"cancelled", "cancelled"},
		{"unknown", ""},
	} {
		t.Run(tc.run, func(t *testing.T) {
			if got := taskStatusFromRun(tc.run); got != tc.task {
				t.Fatalf("taskStatusFromRun(%q) = %q, want %q", tc.run, got, tc.task)
			}
		})
	}
}

func TestSyncTasksMapsQueuedRun(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutIssue(domain.Issue{ID: "queued-issue", ProjectID: "platform", Title: "Queued", Status: "in_progress"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{ID: "queued-run", Status: "queued", ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "queued-task", IssueID: "queued-issue", RunID: "queued-run", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	h := &tasksHandlers{deps: &deps{repo: s}}
	h.syncTasks()
	if task, ok := s.Task("queued-task"); !ok || task.Status != "queued" {
		t.Fatalf("queued run did not synchronize task: %+v, found=%v", task, ok)
	}
	if issue, ok := s.Issue("queued-issue"); !ok || issue.LatestTaskID != "queued-task" || issue.Status != "in_progress" {
		t.Fatalf("queued task lost issue linkage: %+v, found=%v", issue, ok)
	}
	run, _ := s.Run("queued-run")
	run.Status = "succeeded"
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	h.syncTasksAt = time.Time{}
	h.syncTasks()
	if task, ok := s.Task("queued-task"); !ok || task.Status != "completed" {
		t.Fatalf("succeeded run did not synchronize task: %+v, found=%v", task, ok)
	}
}

func TestIssueDispatchReturnsQueuedTask(t *testing.T) {
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	workflow := domain.WorkflowDefinition{ID: "dispatch-queued", Name: "Queued dispatch", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < repo.ProjectRunLimit; i++ {
		if err := s.PutRun(domain.Run{ID: fmt.Sprintf("occupied-%d", i), Status: "running", ExecutionContext: domain.TelemetryContext{ProjectID: "platform"}, Nodes: map[string]*domain.NodeRun{}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutIssue(domain.Issue{ID: "dispatch-queued-issue", ProjectID: "platform", Title: "Queued dispatch", Status: "todo", WorkflowID: workflow.ID}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(t.TempDir(), "workspaces")))
	e.ConfigureExecution("queued-dispatch-api", "", time.Minute, false)
	task, err := dispatchIssue(&deps{repo: s, engine: e}, dispatchRequest{IssueID: "dispatch-queued-issue"})
	if err != nil {
		t.Fatal(err)
	}
	run, ok := s.RunState(task.RunID)
	if !ok || run.Status != "queued" || task.Status != "queued" || run.QueueEnteredAt.IsZero() {
		t.Fatalf("dispatch must reflect admitted run state: task=%+v run=%+v found=%v", task, run, ok)
	}
}

func TestSyncTasksBindsIssueToLatestSOP(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")

	if err := s.PutIssue(domain.Issue{ID: "gf-5", ProjectID: "platform", Title: "count", Status: "blocked", LatestTaskID: "task-old"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "task-old", IssueID: "gf-5", RunID: "run-old", Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{ID: "run-old", Status: "failed", Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "task-new", IssueID: "gf-5", RunID: "run-new", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{ID: "run-new", Status: "succeeded", ExecutionContext: domain.TelemetryContext{IssueID: "gf-5", TaskID: "task-new"}, Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}

	if err := s.PutIssue(domain.Issue{ID: "gf-20", ProjectID: "platform", Title: "cos", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "task-20", IssueID: "gf-20", RunID: "run-20", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{ID: "run-20", Status: "succeeded", ExecutionContext: domain.TelemetryContext{IssueID: "gf-20"}, Nodes: map[string]*domain.NodeRun{}}); err != nil {
		t.Fatal(err)
	}

	// Regression: deleting a cancelled Run used to leave its Task in running
	// forever.  The issue list then showed “正在启动中” even though the Run no
	// longer existed.
	if err := s.PutIssue(domain.Issue{ID: "deleted-run-issue", ProjectID: "platform", Title: "deleted run", Status: "in_progress"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "deleted-run-task", IssueID: "deleted-run-issue", RunID: "deleted-run", Status: "running"}); err != nil {
		t.Fatal(err)
	}

	issues := requestJSON[[]domain.Issue](t, h, http.MethodGet, "/api/issues", nil, http.StatusOK)
	got := map[string]domain.Issue{}
	for _, issue := range issues {
		got[issue.ID] = issue
	}
	if gf5 := got["gf-5"]; gf5.LatestTaskID != "task-new" || gf5.Status != "in_review" {
		t.Fatalf("gf-5 not bound to latest SOP: %+v", gf5)
	}
	if gf20 := got["gf-20"]; gf20.LatestTaskID != "task-20" || gf20.Status != "in_review" {
		t.Fatalf("gf-20 still looks unstarted: %+v", gf20)
	}
	if deleted := got["deleted-run-issue"]; deleted.LatestTaskID != "deleted-run-task" || deleted.Status != "cancelled" {
		t.Fatalf("deleted run relation did not converge: %+v", deleted)
	}
	if task, ok := s.Task("deleted-run-task"); !ok || task.Status != "cancelled" {
		t.Fatalf("dangling task did not converge to cancelled: %+v, found=%v", task, ok)
	}
}

func TestIssueListFiltersAndPages(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	now := time.Now().UTC()
	if err := s.PutProject(domain.Project{ID: "list-filter", Key: "LFLT", Name: "List Filter"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "lflt-backlog", ProjectID: "list-filter", Number: 11, Title: "扫描超包-unique", Status: "backlog", Source: "tapd", UpdatedAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{ID: "lflt-todo", ProjectID: "list-filter", Number: 12, Title: "评审清单-unique", Status: "todo", Source: "gongfeng", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	all := requestJSON[[]domain.Issue](t, h, http.MethodGet, "/api/issues", nil, http.StatusOK)
	if len(all) < 2 {
		t.Fatalf("unfiltered GET must stay an array: %d", len(all))
	}

	filtered := requestJSON[[]domain.Issue](t, h, http.MethodGet, "/api/issues?projectId=list-filter&status=backlog", nil, http.StatusOK)
	if len(filtered) != 1 || filtered[0].ID != "lflt-backlog" {
		t.Fatalf("status filter: %+v", filtered)
	}

	page := requestJSON[issueListPage](t, h, http.MethodGet, "/api/issues?projectId=list-filter&limit=1&offset=0", nil, http.StatusOK)
	if page.Limit != 1 || page.Offset != 0 || len(page.Items) != 1 || page.Items[0].ID != "lflt-todo" || !page.HasMore || page.Total != 2 {
		t.Fatalf("page: %+v", page)
	}
	next := requestJSON[issueListPage](t, h, http.MethodGet, "/api/issues?projectId=list-filter&q=扫描超包-unique&limit=10", nil, http.StatusOK)
	if next.Total != 1 || len(next.Items) != 1 || next.Items[0].ID != "lflt-backlog" || next.HasMore {
		t.Fatalf("q page: %+v", next)
	}
	fuzzyTitle := requestJSON[issueListPage](t, h, http.MethodGet, "/api/issues?projectId=list-filter&q=超%20包%20扫描&limit=10", nil, http.StatusOK)
	if fuzzyTitle.Total != 1 || len(fuzzyTitle.Items) != 1 || fuzzyTitle.Items[0].ID != "lflt-backlog" {
		t.Fatalf("fuzzy title page: %+v", fuzzyTitle)
	}
	source := requestJSON[issueListPage](t, h, http.MethodGet, "/api/issues?projectId=list-filter&source=gongfeng&limit=20", nil, http.StatusOK)
	if source.Total != 1 || source.Items[0].ID != "lflt-todo" {
		t.Fatalf("source page: %+v", source)
	}
	requestJSON[map[string]any](t, h, http.MethodGet, "/api/issues?limit=abc", nil, http.StatusBadRequest)
}

func TestGongfengClosedIssueRejectsDispatch(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "issue-fix-production", Name: "Issue fix", Version: 1, SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")

	closed := domain.Issue{
		ID: "gf-closed", ProjectID: "platform", Source: "gongfeng", Kind: "issue", Title: "Already closed on Gongfeng",
		Status: "blocked", WorkflowID: "issue-fix-production",
		ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalID: "7", ExternalIID: 7, ExternalState: "closed"},
	}
	if err := s.PutIssue(closed); err != nil {
		t.Fatal(err)
	}
	resp := requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/"+closed.ID+"/dispatch", map[string]any{}, http.StatusConflict)
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "已关闭") {
		t.Fatalf("unexpected error: %+v", resp)
	}
	stored, _ := s.Issue(closed.ID)
	if stored.Status != "closed" || stored.CloseSource != "external" || stored.LatestTaskID != "" {
		t.Fatalf("closed issue must not start a run: %+v", stored)
	}

	openIssue := domain.Issue{
		ID: "gf-open", ProjectID: "platform", Source: "gongfeng", Kind: "issue", Title: "Still open",
		Status: "todo", WorkflowID: "issue-fix-production",
		ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalID: "8", ExternalIID: 8, ExternalState: "opened"},
	}
	if err := s.PutIssue(openIssue); err != nil {
		t.Fatal(err)
	}
	task := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+openIssue.ID+"/dispatch", map[string]any{}, http.StatusCreated)
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
	if task.IssueID != openIssue.ID || task.RunID == "" {
		t.Fatalf("opened Gongfeng issue should still dispatch: %+v", task)
	}
}

func TestGongfengClosedIssueRejectsRetry(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")

	closed := domain.Issue{
		ID: "gf-closed-retry", ProjectID: "platform", Source: "gongfeng", Kind: "issue", Title: "closed but failed",
		Status: "blocked", WorkflowID: "issue-fix-production", LatestTaskID: "task-failed",
		ExternalRef: &domain.ExternalRef{Source: "gongfeng", ExternalID: "9", ExternalIID: 9, ExternalState: "closed"},
	}
	if err := s.PutIssue(closed); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "task-failed", IssueID: closed.ID, RunID: "run-failed", Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(domain.Run{
		ID: "run-failed", Status: "failed", WorkflowID: "issue-fix-production",
		ExecutionContext: domain.TelemetryContext{IssueID: closed.ID, TaskID: "task-failed"},
		Nodes:            map[string]*domain.NodeRun{"fix": {NodeID: "fix", Status: "failed", Attempt: 1}},
		WorkflowSnapshot: domain.WorkflowDefinition{ID: "issue-fix-production", Nodes: []domain.WorkflowNode{{ID: "fix", Name: "Fix", Type: "agent", MaxAttempts: 1}}},
	}); err != nil {
		t.Fatal(err)
	}

	resp := requestJSON[map[string]any](t, h, http.MethodPost, "/api/runs/run-failed/retry", map[string]any{"reason": "from UI"}, http.StatusConflict)
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "已关闭") {
		t.Fatalf("retry on closed issue must 409, got %+v", resp)
	}
	run, _ := s.Run("run-failed")
	if run.Status != "failed" {
		t.Fatalf("closed issue retry must not resume run: %s", run.Status)
	}
}

func TestMergedTAPDDeliveryRequiresExplicitReopen(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	wf := domain.WorkflowDefinition{ID: "requirement-development-production", Name: "Requirement", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	issue := domain.Issue{ID: "tapd-merged", ProjectID: "platform", Source: "tapd", Kind: "requirement", Title: "Delivered", Status: "done", WorkflowID: wf.ID, LatestTaskID: "task-old", DeliveryState: "merged", DeliveryMRURL: "https://git.example.com/org/repo/merge_requests/9", AutomationState: "ignored", Clarification: domain.ClarificationAssessment{Passed: true, Score: 100}}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTask(domain.Task{ID: "task-old", IssueID: issue.ID, RunID: "run-old", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	blocked := requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]any{"rerun": true}, http.StatusConflict)
	if !strings.Contains(blocked["error"].(string), "重新打开") {
		t.Fatalf("unexpected dispatch error: %+v", blocked)
	}
	reopened := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/reopen", map[string]any{}, http.StatusOK)
	if reopened.DeliveryState != "reopened" || reopened.Status != "todo" {
		t.Fatalf("unexpected reopened issue: %+v", reopened)
	}
	if err := s.MarkIssueDelivered(issue.ID, issue.DeliveryMRURL, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	stillOpen, _ := s.Issue(issue.ID)
	if stillOpen.DeliveryState != "reopened" {
		t.Fatalf("newer MR check must not relock a reopened issue: %+v", stillOpen)
	}
	task := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]any{"rerun": true}, http.StatusCreated)
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
	if task.RunID == "" || task.IssueID != issue.ID {
		t.Fatalf("reopened issue did not dispatch: %+v", task)
	}
}

func TestRequirementMustPassConfigurableClarificationBeforeDispatch(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "requirement-gated", Name: "Requirement gated", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	if err := s.PutIssue(domain.Issue{ID: "req-gated", ProjectID: "platform", Kind: "requirement", Title: "Build requirement", AssigneeType: "agent", AssigneeID: "developer", WorkflowID: wf.ID, Clarification: domain.ClarificationAssessment{Gated: true}}); err != nil {
		t.Fatal(err)
	}
	issue, _ := s.Issue("req-gated")
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]any{}, http.StatusConflict)
	partial := requestJSON[domain.ClarificationAssessment](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/clarify", map[string]any{"answers": map[string]string{"goal": "short"}}, http.StatusOK)
	if partial.Passed || partial.Score >= 80 {
		t.Fatalf("partial clarification unexpectedly passed: %+v", partial)
	}
	long := strings.Repeat("完整且可验证的需求信息", 8)
	passed := requestJSON[domain.ClarificationAssessment](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/clarify", map[string]any{"answers": map[string]string{"goal": long, "user": long, "scope": long, "acceptance": long, "constraints": long}}, http.StatusOK)
	if !passed.Passed || passed.Score != 100 {
		t.Fatalf("complete clarification did not pass: %+v", passed)
	}
	task := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]any{}, http.StatusCreated)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, _ := s.Run(task.RunID)
		if run.Status == "waiting" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("clarified requirement run did not settle")
}

func TestScheduledDispatchAlsoRequiresClarification(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "req-sched-wf", Name: "Req", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{
		ID: "req-sched-gate", ProjectID: "platform", Kind: "requirement", Title: "Thin TAPD",
		WorkflowID: wf.ID, Clarification: domain.ClarificationAssessment{Score: 20, Status: domain.ClarificationNeedsUser, Gated: true},
	}); err != nil {
		t.Fatal(err)
	}
	d := &deps{repo: s, engine: engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), sync: tracker.NewService()}
	_, err = dispatchIssue(d, dispatchRequest{IssueID: "req-sched-gate", WorkflowID: wf.ID, ScheduleJobID: "job-1"})
	httpErr, ok := err.(HTTPError)
	if !ok || httpErr.Status != http.StatusConflict || !strings.Contains(httpErr.Message, "澄清未达标") {
		t.Fatalf("scheduled dispatch must stay gated: %v", err)
	}
}

func TestSyncedRequirementCanDispatchWithoutClarification(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "req-sync-wf", Name: "Req", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{
		ID: "req-sync", ProjectID: "platform", Kind: "requirement", Source: "tapd", Title: "mq_flush_dns CMQ SDK",
		WorkflowID: wf.ID, Clarification: domain.ClarificationAssessment{Score: 0, Status: domain.ClarificationNeedsUser},
	}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	d := &deps{repo: s, engine: e, sync: tracker.NewService()}
	task, err := dispatchIssue(d, dispatchRequest{IssueID: "req-sync", WorkflowID: wf.ID, ScheduleJobID: "job-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
	if task.RunID == "" {
		t.Fatal("synced requirement must dispatch")
	}
}

func TestReadyClarificationCanDispatchBelowThreshold(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "req-ready-wf", Name: "Req", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval"}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{
		ID: "req-ready-low", ProjectID: "platform", Kind: "requirement", Title: "Low score but clarified",
		WorkflowID: wf.ID,
		Clarification: domain.ClarificationAssessment{
			Gated: true, Score: 76, Passed: false, Status: domain.ClarificationReady, Summary: "已确认 insert/delete 语义",
		},
	}); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	d := &deps{repo: s, engine: e, sync: tracker.NewService()}
	task, err := dispatchIssue(d, dispatchRequest{IssueID: "req-ready-low", WorkflowID: wf.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
	if task.RunID == "" {
		t.Fatal("finished clarification must dispatch even below 80")
	}
}

func TestAppendClarificationDescriptionReplacesPreviousBlock(t *testing.T) {
	got := appendClarificationDescription("原描述", "第一轮结论")
	if !strings.Contains(got, "原描述") || !strings.Contains(got, "第一轮结论") {
		t.Fatalf("got %q", got)
	}
	got = appendClarificationDescription(got, "第二轮结论")
	if strings.Count(got, "## AgentFlow 澄清结论") != 1 || strings.Contains(got, "第一轮结论") || !strings.Contains(got, "第二轮结论") {
		t.Fatalf("replace failed: %q", got)
	}
}

func TestExistingIssueClarificationDoesNotStartOrBlockSOP(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "issue-flow", Name: "Issue flow", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "gf-existing", ProjectID: "platform", Kind: "issue", Source: "gongfeng", Title: "unclear bug", WorkflowID: wf.ID}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	updated := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/clarification", map[string]any{
		"summary": "复现条件和预期行为已经确认", "ready": true,
		"history": []map[string]string{{"role": "user", "content": "仅在首次加载时发生"}},
	}, http.StatusOK)
	if !updated.Clarification.Passed || updated.Clarification.Status != "ready" || updated.Clarification.WriteBackStatus != "skipped" {
		t.Fatalf("unexpected clarification: %+v", updated.Clarification)
	}
	if !strings.Contains(updated.Description, "复现条件和预期行为已经确认") {
		t.Fatalf("platform description missing conclusion: %q", updated.Description)
	}
	if len(s.Tasks()) != 0 {
		t.Fatal("clarification must not start an SOP")
	}
	retried := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/clarification", map[string]any{
		"ready": true, "retry": true,
	}, http.StatusOK)
	if retried.Clarification.WriteBackStatus != "skipped" {
		t.Fatalf("retry should keep local-only clarification skipped: %+v", retried.Clarification)
	}
	task := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]any{}, http.StatusCreated)
	if task.RunID == "" {
		t.Fatal("existing issue SOP should still dispatch normally")
	}
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
}

func TestClarificationScoreIsWeightedAndNeverGatesDispatch(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "issue-flow", Name: "Issue flow", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "gf-rated", ProjectID: "platform", Kind: "issue", Source: "gongfeng", Title: "partial info", WorkflowID: wf.ID}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	// Bug-style weights are 25/20/25/20/10; "acceptance" is left unrated on
	// purpose and must count as zero rather than being dropped.
	updated := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/clarification", map[string]any{
		"summary": "现象和期望行为已确认，验收标准仍缺", "ready": true,
		"dimensions": map[string]int{"phenomenon": 100, "impact": 50, "expected": 80, "constraints": 100},
		"missing":    []string{"验收标准还没定", "  "},
	}, http.StatusOK)
	if updated.Clarification.Score != 65 {
		t.Fatalf("weighted score = %d, want 65 (%+v)", updated.Clarification.Score, updated.Clarification.Dimensions)
	}
	if got, ok := updated.Clarification.Dimensions["acceptance"]; !ok || got != 0 {
		t.Fatalf("unrated dimension should be stored as 0: %+v", updated.Clarification.Dimensions)
	}
	if len(updated.Clarification.Missing) != 1 || updated.Clarification.Missing[0] != "验收标准还没定" {
		t.Fatalf("missing items not trimmed: %+v", updated.Clarification.Missing)
	}
	if !updated.Clarification.Passed {
		t.Fatalf("bug-style clarification still marks the item clarified: %+v", updated.Clarification)
	}
	task := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]any{}, http.StatusCreated)
	if task.RunID == "" {
		t.Fatal("low clarification score must not block dispatch")
	}
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
}

func TestRequirementChatClarificationEmptyRatingsDoNotPass(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	wf := domain.WorkflowDefinition{ID: "req-flow", Name: "Req flow", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	issue := domain.Issue{ID: "tapd-rated", ProjectID: "platform", Kind: "requirement", Source: "tapd", Title: "partial req", WorkflowID: wf.ID}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	e := engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces")))
	h := New(s, e, tracker.NewService(), "", "")
	updated := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/clarification", map[string]any{
		"summary": "还缺验收和范围", "ready": true,
	}, http.StatusOK)
	if updated.Clarification.Passed || updated.Clarification.Score != 0 {
		t.Fatalf("empty ratings must not pass: %+v", updated.Clarification)
	}
	if len(updated.Clarification.Dimensions) == 0 {
		t.Fatal("empty ratings must still store every dimension as 0")
	}
	if updated.Clarification.Status != domain.ClarificationReady {
		t.Fatalf("status=%s", updated.Clarification.Status)
	}
	if !strings.Contains(updated.Description, "还缺验收和范围") {
		t.Fatalf("platform description missing conclusion: %q", updated.Description)
	}
	task := requestJSON[domain.Task](t, h, http.MethodPost, "/api/issues/"+issue.ID+"/dispatch", map[string]any{}, http.StatusCreated)
	if task.RunID == "" {
		t.Fatal("ready clarification must dispatch even with empty ratings")
	}
	t.Cleanup(func() { _ = e.Cancel(task.RunID) })
}

func TestCreateLocalIssueWithoutSyncTarget(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	created := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "platform", "title": "local only", "description": "用户自己写的描述",
	}, http.StatusOK)
	if created.Source != "local" || created.Kind != "requirement" || created.Title != "local only" {
		t.Fatalf("local issue: %+v", created)
	}
	if created.ExternalRef != nil {
		t.Fatalf("local issue must not bind tracker: %+v", created.ExternalRef)
	}
	if created.Clarification.Gated {
		t.Fatalf("direct local create must not intake-score: %+v", created.Clarification)
	}
	imported := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId": "platform", "title": "from gongfeng", "source": "gongfeng",
		"externalRef": map[string]any{"source": "gongfeng", "externalId": "99", "externalIid": 18},
	}, http.StatusOK)
	if imported.Source != "gongfeng" || imported.ExternalRef == nil || imported.ExternalRef.ExternalIID != 18 {
		t.Fatalf("imported issue: %+v", imported)
	}
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues", map[string]any{"projectId": "platform", "title": "push me", "syncTarget": "gongfeng"}, http.StatusBadRequest)
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues", map[string]any{"projectId": "platform", "title": "bad dest", "syncTarget": "jira"}, http.StatusBadRequest)
}

func TestPushLocalIssueToTAPDKeepsDescription(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	owner, ok := s.User(1)
	if !ok {
		t.Fatal("seed owner missing")
	}
	if _, err := s.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{TaihuPAT: "tai_pat_push"}); err != nil {
		t.Fatal(err)
	}
	h := newTAPDImportHandler(t, s, root, `{"id":"1010000000888888888","name":"本地推送","status":"open"}`)
	local := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues", map[string]any{
		"projectId":   "platform",
		"title":       "直接创建的需求",
		"description": "用户自己写的完整描述，不需要平台再分析。",
	}, http.StatusOK)
	if local.Source != "local" || local.Clarification.Gated {
		t.Fatalf("local create: %+v", local)
	}
	pushed := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/sync/create", map[string]any{
		"issueId": local.ID, "target": "tapd",
	}, http.StatusOK)
	if pushed.Source != "tapd" || pushed.ExternalRef == nil || pushed.ExternalRef.ExternalID != "1010000000888888888" {
		t.Fatalf("tapd push: %+v", pushed)
	}
	if pushed.Description != "用户自己写的完整描述，不需要平台再分析。" {
		t.Fatalf("description rewritten on tapd push: %q", pushed.Description)
	}
	if pushed.Clarification.Gated {
		t.Fatalf("push of existing local ticket must not intake-score: %+v", pushed.Clarification)
	}
	if _, stillLocal := s.Issue(local.ID); stillLocal && local.ID != pushed.ID {
		t.Fatalf("old local id %q should be replaced by %q", local.ID, pushed.ID)
	}
}

func TestImportTAPDIssueFromURL(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{StaffID: 3101, LoginName: "tapd-owner", ChineseName: "导入人"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := s.UserByLoginName("tapd-owner")
	if _, err := s.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{TaihuPAT: "tai_pat_import"}); err != nil {
		t.Fatal(err)
	}
	project, _ := s.Project("platform")
	project.OwnerID = owner.ID
	if err := s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	repoID := putPlatformImportRepo(t, s)
	payload := `{"data":[{"id":"1010000000100000555","name":"国际站取回绑定套餐域名逻辑对齐国内站","status":"status_38","priority":"3"}]}`
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"mock","version":"1"}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			escaped, _ := json.Marshal(payload)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":%s}]}}`, req.ID, escaped)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	t.Cleanup(gateway.Close)
	if err := s.PutMCPServer(domain.MCPServerDefinition{ID: "mcp-70-project-platform", Name: "TAPD MCP", ProjectID: "platform", MarketID: "70", Scope: "project", Transport: "http", URL: gateway.URL, AuthType: "taihu-pat", TimeoutSec: 30}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSyncConfig(domain.SyncConfig{TAPDMCPURL: gateway.URL, AutoSyncIntervalSec: 300}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/import-tapd", map[string]any{"projectId": "platform"}, http.StatusBadRequest)
	imported := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", map[string]any{
		"projectId":    "platform",
		"url":          "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100000555",
		"repositoryId": repoID,
	}, http.StatusOK)
	if imported.Source != "tapd" || imported.Title != "国际站取回绑定套餐域名逻辑对齐国内站" || imported.ExternalRef == nil || imported.ExternalRef.ExternalID != "1010000000100000555" {
		t.Fatalf("imported=%+v", imported)
	}
	if imported.AssigneeUserID != owner.ID {
		t.Fatalf("assignee=%d want project owner %d when TAPD handler is missing", imported.AssigneeUserID, owner.ID)
	}
	if imported.Clarification.Passed || imported.Clarification.Status != domain.ClarificationNeedsUser || !imported.Clarification.Gated {
		t.Fatalf("imported clarification=%+v", imported.Clarification)
	}
	if len(imported.Clarification.Dimensions) == 0 {
		t.Fatal("import must persist per-dimension scores")
	}
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/"+imported.ID+"/dispatch", map[string]any{}, http.StatusConflict)
}

func TestImportTAPDIssueSaveKeepsExternalBinding(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{StaffID: 3101, LoginName: "tapd-owner", ChineseName: "导入人"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := s.UserByLoginName("tapd-owner")
	if _, err := s.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{TaihuPAT: "tai_pat_import"}); err != nil {
		t.Fatal(err)
	}
	project, _ := s.Project("platform")
	project.OwnerID = owner.ID
	if err := s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	repoID := putPlatformImportRepo(t, s)
	h := newTAPDImportHandler(t, s, root, `{"data":[{"id":"1010000000100000555","name":"导入后改标题","status":"status_38","priority":"3"}]}`)
	imported := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", map[string]any{
		"projectId":    "platform",
		"url":          "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100000555",
		"repositoryId": repoID,
	}, http.StatusOK)
	// Mimic the issue edit form: it spreads list state and can send an empty
	// externalRef object, which used to skip restore and look like a new issue.
	saved := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id":          imported.ID,
		"projectId":   "platform",
		"title":       "改过的标题",
		"description": "本地说明",
		"priority":    "high",
		"status":      "todo",
		"kind":        "requirement",
		"source":      "tapd",
		"externalRef": map[string]any{},
	}, http.StatusOK)
	if saved.Title != "改过的标题" {
		t.Fatalf("title=%q", saved.Title)
	}
	if saved.Source != "tapd" || saved.ExternalRef == nil || saved.ExternalRef.ExternalID != "1010000000100000555" {
		t.Fatalf("tracker binding lost on save: source=%q ref=%+v", saved.Source, saved.ExternalRef)
	}
}

func TestSaveAfterClarificationDoesNotRecreateOrDeleteTAPDIssue(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	issue := domain.Issue{
		ID:        "tapd-1010000000100000888",
		ProjectID: "platform",
		Title:     "澄清后的需求",
		Kind:      "requirement",
		Source:    "tapd",
		Status:    "todo",
		ExternalRef: &domain.ExternalRef{
			Source:       "tapd",
			ExternalID:   "1010000000100000888",
			ExternalType: "story",
		},
		Clarification: domain.ClarificationAssessment{
			Score:   88,
			Passed:  true,
			Gated:   true,
			Status:  domain.ClarificationReady,
			Summary: "已补齐验收标准",
			History: []domain.ClarificationMessage{{Role: "assistant", Content: `{"summary":"ok"}`}},
		},
	}
	if err := s.PutIssue(issue); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	saved := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id":          issue.ID,
		"projectId":   "platform",
		"title":       "澄清后改标题",
		"description": "补充说明",
		"priority":    "high",
		"status":      "todo",
		"kind":        "requirement",
		"syncTarget":  "tapd",
		"externalRef": map[string]any{},
	}, http.StatusOK)
	if saved.ID != issue.ID {
		t.Fatalf("save replaced issue id %q -> %q", issue.ID, saved.ID)
	}
	if _, ok := s.Issue(issue.ID); !ok {
		t.Fatal("original TAPD issue was deleted on save")
	}
	if saved.Source != "tapd" || saved.ExternalRef == nil || saved.ExternalRef.ExternalID != "1010000000100000888" {
		t.Fatalf("binding lost after clarified save: source=%q ref=%+v", saved.Source, saved.ExternalRef)
	}
	if saved.Title != "澄清后改标题" {
		t.Fatalf("title=%q", saved.Title)
	}
	if !saved.Clarification.Passed || saved.Clarification.Summary != "已补齐验收标准" {
		t.Fatalf("clarification wiped on save: %+v", saved.Clarification)
	}
}

func TestIssuesSaveDoesNotGateSyncedTAPDRequirement(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.PutWorkflow(domain.WorkflowDefinition{ID: "dnspod-issue-fix", Name: "Issue fix", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "fix", Name: "Fix", Type: "agent"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRepository(domain.Repository{ID: "repo-new-dnspod", ProjectID: "platform", Name: "new_dnspod", GongfengProjectID: "dnspod/new_dnspod", DefaultBranch: "master"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssue(domain.Issue{
		ID:          "tapd-1010000000100001776",
		ProjectID:   "platform",
		Title:       "mq_flush_dns CMQ SDK",
		Kind:        "requirement",
		Source:      "tapd",
		Status:      "backlog",
		ExternalRef: &domain.ExternalRef{Source: "tapd", ExternalID: "1010000000100001776", ExternalType: "story"},
	}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	saved := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id":            "tapd-1010000000100001776",
		"projectId":     "platform",
		"title":         "mq_flush_dns CMQ SDK",
		"kind":          "requirement",
		"source":        "tapd",
		"workflowId":    "dnspod-issue-fix",
		"repositoryId":  "repo-new-dnspod",
		"repositoryIds": []string{"repo-new-dnspod"},
	}, http.StatusOK)
	if saved.WorkflowID != "dnspod-issue-fix" || saved.RepositoryID != "repo-new-dnspod" {
		t.Fatalf("binding not saved: workflow=%q repo=%q", saved.WorkflowID, saved.RepositoryID)
	}
	if saved.Clarification.Gated || saved.Clarification.Score != 0 || saved.Clarification.Status != "" {
		t.Fatalf("synced TAPD save must not import-score: %+v", saved.Clarification)
	}
}

func TestSaveRestoresTAPDBindingFromStableID(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutIssue(domain.Issue{ID: "tapd-1010000000100000999", ProjectID: "platform", Title: "绑定被清空", Kind: "requirement", Status: "todo"}); err != nil {
		t.Fatal(err)
	}
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	saved := requestJSON[domain.Issue](t, h, http.MethodPut, "/api/issues", map[string]any{
		"id":          "tapd-1010000000100000999",
		"projectId":   "platform",
		"title":       "绑定被清空",
		"kind":        "requirement",
		"syncTarget":  "tapd",
		"externalRef": map[string]any{},
	}, http.StatusOK)
	if saved.ID != "tapd-1010000000100000999" {
		t.Fatalf("id=%q", saved.ID)
	}
	if saved.Source != "tapd" || saved.ExternalRef == nil || saved.ExternalRef.ExternalID != "1010000000100000999" {
		t.Fatalf("stable id did not restore binding: source=%q ref=%+v", saved.Source, saved.ExternalRef)
	}
}

func TestImportTAPDIssueBindsSelectedWorkflow(t *testing.T) {
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutUser(domain.User{StaffID: 3101, LoginName: "tapd-owner", ChineseName: "导入人"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := s.UserByLoginName("tapd-owner")
	if _, err := s.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{TaihuPAT: "tai_pat_import"}); err != nil {
		t.Fatal(err)
	}
	project, _ := s.Project("platform")
	project.OwnerID = owner.ID
	if err := s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	wf := domain.WorkflowDefinition{ID: "custom-req-sop", Name: "定制需求 SOP", ProjectID: "platform", SandboxProfileID: "local", Nodes: []domain.WorkflowNode{{ID: "approval", Name: "Approval", Type: "approval", MaxAttempts: 1}}}
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	h := newTAPDImportHandler(t, s, root, `{"data":[{"id":"1010000000100001221","name":"带 SOP 导入","status":"open"}]}`)
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/import-tapd", map[string]any{
		"projectId":  "platform",
		"url":        "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001221",
		"workflowId": wf.ID,
	}, http.StatusBadRequest)
	for _, issue := range s.Issues() {
		if issue.ExternalRef != nil && issue.ExternalRef.ExternalID == "1010000000100001221" {
			t.Fatal("import without a repository must not persist the TAPD story")
		}
	}
	if err := s.PutRepository(domain.Repository{ID: "repo-import-dnspod", ProjectID: "platform", Name: "new_dnspod", GongfengProjectID: "dnspod/new_dnspod", DefaultBranch: "master"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRepository(domain.Repository{ID: "repo-import-com", ProjectID: "platform", Name: "new_dnspod.com", GongfengProjectID: "dnspod/new_dnspod.com", DefaultBranch: "master"}); err != nil {
		t.Fatal(err)
	}
	imported := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", map[string]any{
		"projectId":     "platform",
		"url":           "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001221",
		"workflowId":    wf.ID,
		"repositoryIds": []string{"repo-import-dnspod"},
	}, http.StatusOK)
	if imported.WorkflowID != wf.ID {
		t.Fatalf("workflowId=%q want %q", imported.WorkflowID, wf.ID)
	}
	bound := requestJSON[domain.Issue](t, h, http.MethodPost, "/api/issues/import-tapd", map[string]any{
		"projectId":     "platform",
		"url":           "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001221",
		"workflowId":    wf.ID,
		"repositoryIds": []string{"repo-import-dnspod", "repo-import-com"},
	}, http.StatusOK)
	if bound.RepositoryID != "repo-import-dnspod" {
		t.Fatalf("repositoryId=%q", bound.RepositoryID)
	}
	if len(bound.RepositoryIDs) != 2 || bound.RepositoryIDs[1] != "repo-import-com" {
		t.Fatalf("repositoryIds=%v", bound.RepositoryIDs)
	}
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/import-tapd", map[string]any{
		"projectId":    "platform",
		"url":          "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001221",
		"repositoryId": "missing-repo",
	}, http.StatusBadRequest)
	requestJSON[map[string]any](t, h, http.MethodPost, "/api/issues/import-tapd", map[string]any{
		"projectId":    "platform",
		"url":          "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100001221",
		"workflowId":   "missing-sop",
		"repositoryId": "repo-import-dnspod",
	}, http.StatusBadRequest)
}

func TestImportTAPDIssueUsesCreatorThenImporter(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "tapd-import-test-session-secret")
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, user := range []domain.User{
		{StaffID: 3101, LoginName: "tapd-owner", ChineseName: "项目负责人"},
		{StaffID: 3102, LoginName: "importer", ChineseName: "导入人"},
		{StaffID: 3103, LoginName: "example-maintainer", ChineseName: "胡锦康"},
	} {
		if err := s.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	owner, _ := s.UserByLoginName("tapd-owner")
	importer, _ := s.UserByLoginName("importer")
	creator, _ := s.UserByLoginName("example-maintainer")
	if _, err := s.UpdateUserCredentials(owner.ID, repo.UserCredentialUpdate{TaihuPAT: "tai_pat_import"}); err != nil {
		t.Fatal(err)
	}
	project, _ := s.Project("platform")
	project.OwnerID = owner.ID
	if err := s.PutProject(project); err != nil {
		t.Fatal(err)
	}
	repoID := putPlatformImportRepo(t, s)
	h := newTAPDImportHandler(t, s, root, `{"data":[{"id":"1010000000100000666","name":"多处理人需求","status":"open","creator":"胡锦康","owner":"张三;"}]}`)
	imported := requestJSONAuthed[domain.Issue](t, h, importer, http.MethodPost, "/api/issues/import-tapd", map[string]any{
		"projectId":    "platform",
		"url":          "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100000666",
		"repositoryId": repoID,
	}, http.StatusOK)
	if imported.AssigneeUserID != creator.ID {
		t.Fatalf("assignee=%d want TAPD creator %d", imported.AssigneeUserID, creator.ID)
	}

	unknown := newTAPDImportHandler(t, s, root, `{"data":[{"id":"1010000000100000777","name":"未知处理人","status":"open","creator":"ghost","owner":"胡锦康;"}]}`)
	fallback := requestJSONAuthed[domain.Issue](t, unknown, importer, http.MethodPost, "/api/issues/import-tapd", map[string]any{
		"projectId":    "platform",
		"url":          "https://tapd.example.com/tapd_fe/10000000/story/detail/1010000000100000777",
		"repositoryId": repoID,
	}, http.StatusOK)
	if fallback.AssigneeUserID != importer.ID {
		t.Fatalf("assignee=%d want importer %d, not the TAPD handler", fallback.AssigneeUserID, importer.ID)
	}
}

func newTAPDImportHandler(t *testing.T, s *repo.Store, root, payload string) http.Handler {
	t.Helper()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"mock","version":"1"}}}`, req.ID)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			escaped, _ := json.Marshal(payload)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":%s}]}}`, req.ID, escaped)
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	t.Cleanup(gateway.Close)
	if err := s.PutMCPServer(domain.MCPServerDefinition{ID: "mcp-70-project-platform", Name: "TAPD MCP", ProjectID: "platform", MarketID: "70", Scope: "project", Transport: "http", URL: gateway.URL, AuthType: "taihu-pat", TimeoutSec: 30}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSyncConfig(domain.SyncConfig{TAPDMCPURL: gateway.URL, AutoSyncIntervalSec: 300}); err != nil {
		t.Fatal(err)
	}
	return New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
}

func putPlatformImportRepo(t *testing.T, s *repo.Store) string {
	t.Helper()
	id := "repo-import-platform"
	if err := s.PutRepository(domain.Repository{ID: id, ProjectID: "platform", Name: "skillhub", GongfengProjectID: "example/skillhub", DefaultBranch: "master"}); err != nil {
		t.Fatal(err)
	}
	return id
}

func requestJSONAuthed[T any](t *testing.T, h http.Handler, user domain.User, method, path string, body any, wantStatus int) T {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	token, err := auth.NewClientFromEnv().SignUser(&auth.User{ID: user.ID, LoginName: user.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s: status %d, body %s", method, path, rec.Code, rec.Body.String())
	}
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUserCredentialsAreStoredAndRedacted(t *testing.T) {
	t.Setenv("AGENTFLOW_SESSION_SECRET", "credential-test-session-secret")
	root := t.TempDir()
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h := New(s, engine.New(s, runtime.NewManager(filepath.Join(root, "workspaces"))), tracker.NewService(), "", "")
	if err := s.PutUser(domain.User{StaffID: 2002, LoginName: "alice", ChineseName: "爱丽丝"}); err != nil {
		t.Fatal(err)
	}
	alice, _ := s.UserByLoginName("alice")
	body, _ := json.Marshal(map[string]any{"gitToken": "user_git_secret", "codeBuddyApiKey": "ck_user_secret", "codeBuddyInternetEnvironment": "ioa"})
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/users/%d/credentials", alice.ID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	token, err := auth.NewClientFromEnv().SignUser(&auth.User{ID: alice.ID, LoginName: alice.LoginName, AuthTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("credential update status=%d body=%s", rec.Code, rec.Body.String())
	}
	var public domain.User
	if err := json.Unmarshal(rec.Body.Bytes(), &public); err != nil {
		t.Fatal(err)
	}
	if public.GitToken != "" || public.CodeBuddyAPIKey != "" || !public.GitTokenConfigured || !public.CodeBuddyAPIKeyConfigured {
		t.Fatalf("credential response must be redacted: %+v", public)
	}
	stored, ok := s.User(alice.ID)
	if !ok || stored.GitToken != "user_git_secret" || stored.CodeBuddyAPIKey != "ck_user_secret" || stored.CodeBuddyInternetEnvironment != "ioa" {
		t.Fatalf("store must keep user secrets, got %+v", stored)
	}
	boot := requestJSON[map[string]any](t, h, http.MethodGet, "/api/bootstrap", nil, http.StatusOK)
	raw, _ := json.Marshal(boot)
	if bytes.Contains(raw, []byte("user_git_secret")) || bytes.Contains(raw, []byte("ck_user_secret")) {
		t.Fatalf("bootstrap leaked secrets: %s", raw)
	}
	unauthorized := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/users/%d/credentials", alice.ID), bytes.NewReader(body))
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorizedRec := httptest.NewRecorder()
	h.ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusForbidden {
		t.Fatalf("guest credential update status=%d", unauthorizedRec.Code)
	}
}

func requestJSON[T any](t *testing.T, h http.Handler, method, path string, body any, wantStatus int) T {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s: status %d, body %s", method, path, rec.Code, rec.Body.String())
	}
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func requestEmptyJSON[T any](t *testing.T, h http.Handler, method, path string, wantStatus int) T {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s: status %d, body %s", method, path, rec.Code, rec.Body.String())
	}
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSlimTelemetryPreservesExecutionAttempt(t *testing.T) {
	event := domain.TelemetryEvent{Context: domain.TelemetryContext{RunID: "run", NodeID: "develop", Attempt: 4}, Attributes: map[string]any{"secret": "hidden"}}
	got := slimTelemetryEvent(event)
	if got.Context.Attempt != 4 || got.Context.NodeID != "develop" || got.Attributes != nil {
		t.Fatalf("attempt or safe projection lost: %+v", got)
	}
}
