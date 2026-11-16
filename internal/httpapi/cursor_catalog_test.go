package httpapi

import (
	"context"
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

	previous := assistantCursorModels
	t.Cleanup(func() { assistantCursorModels = previous; clearCursorModelsCache() })
	assistantCursorModels = func(_ context.Context, apiKey string) ([]domain.CLIModel, error) {
		if apiKey != "crsr_cursor-a" {
			return nil, nil
		}
		return cursorModelFamilies(parseCursorModels(cursorCatalogTestOutput)), nil
	}
	clearCursorModelsCache()
	cachedCursorModels("crsr_cursor-a")
	deadline := time.Now().Add(5 * time.Second)
	for len(cachedCursorModels("crsr_cursor-a")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("cursor model list was never cached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	h := &assistantHandlers{deps: &deps{repo: st}}
	h.saveCursorCatalog(a)

	// Restart: the process forgets every listed model.
	clearCursorModelsCache()
	assistantCursorModels = func(context.Context, string) ([]domain.CLIModel, error) { return nil, nil }
	a, _ = st.User(a.ID)

	if err := validateAssistantEffort(a, "cursor:widget", "xhigh"); err != nil {
		t.Fatalf("saved family rejected its effort: %v", err)
	}
	if err := validateAssistantEffort(a, "cursor:nova", "max"); err == nil {
		t.Fatal("effort the family lacks was accepted")
	}
	if err := validateAssistantEffort(b, "cursor:widget", "xhigh"); err == nil {
		t.Fatal("another user's family resolved")
	}
	if err := validateAssistantEffort(a, "cursor:grok-5-code", "high"); err == nil {
		t.Fatal("unknown model accepted an effort")
	}
	var listed []assistantModel
	for _, model := range assistantEngineModels(context.Background(), a) {
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
}

func clearCursorModelsCache() {
	assistantCursorModelsCache.Range(func(key, _ any) bool {
		assistantCursorModelsCache.Delete(key)
		return true
	})
}
