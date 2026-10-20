package httpapi

import (
	"agentflow/internal/domain"
	"agentflow/internal/engine"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestTaskGroupCreationScopeAndResponsibleFilter(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	checkTaskGroupCreation(t, st)
}

func TestMySQLTaskGroupCreationScopeAndResponsibleFilter(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("requires disposable local MySQL")
	}
	st, err := repo.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	checkTaskGroupCreation(t, st)
}

func checkTaskGroupCreation(t *testing.T, st *repo.Store) {
	t.Helper()
	t.Setenv("AGENTFLOW_ASSISTANT_SESSION_DIR", t.TempDir())
	projectID := fmt.Sprintf("collab-create-%d", time.Now().UnixNano())
	if err := st.PutUser(domain.User{LoginName: projectID}); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByLoginName(projectID)
	if err := st.PutProject(domain.Project{ID: projectID, Name: "协作", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(domain.Project{ID: projectID + "-other", Name: "其他项目", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	sandboxID := projectID + "-sandbox"
	if err := st.PutSandbox(domain.SandboxProfile{ID: sandboxID, Name: "测试环境", ProjectID: projectID, Driver: "local"}); err != nil {
		t.Fatal(err)
	}
	templateID := projectID + "-template"
	if err := st.PutWorkflow(domain.WorkflowDefinition{ID: templateID, Name: "测试模板", ProjectID: projectID, SandboxProfileID: sandboxID, Nodes: []domain.WorkflowNode{{ID: "prepare", Type: "script", Script: "true"}}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c", "foreign"} {
		pid := projectID
		if id == "foreign" {
			pid += "-other"
		}
		if err := st.PutRepository(domain.Repository{ID: projectID + id, ProjectID: pid, Name: id, DefaultBranch: "master", CloneURL: "https://example.invalid/" + id + ".git", PipelineProjectID: 42}); err != nil {
			t.Fatal(err)
		}
	}
	e := engine.New(st, runtime.NewManagerWithLimit(t.TempDir(), 2))
	e.ConfigureExecution("test", "test", time.Minute, false)
	h := &runsHandlers{deps: &deps{repo: st, engine: e}}
	for _, tc := range []struct {
		name           string
		ids            []string
		omit, template bool
		want           int
	}{
		{name: "subset", ids: []string{projectID + "b"}, want: 1},
		{name: "multiple", ids: []string{projectID + "a", projectID + "c"}, want: 2},
		{name: "no repositories", ids: []string{}, want: 0},
		{name: "legacy all", omit: true, template: true, want: 3},
		{name: "template subset", ids: []string{projectID + "c"}, template: true, want: 1},
		{name: "duplicate IDs", ids: []string{projectID + "b", projectID + "b"}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"projectId": projectID, "input": "一起优化任务"}
			if !tc.omit {
				body["repositoryIds"] = tc.ids
			}
			if tc.template {
				body["workflowId"] = templateID
			} else {
				body["sandboxProfileId"] = sandboxID
			}
			raw, _ := json.Marshal(body)
			c, rec := workbenchContext(u, "POST", "/api/task-groups", bytes.NewBuffer(raw))
			h.taskGroups(c)
			if rec.Code != 201 {
				t.Fatalf("status=%d %s", rec.Code, rec.Body)
			}
			var run domain.Run
			if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
				t.Fatal(err)
			}
			if run.Status != "waiting" || run.WorkflowSnapshot.CollaborationMode != "group" || run.ExecutionContext.ResponsibleUserID != u.ID || len(run.Repositories) != tc.want {
				t.Fatalf("unexpected group: %+v", run)
			}
			if !tc.template && (len(run.WorkflowSnapshot.Nodes) != 0 || len(run.WorkflowSnapshot.GroupMembers) != 0 || run.WorkflowSnapshot.SandboxSnapshot == nil) {
				t.Fatalf("native group inherited SOP or lost environment: %+v", run.WorkflowSnapshot)
			}
			for _, repository := range run.Repositories {
				if !tc.omit {
					found := false
					for _, id := range tc.ids {
						found = found || id == repository.RepositoryID
					}
					if !found {
						t.Fatalf("unselected repository: %+v", repository)
					}
				}
				if repository.BaseBranch != "master" || repository.WorkBranch == "" || repository.PipelineProjectID != 42 {
					t.Fatalf("repository metadata lost: %+v", repository)
				}
			}
		})
	}
	for _, tc := range []struct {
		name   string
		extra  map[string]any
		status int
	}{
		{"foreign repository", map[string]any{"repositoryIds": []string{projectID + "a", projectID + "foreign"}}, 400},
		{"missing repository", map[string]any{"repositoryIds": []string{"missing"}}, 400},
		{"missing template", map[string]any{"workflowId": "missing"}, 403},
		{"missing sandbox", map[string]any{"sandboxProfileId": "missing"}, 403},
		{"empty goal", map[string]any{"input": "  "}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"projectId": projectID, "input": "目标", "sandboxProfileId": sandboxID}
			for key, value := range tc.extra {
				body[key] = value
			}
			before := len(st.Runs())
			raw, _ := json.Marshal(body)
			c, rec := workbenchContext(u, "POST", "/api/task-groups", bytes.NewBuffer(raw))
			h.taskGroups(c)
			if rec.Code != tc.status || len(st.Runs()) != before {
				t.Fatalf("invalid request wrote run: %d %s", rec.Code, rec.Body)
			}
		})
	}
	// Both the execution's responsible user and the linked issue's current
	// assignee count as mine, even if a different user triggered the run.
	issueID := projectID + "-issue"
	if err := st.PutIssue(domain.Issue{ID: issueID, ProjectID: projectID, Title: "Assigned to me", AssigneeUserID: u.ID}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []domain.Run{
		{ID: projectID + "-mine", ExecutionContext: domain.TelemetryContext{ProjectID: projectID, ResponsibleUserID: u.ID}},
		{ID: projectID + "-assigned", ExecutionContext: domain.TelemetryContext{ProjectID: projectID, IssueID: issueID, ResponsibleUserID: u.ID + 100}},
		{ID: projectID + "-other", ExecutionContext: domain.TelemetryContext{ProjectID: projectID, ResponsibleUserID: u.ID + 100}},
		{ID: projectID + "-foreign", ExecutionContext: domain.TelemetryContext{ProjectID: projectID + "-other", ResponsibleUserID: u.ID}},
	} {
		r.Status = "waiting"
		if err := st.PutRun(r); err != nil {
			t.Fatal(err)
		}
	}
	c, rec := workbenchContext(u, "GET", "/api/runs?projectId="+projectID+"&responsible=me", bytes.NewBuffer(nil))
	h.runs(c)
	if rec.Code != 200 {
		t.Fatalf("filter: %d %s", rec.Code, rec.Body)
	}
	var runs []domain.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, run := range runs {
		ids[run.ID] = true
	}
	if !ids[projectID+"-mine"] || !ids[projectID+"-assigned"] || ids[projectID+"-other"] || ids[projectID+"-foreign"] {
		t.Fatalf("responsible filter returned %v", ids)
	}
	// Do not let an anonymous caller accidentally interpret "me" as everybody.
	c, rec = workbenchContext(domain.User{}, "GET", "/api/runs?responsible=me", bytes.NewBuffer(nil))
	c.Request = httptest.NewRequest("GET", "/api/runs?responsible=me", nil)
	h.runs(c)
	if rec.Code != 401 {
		t.Fatalf("anonymous filter: %d %s", rec.Code, rec.Body)
	}
}
