package httpapi

import (
	"context"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

// TestCursorFreshListWinsOverSavedCatalog covers the chat model list once
// the CLI's own list is cached: it replaces the saved catalog, and
// saveCursorCatalog leaves users alone when there is nothing to save.
func TestCursorFreshListWinsOverSavedCatalog(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	saved := []domain.CLIModel{{ID: "old-model", Name: "Old"}}
	users := map[string]domain.User{}
	for login, key := range map[string]string{"cursor-fresh": "crsr_fresh", "cursor-quiet": "crsr_quiet", "cursor-nokey": ""} {
		if err := st.PutUser(domain.User{LoginName: login}); err != nil {
			t.Fatal(err)
		}
		u, _ := st.UserByLoginName(login)
		if key != "" {
			models := saved
			if _, err := st.UpdateUserCredentials(u.ID, repo.UserCredentialUpdate{AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{
				runtime.AgentCLICursor: {APIKey: key, Models: &models},
			}}); err != nil {
				t.Fatal(err)
			}
		}
		users[login], _ = st.User(u.ID)
	}

	previous := assistantCursorModels
	t.Cleanup(func() { assistantCursorModels = previous; clearCursorModelsCache() })
	assistantCursorModels = func(_ context.Context, apiKey string) ([]domain.CLIModel, error) {
		if apiKey != "crsr_fresh" {
			return nil, nil
		}
		return []domain.CLIModel{{ID: "new-model", Name: "New"}}, nil
	}
	clearCursorModelsCache()
	cachedCursorModels("crsr_fresh")
	cachedCursorModels("crsr_quiet")
	deadline := time.Now().Add(5 * time.Second)
	for len(cachedCursorModels("crsr_fresh")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("cursor model list was never cached")
		}
		time.Sleep(10 * time.Millisecond)
	}

	listed := map[string]bool{}
	for _, model := range assistantEngineModels(context.Background(), users["cursor-fresh"]) {
		if model.Engine == runtime.AgentCLICursor {
			listed[model.ID] = true
		}
	}
	if !listed["cursor:new-model"] || listed["cursor:old-model"] {
		t.Fatalf("cursor models listed = %v, want the fresh CLI list only", listed)
	}

	h := &assistantHandlers{deps: &deps{repo: st}}
	var nilHandler *assistantHandlers
	nilHandler.saveCursorCatalog(users["cursor-fresh"])
	h.saveCursorCatalog(domain.User{})
	h.saveCursorCatalog(users["cursor-nokey"])
	h.saveCursorCatalog(users["cursor-quiet"])
	if got, _ := st.User(users["cursor-nokey"].ID); got.AgentCLICredentials[runtime.AgentCLICursor].Models != nil {
		t.Fatalf("user without a Cursor key got a catalog: %+v", got.AgentCLICredentials)
	}
	if got, _ := st.User(users["cursor-quiet"].ID); !cursorCatalogEqual(got.AgentCLICredentials[runtime.AgentCLICursor].Models, saved) {
		t.Fatalf("empty CLI list replaced the saved catalog: %+v", got.AgentCLICredentials[runtime.AgentCLICursor].Models)
	}
	// The caller's copy already matches the CLI list: nothing is written,
	// so the stored catalog stays as it was.
	current := users["cursor-fresh"]
	cred := current.AgentCLICredentials[runtime.AgentCLICursor]
	cred.Models = []domain.CLIModel{{ID: "new-model", Name: "New"}}
	current.AgentCLICredentials = map[string]domain.AgentCLICredential{runtime.AgentCLICursor: cred}
	h.saveCursorCatalog(current)
	if got, _ := st.User(current.ID); !cursorCatalogEqual(got.AgentCLICredentials[runtime.AgentCLICursor].Models, saved) {
		t.Fatalf("unchanged catalog was written again: %+v", got.AgentCLICredentials[runtime.AgentCLICursor].Models)
	}
	h.saveCursorCatalog(users["cursor-fresh"])
	got, _ := st.User(users["cursor-fresh"].ID)
	if models := got.AgentCLICredentials[runtime.AgentCLICursor].Models; len(models) != 1 || models[0].ID != "new-model" {
		t.Fatalf("fresh list was not saved: %+v", models)
	}
}

func TestCursorCatalogEqual(t *testing.T) {
	base := []domain.CLIModel{{ID: "w", Name: "W", DefaultEffort: "low", Efforts: map[string]string{"low": "w-low"}}}
	for name, c := range map[string]struct {
		a, b []domain.CLIModel
		want bool
	}{
		"same":                   {base, []domain.CLIModel{{ID: "w", Name: "W", DefaultEffort: "low", Efforts: map[string]string{"low": "w-low"}}}, true},
		"name equal to id vs ''": {[]domain.CLIModel{{ID: "m", Name: "m"}}, []domain.CLIModel{{ID: "m"}}, true},
		"'' vs name equal to id": {[]domain.CLIModel{{ID: "m"}}, []domain.CLIModel{{ID: "m", Name: "m"}}, true},
		"other name":             {[]domain.CLIModel{{ID: "m", Name: "M"}}, []domain.CLIModel{{ID: "m"}}, false},
		"other default":          {base, []domain.CLIModel{{ID: "w", Name: "W", Efforts: map[string]string{"low": "w-low"}}}, false},
		"other efforts":          {base, []domain.CLIModel{{ID: "w", Name: "W", DefaultEffort: "low", Efforts: map[string]string{"low": "w-lo"}}}, false},
		"other length":           {base, nil, false},
	} {
		if got := cursorCatalogEqual(c.a, c.b); got != c.want {
			t.Errorf("%s: cursorCatalogEqual = %v, want %v", name, got, c.want)
		}
	}
}
