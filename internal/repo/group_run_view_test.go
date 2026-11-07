package repo

import (
	"agentflow/internal/domain"
	"fmt"
	"testing"
)

func TestGroupRunViewRetainsBoundedCommunication(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := domain.Run{ID: "group-view", Status: "waiting", WorkflowSnapshot: domain.WorkflowDefinition{CollaborationMode: "group"}, Nodes: map[string]*domain.NodeRun{}}
	for i := 0; i < 100; i++ {
		r.Events = append(r.Events, domain.RunEvent{ID: fmt.Sprintf("event-%d", i), RunID: r.ID, Type: "group.message", Message: fmt.Sprint(i)})
	}
	if err = s.PutRun(r); err != nil {
		t.Fatal(err)
	}
	got, ok := s.RunView(r.ID)
	if !ok || len(got.Events) != 80 || got.Events[0].Message != "20" || got.Events[79].Message != "99" {
		t.Fatalf("group communication missing: %+v", got.Events)
	}
}
