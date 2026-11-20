package domain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewDetectSkipDir(t *testing.T) {
	t.Parallel()

	deep := strings.Repeat("d/", maxPreviewDetectDepth) + "leaf"
	shallow := strings.Repeat("d/", maxPreviewDetectDepth-1) + "leaf"
	cases := []struct {
		name, rel string
		want      bool
	}{
		{"node_modules", "node_modules", true},
		{"Node_Modules", "web/Node_Modules", true},
		{".git", ".git", true},
		{"src", "src", false},
		{"leaf", shallow, false},
		{"leaf", deep, true},
	}
	for _, tc := range cases {
		if got := previewDetectSkipDir(tc.name, tc.rel); got != tc.want {
			t.Errorf("previewDetectSkipDir(%q, %q) = %v, want %v", tc.name, tc.rel, got, tc.want)
		}
	}
}

func TestFrontendFileEvidence(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	reactPkg := write("react.json", `{"dependencies":{"react":"18.3.1"}}`)
	scriptPkg := write("script.json", `{"scripts":{"dev":"VITE_X=1 vite --host"}}`)
	apiPkg := write("api.json", `{"dependencies":{"express":"^4.19.0"}}`)
	brokenPkg := write("broken.json", `{not json`)

	cases := []struct {
		label, path, rel, name string
		wantHit                bool
		wantEvidence           string
	}{
		{"index page", "", filepath.Join("dist", "index.html"), "index.html", true, "dist/index.html"},
		{"index page upper case", "", "INDEX.HTM", "INDEX.HTM", true, "INDEX.HTM"},
		{"bundler config", "", filepath.Join("web", "vite.config.ts"), "vite.config.ts", true, "web/vite.config.ts"},
		{"frontend dependency", reactPkg, filepath.Join("app", "package.json"), "package.json", true, "app/package.json (react)"},
		{"frontend script", scriptPkg, "package.json", "package.json", true, "package.json (scripts:vite)"},
		{"backend package", apiPkg, "package.json", "package.json", false, ""},
		{"invalid package", brokenPkg, "package.json", "package.json", false, ""},
		{"unrelated file", "", "main.go", "main.go", false, ""},
	}
	for _, tc := range cases {
		hit, evidence := frontendFileEvidence(tc.path, tc.rel, tc.name)
		if hit != tc.wantHit || evidence != tc.wantEvidence {
			t.Errorf("%s: frontendFileEvidence = (%v, %q), want (%v, %q)", tc.label, hit, evidence, tc.wantHit, tc.wantEvidence)
		}
	}
}
