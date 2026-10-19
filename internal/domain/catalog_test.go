package domain

import "testing"

func TestCatalogVisibility(t *testing.T) {
	if !CatalogOwned("gslb", "gslb") {
		t.Fatal("owned item should match its project")
	}
	if CatalogOwned("", "gslb") {
		t.Fatal("legacy global item is not owned by a project")
	}
	if !CatalogSharedWith("", false, "gslb") {
		t.Fatal("legacy global item should appear as shared")
	}
	if CatalogSharedWith("gslb", true, "gslb") {
		t.Fatal("an owned item stays in the project tab even when shared")
	}
	if !CatalogSharedWith("gslb", true, "igtm") {
		t.Fatal("shared item should appear in another project's shared tab")
	}
	if CatalogSharedWith("gslb", false, "igtm") {
		t.Fatal("private item must not leak to another project")
	}
	if !CatalogVisible("gslb", false, "gslb") || CatalogVisible("gslb", false, "igtm") {
		t.Fatal("private catalog visibility is wrong")
	}
	if !AssetVisible("", "gslb") || !AssetVisible("gslb", "gslb") || AssetVisible("igtm", "gslb") {
		t.Fatal("asset visibility is wrong")
	}
	if got := RunProjectID(Run{ExecutionContext: TelemetryContext{ProjectID: "gslb"}}, "other"); got != "gslb" {
		t.Fatalf("run project = %q", got)
	}
	if got := RunProjectID(Run{}, "gslb"); got != "gslb" {
		t.Fatalf("issue-linked run project = %q", got)
	}
	if got := KeepCatalogProjectID("", "gslb"); got != "gslb" {
		t.Fatalf("empty write should keep existing project: %q", got)
	}
	if got := KeepCatalogProjectID("igtm", "gslb"); got != "igtm" {
		t.Fatalf("explicit project should win: %q", got)
	}
}
