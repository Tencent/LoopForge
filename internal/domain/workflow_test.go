package domain

import "testing"

func TestKeepGenericSourceCarriesOnlyAMissingMarker(t *testing.T) {
	previous := WorkflowDefinition{Tags: []string{"public", GenericSourceTagPrefix + "tpl"}}
	edited := WorkflowDefinition{Tags: []string{"mine"}}
	edited.KeepGenericSource(previous)
	if got := edited.GenericSource(); got != "tpl" || len(edited.Tags) != 2 || edited.Tags[0] != "mine" {
		t.Fatalf("tags = %v source = %q", edited.Tags, got)
	}
	retagged := WorkflowDefinition{Tags: []string{GenericSourceTagPrefix + "other"}}
	retagged.KeepGenericSource(previous)
	if len(retagged.Tags) != 1 || retagged.GenericSource() != "other" {
		t.Fatalf("an explicit marker must win: %v", retagged.Tags)
	}
	untouched := WorkflowDefinition{Tags: []string{"a"}}
	untouched.KeepGenericSource(WorkflowDefinition{Tags: []string{"b", GenericSourceTagPrefix}})
	if len(untouched.Tags) != 1 || untouched.GenericSource() != "" {
		t.Fatalf("no provenance to keep: %v", untouched.Tags)
	}
}
