package runtime

import (
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

// TestCursorCatalogFamilyEdges covers saved catalog rows the resolver must
// not trust as-is: a row whose efforts are all unknown or blank is not a
// family, and a saved default the row lacks is replaced by the inferred one.
func TestCursorCatalogFamilyEdges(t *testing.T) {
	catalog := []domain.CLIModel{
		{ID: "junk", Efforts: map[string]string{"turbo": "junk-turbo", "low": "  "}},
		{ID: "pair", DefaultEffort: "max", Efforts: map[string]string{"low": "pair-low", "high": "pair-high"}},
	}
	if got := CursorModelID("junk", "low", catalog); got != "junk" {
		t.Fatalf("row without usable efforts resolved to %q", got)
	}
	if got := CursorModelEfforts("junk", catalog); got != nil {
		t.Fatalf("row without usable efforts listed %v", got)
	}
	// "max" is not in the row; medium is missing too, so the closest
	// level on the stronger side (high) becomes the default.
	if got := CursorModelID("pair", "", catalog); got != "pair-high" {
		t.Fatalf("default for a row with a stale default = %q, want pair-high", got)
	}
	if got := CursorModelID("pair", "low", catalog); got != "pair-low" {
		t.Fatalf("explicit effort = %q", got)
	}
}

// TestFoldCursorCLIModelsSkipsNoise covers inputs the CLI list may carry:
// blank IDs, a plain ID listed twice, an unlisted effort of a built-in
// family, and a dynamic family without a display name.
func TestFoldCursorCLIModelsSkipsNoise(t *testing.T) {
	got := FoldCursorCLIModels([]domain.CLIModel{
		{ID: "  "},
		{ID: "plain", Name: "Plain"},
		{ID: "plain", Name: "Plain again"},
		{ID: "grok-4.7-ultra", Name: "Grok 4.7 Ultra"},
		{ID: "zeta-low"},
	})
	want := []domain.CLIModel{
		{ID: "grok-4.7", Name: "Grok 4.7"},
		{ID: "plain", Name: "Plain"},
		{ID: "zeta", Name: "zeta", DefaultEffort: "low", Efforts: map[string]string{"low": "zeta-low"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("folded models:\n got %+v\nwant %+v", got, want)
	}
}

func TestWithCursorModelArg(t *testing.T) {
	argv := []string{"cursor-agent", "--model", "widget", "-p"}
	same := withCursorModelArg(argv, "widget")
	if &same[0] != &argv[0] {
		t.Fatal("unchanged --model should keep the original argv")
	}
	changed := withCursorModelArg(argv, "widget-high")
	if !reflect.DeepEqual(changed, []string{"cursor-agent", "--model", "widget-high", "-p"}) || argv[2] != "widget" {
		t.Fatalf("changed = %q, original = %q", changed, argv)
	}
	if got := withCursorModelArg([]string{"cursor-agent", "--model"}, "x"); !reflect.DeepEqual(got, []string{"cursor-agent", "--model"}) {
		t.Fatalf("dangling --model = %q", got)
	}
}
