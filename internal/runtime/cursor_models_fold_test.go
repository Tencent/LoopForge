package runtime

import (
	"reflect"
	"testing"

	"agentflow/internal/domain"
)

func TestScanCursorCLIModelsSortsIDs(t *testing.T) {
	present, groups, tail := scanCursorCLIModels([]domain.CLIModel{
		{ID: "gemini-4-carbon-high"},
		{ID: " "},
		{ID: "widget-low", Name: "Widget Low"},
		{ID: "widget-medium", Name: "Widget Medium"},
		{ID: "widget-low", Name: "Widget Low Duplicate"},
		{ID: "plain", Name: " Plain "},
		{ID: "plain", Name: "Plain Again"},
	})
	if !present["gemini-4-carbon"] || len(present) != 1 {
		t.Fatalf("present = %v", present)
	}
	g := groups["widget"]
	if g == nil || len(groups) != 1 {
		t.Fatalf("groups = %v", groups)
	}
	if g.name != "Widget" || !reflect.DeepEqual(g.variants, map[string]string{"low": "widget-low", "medium": "widget-medium"}) {
		t.Fatalf("widget group = %+v", *g)
	}
	want := []cursorModelSlot{
		{family: true, id: "widget"},
		{id: "plain", plain: domain.CLIModel{ID: "plain", Name: "Plain"}},
	}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("tail = %+v, want %+v", tail, want)
	}
}

func TestMergePlainCursorFamilies(t *testing.T) {
	groups := map[string]*cursorFamilyGroup{
		"widget": {name: "Widget Stripped", variants: map[string]string{"low": "widget-low"}},
		"gadget": {name: "Gadget", variants: map[string]string{"medium": "gadget-medium"}},
	}
	tail := []cursorModelSlot{
		{family: true, id: "widget"},
		{id: "widget", plain: domain.CLIModel{ID: "widget", Name: "Widget"}},
		{family: true, id: "gadget"},
		{id: "gadget", plain: domain.CLIModel{ID: "gadget", Name: "Gadget Plain"}},
		{id: "other", plain: domain.CLIModel{ID: "other"}},
	}
	got := mergePlainCursorFamilies(tail, groups)
	want := []cursorModelSlot{tail[0], tail[2], tail[4]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dynamic = %+v, want %+v", got, want)
	}
	if w := groups["widget"]; w.name != "Widget" || w.variants["medium"] != "widget" {
		t.Errorf("widget should take the plain ID as medium: %+v", *w)
	}
	if g := groups["gadget"]; g.name != "Gadget" || g.variants["medium"] != "gadget-medium" {
		t.Errorf("gadget already had medium and must stay: %+v", *g)
	}
}

func TestCursorFamilyRow(t *testing.T) {
	g := &cursorFamilyGroup{variants: map[string]string{"low": "solo-low", "high": "solo-high"}}
	got := cursorFamilyRow("solo", g)
	want := domain.CLIModel{ID: "solo", Name: "solo", DefaultEffort: inferredCursorDefault(g.variants), Efforts: map[string]string{"low": "solo-low", "high": "solo-high"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("row = %+v, want %+v", got, want)
	}
	got.Efforts["low"] = "changed"
	if g.variants["low"] != "solo-low" {
		t.Error("row efforts must be a copy of the group variants")
	}
}
