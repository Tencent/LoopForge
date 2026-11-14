package repo

import (
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

// A member list can name the same user twice (e.g. a role change merged with
// the old entry). The write must not fail on the member primary key, and the
// later entry decides the stored role.
func TestPutProjectDuplicateMemberKeepsLastRole(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutProject(domain.Project{ID: "p", Name: "P", Members: []domain.ProjectMember{
		{UserID: 1, Role: "viewer"},
		{UserID: 1, Role: "maintainer"},
	}}); err != nil {
		t.Fatalf("duplicate member rejected: %v", err)
	}
	project, ok := st.Project("p")
	if !ok {
		t.Fatal("project not found")
	}
	want := []domain.ProjectMember{{UserID: 1, Role: "maintainer"}}
	if !reflect.DeepEqual(project.Members, want) {
		t.Fatalf("members=%+v, want %+v", project.Members, want)
	}
}

// The owner backfill must update both the indexed column and the JSON payload,
// and only for issues of this project.
func TestPutProjectBackfillWritesColumnAndPayloadInProject(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, id := range []string{"p", "q"} {
		if err := st.PutProject(domain.Project{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutIssue(domain.Issue{ID: "mine", ProjectID: "p", Title: "mine"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{ID: "other", ProjectID: "q", Title: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(domain.Project{ID: "p", Name: "p", OwnerID: 42}); err != nil {
		t.Fatal(err)
	}

	var row issueRow
	if err := st.DB.Where("id = ?", "mine").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.AssigneeUserID == nil || *row.AssigneeUserID != 42 {
		t.Fatalf("assignee column=%v, want 42", row.AssigneeUserID)
	}
	var payload domain.Issue
	if err := jsonUnmarshal(row.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AssigneeUserID != 42 {
		t.Fatalf("payload assignee=%d, want 42", payload.AssigneeUserID)
	}
	if payload.Title != "mine" || payload.ProjectID != "p" {
		t.Fatalf("payload lost fields: %+v", payload)
	}

	if issue, _ := st.Issue("other"); issue.AssigneeUserID != 0 {
		t.Fatalf("issue of another project assigned to %d", issue.AssigneeUserID)
	}
}

// One issue whose payload cannot be decoded must neither fail the project
// write nor stop the backfill of the issues after it.
func TestPutProjectBackfillSkipsUndecodableIssue(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutProject(domain.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"broken", "first", "second"} {
		if err := st.PutIssue(domain.Issue{ID: id, ProjectID: "p", Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DB.Exec("UPDATE issues SET payload = ? WHERE id = ?", "{", "broken").Error; err != nil {
		t.Fatal(err)
	}

	if err := st.PutProject(domain.Project{ID: "p", Name: "P", OwnerID: 42}); err != nil {
		t.Fatalf("undecodable issue failed the project write: %v", err)
	}
	for _, id := range []string{"first", "second"} {
		if issue, _ := st.Issue(id); issue.AssigneeUserID != 42 {
			t.Fatalf("issue %s assignee=%d, want owner 42", id, issue.AssigneeUserID)
		}
	}
	var row issueRow
	if err := st.DB.Where("id = ?", "broken").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.AssigneeUserID != nil {
		t.Fatalf("undecodable issue was assigned to %d", *row.AssigneeUserID)
	}
}
