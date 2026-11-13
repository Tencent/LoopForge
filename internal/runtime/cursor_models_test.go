package runtime

import (
	"reflect"
	"slices"
	"testing"

	"agentflow/internal/domain"
)

func TestCursorModelTable(t *testing.T) {
	levels := domain.ExecutorEffortLevels[AgentCLICursor]
	ids, raws := map[string]bool{}, map[string]bool{}
	for _, model := range cursorModels {
		if ids[model.ID] || model.Name == "" {
			t.Fatalf("duplicate or unnamed family %+v", model)
		}
		ids[model.ID] = true
		for effort, raw := range model.Variants {
			if (effort != "" && !slices.Contains(levels, effort)) || raws[raw] {
				t.Fatalf("%s: bad variant %q=%q", model.ID, effort, raw)
			}
			raws[raw] = true
		}
		if _, ok := model.Variants[model.Default]; !ok || (model.Default == "") != (len(model.Variants) == 1 && model.Variants[""] != "") {
			t.Fatalf("%s: default %q outside %v", model.ID, model.Default, model.Variants)
		}
	}
}

func TestCursorModelID(t *testing.T) {
	cases := []struct{ id, effort, want string }{
		{"cursor-grok-4.6", "", "cursor-grok-4.6-high"},
		{"cursor-grok-4.6", "low", "cursor-grok-4.6-low"},
		{"gpt-5.5", "xhigh", "gpt-5.5-extra-high"},
		{"gpt-5.3-codex", "medium", "gpt-5.3-codex"},
		{"gpt-5.6-sol-fast", "none", "gpt-5.6-sol-none-fast"},
		{"claude-4.6-opus-thinking", "max", "claude-4.6-opus-max-thinking"},
		{"claude-opus-5-5", "", "claude-opus-5-5-medium"},
		// Missing efforts take the closest one, the stronger on a tie.
		{"kimi-k3", "medium", "kimi-k3-high"},
		{"glm-5.2", "none", "glm-5.2-high"},
		{"gemini-4-argon", "max", "gemini-4-argon-high"},
		{"composer-2.5", "high", "composer-2.5"},
		{"auto", "", "auto"},
		{"gemini-4-carbon", "", "gemini-4-carbon-medium"},
		{"gemini-4-carbon", "high", "gemini-4-carbon-high"},
		{"gemini-4-hydrogen", "xhigh", "gemini-4-hydrogen-xhigh"},
		{"gemini-4-hydrogen", "max", "gemini-4-hydrogen-xhigh"},
		// A raw effort ID keeps that effort.
		{"claude-opus-5-high", "low", "claude-opus-5-high"},
		// An ID outside the table and the catalog never gains a suffix: the
		// CLI only accepts IDs it listed (grok-5-code-high does not exist).
		{"grok-5-code", "high", "grok-5-code"},
		{"some-new-model", "", "some-new-model"},
	}
	for _, c := range cases {
		if got := CursorModelID(c.id, c.effort, nil); got != c.want {
			t.Errorf("CursorModelID(%q, %q) = %q, want %q", c.id, c.effort, got, c.want)
		}
	}
	if got := CursorModelEfforts("kimi-k3", nil); !slices.Equal(got, []string{"low", "high", "max"}) {
		t.Errorf("kimi-k3 efforts: %v", got)
	}
	if got := CursorModelEfforts("composer-2.5", nil); got != nil {
		t.Errorf("composer efforts: %v", got)
	}
	if got := CursorModelEfforts("grok-5-code", nil); got != nil {
		t.Errorf("unknown model efforts: %v", got)
	}
	if family, ok := CursorModelFamily("gpt-5.6-terra-xhigh-fast"); !ok || family.ID != "gpt-5.6-terra-fast" {
		t.Errorf("family of a fast variant: %+v %v", family, ok)
	}
	argv := AgentCLICommand(AgentCLICursor, AgentCLICommandOptions{Model: "claude-opus-4-8-thinking", Effort: "xhigh", Prompt: "p"}).Argv
	if i := slices.Index(argv, "--model"); i < 0 || argv[i+1] != "claude-opus-4-8-thinking-xhigh" {
		t.Errorf("cursor argv: %q", argv)
	}
	// The SOP path: an unknown ID with an effort reaches the CLI unchanged.
	argv = AgentCLICommand(AgentCLICursor, AgentCLICommandOptions{Model: "grok-5-code", Effort: "high", Prompt: "p"}).Argv
	if i := slices.Index(argv, "--model"); i < 0 || argv[i+1] != "grok-5-code" {
		t.Errorf("unknown model argv: %q", argv)
	}
}

func TestFoldCursorCLIModels(t *testing.T) {
	got := FoldCursorCLIModels([]domain.CLIModel{
		{ID: "gemini-4-carbon-high", Name: "Gemini 4 Carbon High"},
		{ID: "gemini-4-carbon-low", Name: "Gemini 4 Carbon Low"},
		{ID: "gemini-4-carbon-medium", Name: "Gemini 4 Carbon Medium"},
		{ID: "gemini-4-hydrogen-xhigh", Name: "Gemini 4 Hydrogen Extra High"},
		{ID: "widget-extra-high", Name: "Widget Extra High"},
		{ID: "widget-low", Name: "Widget Low"},
		{ID: "widget", Name: "Widget"},
		{ID: "widget-low-fast", Name: "Widget Low Fast"},
		{ID: "widget-high-fast", Name: "Widget High Fast"},
		{ID: "plain-model", Name: "Plain Model"},
		{ID: "solo-ultra", Name: "Solo Ultra"},
		{ID: "nova-medium", Name: "Nova Medium (NO ZDR)"},
		{ID: "nova-high", Name: "Nova High (NO ZDR)"},
	})
	want := []domain.CLIModel{
		{ID: "gemini-4-carbon", Name: "Gemini 4 Carbon"},
		{ID: "gemini-4-hydrogen", Name: "Gemini 4 Hydrogen"},
		{ID: "widget", Name: "Widget", DefaultEffort: "medium", Efforts: map[string]string{"low": "widget-low", "medium": "widget", "xhigh": "widget-extra-high"}},
		{ID: "widget-fast", Name: "Widget Fast", DefaultEffort: "high", Efforts: map[string]string{"low": "widget-low-fast", "high": "widget-high-fast"}},
		{ID: "plain-model", Name: "Plain Model"},
		{ID: "solo", Name: "Solo", DefaultEffort: "ultra", Efforts: map[string]string{"ultra": "solo-ultra"}},
		{ID: "nova", Name: "Nova (NO ZDR)", DefaultEffort: "medium", Efforts: map[string]string{"medium": "nova-medium", "high": "nova-high"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("folded models:\n got %+v\nwant %+v", got, want)
	}
	// The folded rows are what gets saved; they resolve without any state
	// left behind by the fold, as after a restart or on another instance.
	cases := []struct{ id, effort, want string }{
		{"widget", "xhigh", "widget-extra-high"},
		{"widget", "", "widget"},
		{"widget-fast", "high", "widget-high-fast"},
		{"solo", "", "solo-ultra"},
		{"nova", "max", "nova-high"},
	}
	for _, c := range cases {
		if got := CursorModelID(c.id, c.effort, want); got != c.want {
			t.Errorf("CursorModelID(%q, %q) = %q, want %q", c.id, c.effort, got, c.want)
		}
		if got := CursorModelID(c.id, c.effort, nil); got != c.id {
			t.Errorf("without the catalog %q/%q = %q, want it unchanged", c.id, c.effort, got)
		}
	}
	if got := CursorModelEfforts("widget", want); !slices.Equal(got, []string{"low", "medium", "xhigh"}) {
		t.Errorf("widget efforts: %v", got)
	}
	if got := CursorModelEfforts("solo", want); !slices.Equal(got, []string{"ultra"}) {
		t.Errorf("solo efforts: %v", got)
	}
	// Each user resolves against their own list: another user's CLI may map
	// the same family differently or not offer it at all.
	other := []domain.CLIModel{{ID: "widget", Efforts: map[string]string{"high": "widget-high"}, DefaultEffort: "high"}}
	if got := CursorModelID("widget", "xhigh", other); got != "widget-high" {
		t.Errorf("other user's widget = %q", got)
	}
	if got := CursorModelEfforts("widget", nil); got != nil {
		t.Errorf("widget efforts without a catalog: %v", got)
	}
}

func TestStripCursorEffortName(t *testing.T) {
	for name, want := range map[string]string{
		"Gemini 4 Carbon Medium":        "Gemini 4 Carbon",
		"Medium Model Medium (NO ZDR)":  "Medium Model (NO ZDR)",
		"Widget Extra High Fast":        "Widget Fast",
		"Widget High Fast (NO ZDR)":     "Widget Fast (NO ZDR)",
		"Claude Fable 5.1 Max (NO ZDR)": "Claude Fable 5.1 (NO ZDR)",
		"Nova Low (preview) (NO ZDR)":   "Nova (preview) (NO ZDR)",
		"Plain (NO ZDR)":                "Plain (NO ZDR)",
	} {
		if got := stripCursorEffortName(name); got != want {
			t.Errorf("stripCursorEffortName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCursorCredentialResolvesSavedFamily(t *testing.T) {
	catalog := []domain.CLIModel{{ID: "widget", Efforts: map[string]string{"low": "widget-low", "medium": "widget", "xhigh": "widget-extra-high"}, DefaultEffort: "medium"}}
	command := AgentCLICommand(AgentCLICursor, AgentCLICommandOptions{Model: "widget", Effort: "xhigh", Prompt: "p"})
	if i := slices.Index(command.Argv, "--model"); command.Argv[i+1] != "widget" {
		t.Fatalf("argv before the credential: %q", command.Argv)
	}
	home := AgentCLIHome{Base: t.TempDir(), Rel: "home"}
	argv, env, err := ApplyAgentCLICredential(AgentCLICursor, ProviderCredential{APIKey: "crsr_x", Models: catalog}, command.AgentCLI, home, home.Base, command.Argv)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(argv, "--model"); argv[i+1] != "widget-extra-high" || env["CURSOR_API_KEY"] != "crsr_x" {
		t.Fatalf("bound argv %q env %v", argv, env)
	}
	if argv[len(argv)-1] != "p" {
		t.Fatalf("prompt moved: %q", argv)
	}
}
