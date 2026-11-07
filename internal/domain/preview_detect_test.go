package domain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectFrontendProject(t *testing.T) {
	t.Parallel()

	goOnly := t.TempDir()
	if err := os.WriteFile(filepath.Join(goOnly, "go.mod"), []byte("module inventory-api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goOnly, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found, _ := DetectFrontendProject(goOnly); found {
		t.Fatal("Go-only workspace must not look like a frontend")
	}

	apiOnly := t.TempDir()
	if err := os.WriteFile(filepath.Join(apiOnly, "package.json"), []byte(`{"dependencies":{"express":"^4.19.0"},"scripts":{"start":"node server.js"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if found, _ := DetectFrontendProject(apiOnly); found {
		t.Fatal("Node API without frontend deps must skip")
	}

	nestedNoise := t.TempDir()
	hidden := filepath.Join(nestedNoise, "node_modules", "some-pkg")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found, _ := DetectFrontendProject(nestedNoise); found {
		t.Fatal("index.html inside node_modules must be ignored")
	}

	vite := t.TempDir()
	if err := os.WriteFile(filepath.Join(vite, "vite.config.ts"), []byte("export default {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found, evidence := DetectFrontendProject(vite); !found || evidence != "vite.config.ts" {
		t.Fatalf("vite config: found=%v evidence=%q", found, evidence)
	}

	react := t.TempDir()
	if err := os.WriteFile(filepath.Join(react, "package.json"), []byte(`{"dependencies":{"react":"18.3.1","react-dom":"18.3.1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if found, evidence := DetectFrontendProject(react); !found || evidence == "" {
		t.Fatalf("react package.json: found=%v evidence=%q", found, evidence)
	}

	static := t.TempDir()
	if err := os.MkdirAll(filepath.Join(static, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(static, "dist", "index.html"), []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found, evidence := DetectFrontendProject(static); !found || evidence != "dist/index.html" {
		t.Fatalf("static dist: found=%v evidence=%q", found, evidence)
	}
}
