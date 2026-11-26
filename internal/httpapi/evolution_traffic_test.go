package httpapi

import (
	"net/http"
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

func TestEvolutionTrafficEndpointPreservesArmsAndBlocksCompletedTrial(t *testing.T) {
	env := newEvoTestEnv(t)
	p := env.seedPendingProposal(t, "traffic-endpoint")
	path := "/api/evolution/proposals/" + p.ID
	trial := requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, path+"/approve", map[string]any{"mode": "canary", "percent": 50}, http.StatusOK)
	trial.Canary.Canary = domain.EvoArmStats{Runs: 5, Clean: 5}
	trial.Canary.Control = domain.EvoArmStats{Runs: 5, Clean: 3, Reworked: 1, Failed: 1}
	if err := env.store.PutEvoProposal(trial); err != nil {
		t.Fatal(err)
	}
	trial, _ = env.store.EvoProposal(p.ID)
	changed := requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, path+"/canary/traffic", map[string]any{"percent": 25}, http.StatusOK)
	expected := *trial.Canary
	expected.Percent = 25
	if !reflect.DeepEqual(changed.Canary, &expected) || changed.Revision != trial.Revision+1 {
		t.Fatalf("traffic response must preserve arms and advance the CAS revision: %+v", changed)
	}
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, path+"/canary/traffic", map[string]any{"percent": 0}, http.StatusBadRequest)
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, path+"/canary/traffic", map[string]any{"percent": 100}, http.StatusBadRequest)
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, path+"/approve", nil, http.StatusConflict)
	live, _ := env.store.Agent(p.AssetID)
	if live.Prompt != p.BaseContent {
		t.Fatal("adjustment must not publish candidate body")
	}
	stored, _ := env.store.EvoProposal(p.ID)
	stored.Canary.Status = domain.EvoCanaryPassed
	if err := env.store.PutEvoProposal(stored); err != nil {
		t.Fatal(err)
	}
	requestJSONAuthed[map[string]any](t, env.router, env.user, http.MethodPost, path+"/canary/traffic", map[string]any{"percent": 50}, http.StatusConflict)
}

func TestEvolutionTrafficEndpointRejectsViewerAndForeignProject(t *testing.T) {
	env := newEvoTestEnv(t)
	t.Setenv("AGENTFLOW_AUTH_REQUIRED", "true")
	for _, user := range []domain.User{{StaffID: 3122, LoginName: "traffic-viewer"}, {StaffID: 3123, LoginName: "traffic-outsider"}} {
		if err := env.store.PutUser(user); err != nil {
			t.Fatal(err)
		}
	}
	viewer, _ := env.store.UserByLoginName("traffic-viewer")
	outsider, _ := env.store.UserByLoginName("traffic-outsider")
	project, _ := env.store.Project("evo-p")
	project.OwnerID = env.user.ID
	project.Members = []domain.ProjectMember{{UserID: viewer.ID, Role: "viewer"}}
	if err := env.store.PutProject(project); err != nil {
		t.Fatal(err)
	}
	p := env.seedPendingProposal(t, "traffic-permissions")
	path := "/api/evolution/proposals/" + p.ID
	requestJSONAuthed[domain.EvoProposal](t, env.router, env.user, http.MethodPost, path+"/approve", map[string]any{"mode": "canary", "percent": 50}, http.StatusOK)
	for _, user := range []domain.User{viewer, outsider} {
		requestJSONAuthed[map[string]any](t, env.router, user, http.MethodPost, path+"/canary/traffic", map[string]any{"percent": 25}, http.StatusForbidden)
	}
	stored, _ := env.store.EvoProposal(p.ID)
	if stored.Canary.Percent != 50 {
		t.Fatal("unauthorized traffic adjustment was persisted")
	}
}
