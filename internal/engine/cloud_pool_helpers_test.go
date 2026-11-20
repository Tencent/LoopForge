package engine

import (
	"testing"

	"agentflow/internal/domain"
)

func TestCloudRunnerPoolServes(t *testing.T) {
	cases := []struct {
		name    string
		pool    CloudRunnerPool
		project string
		want    bool
	}{
		{"disabled without label", CloudRunnerPool{Projects: []string{"*"}}, "p1", false},
		{"wildcard", CloudRunnerPool{Label: "pool", Projects: []string{"*"}}, "p1", true},
		{"listed project", CloudRunnerPool{Label: "pool", Projects: []string{"p1"}}, " p1 ", true},
		{"unlisted project", CloudRunnerPool{Label: "pool", Projects: []string{"p1"}}, "p2", false},
		{"empty project", CloudRunnerPool{Label: "pool", Projects: []string{"p1"}}, "", false},
		{"no projects", CloudRunnerPool{Label: "pool"}, "p1", false},
	}
	for _, tc := range cases {
		if got := tc.pool.serves(tc.project); got != tc.want {
			t.Errorf("%s: serves(%q) = %v, want %v", tc.name, tc.project, got, tc.want)
		}
	}
}

func TestEligiblePoolRunnersFiltersRegisteredRunners(t *testing.T) {
	e, st := cvEEngine(t)
	if got := e.eligiblePoolRunners("pool"); len(got) != 0 {
		t.Fatalf("empty registry = %+v", got)
	}
	runners := []domain.Runner{
		{ID: "ok", Name: "ok", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation, Labels: []string{" pool "}},
		{ID: "other-label", Name: "other", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation, Labels: []string{"gpu"}},
		{ID: "owned", Name: "owned", OwnerUserID: 7, Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation, Labels: []string{"pool"}},
		{ID: "offline", Name: "offline", Status: domain.RunnerStatusOffline, RunnerVersion: domain.RunnerVersionWorkspaceOwnerPreparation, Labels: []string{"pool"}},
		{ID: "old", Name: "old", Status: domain.RunnerStatusOnline, RunnerVersion: domain.RunnerVersionWorkBranchSync, Labels: []string{"pool"}},
	}
	for _, r := range runners {
		if err := st.PutRunner(r); err != nil {
			t.Fatal(err)
		}
	}
	got := e.eligiblePoolRunners("pool")
	if len(got) != 1 || got[0].ID != "ok" {
		t.Fatalf("eligible = %+v", got)
	}
	e.ConfigureCloudRunnerPool(CloudRunnerPool{Label: "pool", Projects: []string{"*"}})
	if configured, online := e.CloudPoolStatus(); !configured || online != len(got) {
		t.Fatalf("status = %v %d, want true %d", configured, online, len(got))
	}
	if picked, ok := e.pickPoolRunner("pool"); !ok || picked.ID != "ok" {
		t.Fatalf("picked = %+v %v", picked, ok)
	}
	if _, ok := e.pickPoolRunner("gpu-missing"); ok {
		t.Fatal("picked runner for unknown label")
	}
}
