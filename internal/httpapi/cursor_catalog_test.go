package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

const cursorCatalogTestOutput = `Available models

widget-low - Widget Low
widget - Widget
widget-extra-high - Widget Extra High
nova-medium - Nova Medium (NO ZDR)
nova-high - Nova High (NO ZDR)
grok-5-code - Grok 5 Code
cursor-grok-4.6-high - Grok 4.6 High
muse-wonder-1.0-medium - Muse Wonder Medium
muse-spark-1.3-high - Muse Spark 1.3 High
composer-2.5 - Composer 2.5
auto - Auto (default)
`

// TestCursorFamiliesSurviveRestart covers a family folded out of the CLI's
// own list: it is saved with the user's Cursor credential, so effort checks
// and the --model of a turn still resolve once the in-memory list is gone
// (a restart, another instance), and only for the user whose CLI listed it.
func TestCursorFamiliesSurviveRestart(t *testing.T) {
	st, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, login := range []string{"cursor-a", "cursor-b"} {
		if err := st.PutUser(domain.User{LoginName: login}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := st.UserByLoginName("cursor-a")
	b, _ := st.UserByLoginName("cursor-b")
	for _, u := range []domain.User{a, b} {
		if _, err := st.UpdateUserCredentials(u.ID, repo.UserCredentialUpdate{AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{
			runtime.AgentCLICursor: {APIKey: "crsr_" + u.LoginName},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	a, _ = st.User(a.ID)
	b, _ = st.User(b.ID)

	stubCLIModelDiscovery(t)
	release := make(chan struct{})
	previous := assistantCursorModels
	t.Cleanup(func() { assistantCursorModels = previous; clearCursorModelsCache() })
	assistantCursorModels = func(_ context.Context, apiKey string) ([]domain.CLIModel, error) {
		if apiKey != "crsr_cursor-a" {
			return nil, nil
		}
		select {
		case <-release:
		case <-t.Context().Done():
			return nil, t.Context().Err()
		}
		return cursorModelFamilies(parseCursorModels(cursorCatalogTestOutput)), nil
	}
	clearCursorModelsCache()
	h := &assistantHandlers{deps: &deps{repo: st}}
	readCatalog := func() assistantModelCatalog {
		t.Helper()
		c, rec := workbenchContext(a, http.MethodGet, "/api/assistant/models", &bytes.Buffer{})
		h.models(c)
		var catalog assistantModelCatalog
		if err := json.Unmarshal(rec.Body.Bytes(), &catalog); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("model catalog: %d %s", rec.Code, rec.Body)
		}
		return catalog
	}
	if catalog := readCatalog(); !catalog.CursorModelsPending {
		t.Fatal("cold Cursor catalog must report that discovery is pending")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		catalog := readCatalog()
		if !catalog.CursorModelsPending {
			if !slices.ContainsFunc(catalog.Models, func(model assistantModel) bool {
				return model.ID == "cursor:widget" && slices.Contains(model.Efforts, "xhigh")
			}) {
				t.Fatal("completed discovery did not expose the model's efforts")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cursor model list was never cached")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Restart: the process forgets every listed model.
	clearCursorModelsCache()
	assistantCursorModels = func(context.Context, string) ([]domain.CLIModel, error) { return nil, nil }
	a, _ = st.User(a.ID)

	if err := validateAssistantEffort(context.Background(), a, "cursor:widget", "xhigh"); err != nil {
		t.Fatalf("saved family rejected its effort: %v", err)
	}
	if err := validateAssistantEffort(context.Background(), a, "cursor:nova", "max"); err == nil {
		t.Fatal("effort the family lacks was accepted")
	}
	if err := validateAssistantEffort(context.Background(), b, "cursor:widget", "xhigh"); err == nil {
		t.Fatal("another user's family resolved")
	}
	if err := validateAssistantEffort(context.Background(), a, "cursor:grok-5-code", "high"); err == nil {
		t.Fatal("unknown model accepted an effort")
	}
	var listed []assistantModel
	models, _ := assistantEngineModels(context.Background(), a, nil)
	for _, model := range models {
		if model.Engine == runtime.AgentCLICursor {
			listed = append(listed, model)
		}
	}
	names := map[string]assistantModel{}
	for _, model := range listed {
		names[model.ID] = model
	}
	if got := names["cursor:widget"]; !slices.Equal(got.Efforts, []string{"low", "medium", "xhigh"}) {
		t.Fatalf("listed after restart: %+v", listed)
	}
	if got := names["cursor:nova"]; got.Name != "Nova (NO ZDR)" || !slices.Equal(got.Efforts, []string{"medium", "high"}) {
		t.Fatalf("nova after restart: %+v", got)
	}
	for id, want := range map[string]string{
		"cursor:grok-5-code":     "grok",
		"cursor:cursor-grok-4.6": "grok",
		"cursor:muse-wonder-1.0": "muse",
		"cursor:muse-spark-1.3":  "muse",
		"cursor:composer-2.5":    "cursor",
		"cursor:auto":            "cursor",
	} {
		if got := names[id].Family; got != want {
			t.Errorf("%s family = %q, want %q", id, got, want)
		}
	}

	for _, c := range []struct {
		user                domain.User
		model, effort, want string
	}{
		{a, "widget", "xhigh", "widget-extra-high"},
		{a, "widget", "", "widget"},
		{a, "nova", "", "nova-medium"},
		{a, "grok-5-code", "high", "grok-5-code"},
		{b, "widget", "xhigh", "widget"},
	} {
		command := runtime.AgentCLICommand(runtime.AgentCLICursor, runtime.AgentCLICommandOptions{Model: c.model, Effort: c.effort, Prompt: "p", Chat: true})
		home := runtime.AgentCLIHome{Base: t.TempDir(), Rel: "home"}
		argv, _, err := runtime.ApplyAgentCLICredential(runtime.AgentCLICursor, runtime.AgentCLIUserCredentials(c.user)[runtime.AgentCLICursor], command.AgentCLI, home, home.Base, command.Argv)
		if err != nil {
			t.Fatal(err)
		}
		if i := slices.Index(argv, "--model"); i < 0 || argv[i+1] != c.want {
			t.Errorf("%s %s/%s: argv %q, want --model %s", c.user.LoginName, c.model, c.effort, argv, c.want)
		}
	}

	// A refresh already in flight must not replace a catalog just saved in
	// personal settings or on a machine card.
	custom := []domain.CLIModel{{ID: "nova", Name: "My Nova"}}
	save := func(change repo.AgentCLICredentialUpdate) domain.User {
		stored, err := st.UpdateUserCredentials(a.ID, repo.UserCredentialUpdate{
			AgentCLICredentials: map[string]repo.AgentCLICredentialUpdate{runtime.AgentCLICursor: change},
		})
		if err != nil {
			t.Fatal(err)
		}
		return stored
	}
	save(repo.AgentCLICredentialUpdate{Models: &custom})
	refresh := cursorModelFamilies(parseCursorModels(cursorCatalogTestOutput))
	stored := save(repo.AgentCLICredentialUpdate{Models: &refresh, DiscoveredForKey: "crsr_cursor-a"})
	if got := assistantCursorCatalog(stored); len(got) != 1 || got[0].ID != "nova" || got[0].Name != "My Nova" {
		t.Fatalf("discovery replaced a user catalog: %+v", got)
	}
}

func clearCursorModelsCache() {
	assistantCursorModelsCache.Range(func(key, _ any) bool {
		assistantCursorModelsCache.Delete(key)
		return true
	})
}
