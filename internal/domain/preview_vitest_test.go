package domain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFrontendDetectionDoesNotTreatVitestAsVite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"devDependencies":{"vitest":"3.0.0"},"scripts":{"test":"vitest run"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, evidence := DetectFrontendProject(root); found {
		t.Fatalf("backend test runner triggered frontend preview: %q", evidence)
	}
}

func TestFrontendScriptsRequireCompleteCommandTokens(t *testing.T) {
	for command, want := range map[string]bool{
		"npx vite build":                 true,
		"./node_modules/.bin/vite build": true,
		"prepare&&vite build":            true,
		"next build":                     true,
		"vitest run":                     false,
		"node scripts/invite-users.js":   false,
		"node webpack-helper.js":         false,
	} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"build":"`+command+`"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, _ := DetectFrontendProject(root); got != want {
				t.Errorf("DetectFrontendProject(%q) = %v, want %v", command, got, want)
			}
		})
	}
}
