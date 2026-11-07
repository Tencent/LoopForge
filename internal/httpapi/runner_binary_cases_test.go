package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerBinarySourceBuildAndDefaults(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"cmd/agentflow-runner", "cmd/agentflow-gate-mcp", "internal/empty"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"go.mod":                       "module cvbrunner\n\ngo 1.22\n",
		"go.sum":                       "",
		"cmd/agentflow-runner/main.go": "package main\nfunc main() {}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AGENTFLOW_RUNNER_BINARY", "")
	t.Setenv("AGENTFLOW_SOURCE_ROOT", root)
	rec := serveRunnerBinaryViaGin(t, "/deploy/agentflow-runner?os=linux&arch=amd64")
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("source build = %d: %s", rec.Code, rec.Body.String())
	}
	// The second request takes the content-addressed cache path.
	rec = serveRunnerBinaryViaGin(t, "/deploy/agentflow-runner?os=linux&arch=amd64")
	if rec.Code != http.StatusOK {
		t.Fatalf("cached source build = %d: %s", rec.Code, rec.Body.String())
	}

	prebuilt := filepath.Join(t.TempDir(), "default")
	if err := os.WriteFile(prebuilt, []byte("default-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_RUNNER_BINARY", prebuilt)
	if rec = serveRunnerBinaryViaGin(t, "/deploy/agentflow-runner"); rec.Code != http.StatusOK {
		t.Fatalf("default platform = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRunnerBinaryMissingSourceAndDigestErrors(t *testing.T) {
	t.Setenv("AGENTFLOW_RUNNER_BINARY", "")
	t.Setenv("AGENTFLOW_SOURCE_ROOT", t.TempDir())
	if rec := serveRunnerBinaryViaGin(t, "/deploy/agentflow-runner?os=linux&arch=amd64"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing source = %d: %s", rec.Code, rec.Body.String())
	}

	root := t.TempDir()
	for _, dir := range []string{"cmd/agentflow-runner", "cmd/agentflow-gate-mcp", "internal/testdata", "internal/live"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"go.mod", "go.sum", "internal/live/live.go", "internal/live/live_test.go", "internal/testdata/ignored"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if digest, err := runnerSourceDigest(root); err != nil || len(digest) != 64 {
		t.Fatalf("digest = %q, %v", digest, err)
	}
	if err := os.Remove(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if _, err := runnerSourceDigest(root); err == nil {
		t.Fatal("missing go.mod unexpectedly hashed")
	}
	if got := clipBuildOutput([]byte(strings.Repeat("a", 450))); len(got) != 400 {
		t.Fatalf("clipped output length = %d", len(got))
	}
}
