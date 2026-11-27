package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCodexUserProviderArgvWithCatalogAddsTheCatalog(t *testing.T) {
	in := []string{"codex", "exec", "--json", "prompt"}
	got, err := CodexUserProviderArgvWithCatalog(in, "https://gateway.example/v1", "/tmp/models.json")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, `model_catalog_json="/tmp/models.json"`) {
		t.Fatalf("the catalog must be passed to the CLI: %q", joined)
	}
	// The catalog is a config override, so it belongs with the other overrides
	// and not after the prompt.
	prompt := indexOf(got, "prompt")
	catalog := indexOf(got, `model_catalog_json="/tmp/models.json"`)
	if catalog < 0 || catalog > prompt {
		t.Fatalf("the catalog must precede the prompt: %q", got)
	}
}

func TestCodexUserProviderArgvWithoutCatalogKeepsTheCLIOne(t *testing.T) {
	for _, path := range []string{"", "   "} {
		got, err := CodexUserProviderArgvWithCatalog([]string{"codex", "exec", "prompt"}, "", path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(got, " "), "model_catalog_json") {
			t.Fatalf("no catalog must be passed for %q: %q", path, got)
		}
	}
}

func indexOf(argv []string, want string) int {
	for i, v := range argv {
		if v == want {
			return i
		}
	}
	return -1
}

func TestWriteCodexModelCatalogDescribesTheModels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	models := []CodexModel{
		{ID: "domestic-model", Name: "Domestic Model", ContextWindow: 200000, MaxOutputTokens: 64000, Efforts: []string{"low", "high"}},
		{ID: "  ", Name: "skipped"},
	}
	if _, err := WriteCodexModelCatalog(dir, path, models); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var catalog codexModelCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 1 {
		t.Fatalf("a model without an ID must be skipped: %+v", catalog.Models)
	}
	model := catalog.Models[0]
	if model.Slug != "domestic-model" || model.DisplayName != "Domestic Model" {
		t.Fatalf("model = %+v", model)
	}
	if model.ContextWindow != 200000 || model.MaxOutputTokens != 64000 {
		t.Fatalf("the real window must survive: %+v", model)
	}
	// The truncation budget leaves room for the reply.
	if model.TruncationPolicy.Mode != "tokens" || model.TruncationPolicy.Limit != 100000 {
		t.Fatalf("truncation = %+v", model.TruncationPolicy)
	}
	// Only the levels the model accepts, in Codex's own order.
	var efforts []string
	for _, level := range model.SupportedReasoningLevels {
		efforts = append(efforts, level.Effort)
	}
	if !slices.Equal(efforts, []string{"low", "high"}) {
		t.Fatalf("efforts = %v", efforts)
	}
}

func TestWriteCodexModelCatalogUsesAConservativeWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if _, err := WriteCodexModelCatalog(dir, path, []CodexModel{{ID: "unknown-model"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var catalog codexModelCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 1 {
		t.Fatalf("models = %+v", catalog.Models)
	}
	model := catalog.Models[0]
	// Codex's own fallback is smaller than this, so the catalog still wins.
	if model.ContextWindow != codexModelCatalogWindow {
		t.Fatalf("window = %d want %d", model.ContextWindow, codexModelCatalogWindow)
	}
	if model.MaxOutputTokens != codexModelCatalogOutput {
		t.Fatalf("output = %d want %d", model.MaxOutputTokens, codexModelCatalogOutput)
	}
	if model.DisplayName != "unknown-model" {
		t.Fatalf("a model without a name is labelled by its ID: %q", model.DisplayName)
	}
}

func TestWriteCodexModelCatalogWithoutModelsWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	got, err := WriteCodexModelCatalog(dir, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("no model list must produce no path: %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("no file must be written: %v", err)
	}
}

func TestCodexCatalogPathUsesTheCodexHome(t *testing.T) {
	home := t.TempDir()
	bound := CommandSpec{environment: map[string]string{
		"HOME":       home,
		"CODEX_HOME": filepath.Join(home, ".codex"),
	}}
	path, err := codexCatalogPath(bound, []CodexModel{{ID: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, ".codex", "models.json") {
		t.Fatalf("path = %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the catalog must be written: %v", err)
	}
}

func TestCodexCatalogPathWithoutModelsWritesNothing(t *testing.T) {
	bound := CommandSpec{environment: map[string]string{"HOME": t.TempDir()}}
	path, err := codexCatalogPath(bound, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("path = %q", path)
	}
}

func TestCodexCatalogArgSurvivesAFailedWrite(t *testing.T) {
	// A turn without a private home cannot write a catalog. The CLI still has
	// to run, it only falls back to its own metadata.
	bound := CommandSpec{environment: map[string]string{}}
	if got := codexCatalogArg(bound, []CodexModel{{ID: "m"}}); got != "" {
		t.Fatalf("a catalog that cannot be written must be skipped: %q", got)
	}
}
