package engine

import (
	"errors"
	"testing"

	"agentflow/internal/domain"
)

func TestDeliveryMRsFromRunFallsBackWhenLookupFails(t *testing.T) {
	e := &Engine{FindBranchMR: func(project, branch string) (domain.DeliveryMR, bool, error) {
		if project == "g/a" {
			return domain.DeliveryMR{IID: 21, GongfengState: "opened"}, true, nil
		}
		return domain.DeliveryMR{}, false, errors.New("gongfeng unavailable")
	}}
	run := domain.Run{
		ID: "run-1",
		Repositories: []domain.RepositoryContext{
			{RepositoryID: "a", GongfengProjectID: "g/a", WorkBranch: "feature/x"},
			{RepositoryID: "b", GongfengProjectID: "g/b", WorkBranch: "feature/x"},
		},
		Artifacts: []domain.Artifact{{Name: "mr-body", Content: "https://git.example.com/g/a/-/merge_requests/1 https://git.example.com/g/b/-/merge_requests/6"}},
	}
	got := e.deliveryMRsFromRun(run)
	if len(got) != 2 || got[0].IID != 21 || got[1].IID != 6 || got[1].RepositoryID != "b" {
		t.Fatalf("deliveryMRsFromRun() = %+v", got)
	}
}
