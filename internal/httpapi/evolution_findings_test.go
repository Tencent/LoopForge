package httpapi

import (
	"net/http"
	"testing"

	"agentflow/internal/domain"
)

type evoFindingRunsResponse struct {
	Items    []evoFindingRun `json:"items"`
	Complete bool            `json:"complete"`
	Total    int             `json:"total"`
}

func TestEvolutionFindingRuns(t *testing.T) {
	env := newEvoTestEnv(t)
	for _, id := range []string{"one", "two"} {
		env.seedRun(t, id, "failed", "failed", "compile failed line 12")
	}
	env.seedRun(t, "three", "failed", "failed", "network unavailable")
	diag := requestJSON[struct {
		Findings []domain.EvoFinding `json:"findings"`
	}](t, env.router, http.MethodPost, "/api/evolution/diagnose", map[string]any{"projectId": "evo-p"}, http.StatusOK)
	if len(diag.Findings) != 1 || len(diag.Findings[0].Clusters) != 2 {
		t.Fatalf("diagnosis: %+v", diag)
	}
	f := diag.Findings[0]
	cluster := f.Clusters[0]
	url := "/api/evolution/findings/" + f.ID + "/runs?cluster=" + cluster.ID
	res := requestJSON[evoFindingRunsResponse](t, env.router, http.MethodGet, url, nil, http.StatusOK)
	if !res.Complete || res.Total != 2 || len(res.Items) != 2 {
		t.Fatalf("records: %+v", res)
	}
	for _, item := range res.Items {
		if item.RunID == "three" || item.Detail != "compile failed line 12" || item.WorkflowName != env.wf.Name || item.Unavailable {
			t.Fatalf("wrong cluster or missing details: %+v", item)
		}
	}
	requestJSON[map[string]any](t, env.router, http.MethodGet, "/api/evolution/findings/"+f.ID+"/runs?cluster=forged", nil, http.StatusNotFound)

	foreign := domain.Run{ID: "foreign", WorkflowID: "other-workflow", Status: "failed", ExecutionContext: domain.TelemetryContext{ProjectID: "other-project"}, WorkflowSnapshot: domain.WorkflowDefinition{ID: "other-workflow", ProjectID: "other-project"}, Input: "private"}
	if err := env.store.PutRun(foreign); err != nil {
		t.Fatal(err)
	}
	f.Clusters[0].Occurrences = append(f.Clusters[0].Occurrences, domain.EvoClusterOccurrence{RunID: "foreign", Detail: "private"}, domain.EvoClusterOccurrence{RunID: "deleted", Detail: "private"})
	f.Clusters[0].Runs = 4
	if err := env.store.PutEvoFinding(f); err != nil {
		t.Fatal(err)
	}
	res = requestJSON[evoFindingRunsResponse](t, env.router, http.MethodGet, url, nil, http.StatusOK)
	if len(res.Items) != 4 {
		t.Fatalf("missing unavailable placeholders: %+v", res)
	}
	for _, item := range res.Items {
		if item.RunID == "foreign" || item.RunID == "deleted" {
			if !item.Unavailable || item.Detail != "" || item.IssueTitle != "" {
				t.Fatalf("leaked unavailable record: %+v", item)
			}
		}
	}

	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	requestJSON[map[string]any](t, env.router, http.MethodGet, url, nil, http.StatusUnauthorized)
	p, _ := env.store.Project("evo-p")
	p.OwnerID = env.user.ID + 10000
	if err := env.store.PutProject(p); err != nil {
		t.Fatal(err)
	}
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodGet, url, nil, http.StatusForbidden)
	p, _ = env.store.Project("evo-p")
	p.OwnerID = env.user.ID
	if err := env.store.PutProject(p); err != nil {
		t.Fatal(err)
	}
	requestJSONAuthed[evoFindingRunsResponse](t, env.router, env.user, http.MethodGet, url, nil, http.StatusOK)
}

func TestEvolutionFindingRunsLegacy(t *testing.T) {
	env := newEvoTestEnv(t)
	env.seedRun(t, "old", "failed", "failed", "old failure")
	f := domain.EvoFinding{ID: "legacy", ProjectID: "evo-p", AssetKind: domain.EvoAssetAgent, AssetID: env.agent.ID, Status: domain.EvoFindingOpen, Clusters: []domain.EvoSignalCluster{{Kind: "node_failed", Label: "old failure", Runs: 5, RunID: "old"}}}
	if err := env.store.PutEvoFinding(f); err != nil {
		t.Fatal(err)
	}
	res := requestJSON[evoFindingRunsResponse](t, env.router, http.MethodGet, "/api/evolution/findings/legacy/runs?cluster=legacy-0", nil, http.StatusOK)
	if res.Complete || res.Total != 5 || len(res.Items) != 1 {
		t.Fatalf("legacy must not claim completeness: %+v", res)
	}
}
