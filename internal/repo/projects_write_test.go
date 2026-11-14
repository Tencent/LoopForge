package repo

import (
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

func TestPutProjectReplacesMembers(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutProject(domain.Project{ID: "p", Name: "P", Members: []domain.ProjectMember{
		{UserID: 1, Role: "maintainer"},
		{UserID: 2, Role: "  "},
		{UserID: 0, Role: "maintainer"},
		{UserID: -3, Role: "member"},
	}}); err != nil {
		t.Fatal(err)
	}
	project, ok := st.Project("p")
	if !ok {
		t.Fatal("project not found")
	}
	want := []domain.ProjectMember{{UserID: 1, Role: "maintainer"}, {UserID: 2, Role: "member"}}
	if !reflect.DeepEqual(project.Members, want) {
		t.Fatalf("members=%+v, want %+v", project.Members, want)
	}

	if err := st.PutProject(domain.Project{ID: "p", Name: "P", Members: []domain.ProjectMember{{UserID: 3, Role: "viewer"}}}); err != nil {
		t.Fatal(err)
	}
	project, _ = st.Project("p")
	want = []domain.ProjectMember{{UserID: 3, Role: "viewer"}}
	if !reflect.DeepEqual(project.Members, want) {
		t.Fatalf("replaced members=%+v, want %+v", project.Members, want)
	}
}

func TestPutProjectBackfillsOnlyUnassignedIssues(t *testing.T) {
	st, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutProject(domain.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{ID: "unassigned", ProjectID: "p", Title: "unassigned"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutIssue(domain.Issue{ID: "assigned", ProjectID: "p", Title: "assigned", AssigneeUserID: 7}); err != nil {
		t.Fatal(err)
	}
	// Without an owner nothing is backfilled.
	if err := st.PutProject(domain.Project{ID: "p", Name: "P"}); err != nil {
		t.Fatal(err)
	}
	if issue, _ := st.Issue("unassigned"); issue.AssigneeUserID != 0 {
		t.Fatalf("ownerless project assigned issue to %d", issue.AssigneeUserID)
	}

	if err := st.PutProject(domain.Project{ID: "p", Name: "P", OwnerID: 42}); err != nil {
		t.Fatal(err)
	}
	if issue, _ := st.Issue("unassigned"); issue.AssigneeUserID != 42 {
		t.Fatalf("unassigned issue assignee=%d, want owner 42", issue.AssigneeUserID)
	}
	if issue, _ := st.Issue("assigned"); issue.AssigneeUserID != 7 {
		t.Fatalf("assigned issue assignee=%d, want kept 7", issue.AssigneeUserID)
	}
}
