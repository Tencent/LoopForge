package verifysidecar

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const sidecarSpec = `spec_version: "1.1"
requirement_id: R-1
clauses: [{id: C1, text: ok}]
scenarios:
  - id: S1
    covers: [C1]
    primary: positive
    kind: positive
    when: {http: {method: GET, path: /healthz}}
    then: {http: {status: 200}}
  - id: S2
    covers: [C1]
    primary: negative
    kind: negative
    when: {http: {method: GET, path: /missing}}
    then: {http: {status: 404}}
`

func TestCompileSpecWritesSources(t *testing.T) {
	repo := testRepo(t)
	out := filepath.Join(repo, "generated")
	server := httptest.NewServer(New(repo).Handler())
	defer server.Close()

	status, body := postJSON(t, server, "/compile-spec", map[string]any{"yaml": sidecarSpec, "outputDir": out})
	if status != http.StatusOK {
		t.Fatalf("compile-spec = %d %v", status, body)
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no sources written: %v %v", entries, err)
	}
	matches, _ := filepath.Glob(filepath.Join(out, "aegis", "generated", "*.java"))
	if len(matches) != 2 {
		t.Fatalf("generated files = %v", matches)
	}
	status, body = postJSON(t, server, "/compile-spec", map[string]any{"yaml": sidecarSpec, "outputDir": out})
	if status != http.StatusConflict {
		t.Fatalf("second compile = %d %v, want 409", status, body)
	}
}

// TestCompiledSpecRunsAgainstSubject is the M3 end-to-end gate: compile a YAML
// spec, launch a subject container, run the generated JUnit tests against it,
// and accept only on a green run.
func TestCompiledSpecRunsAgainstSubject(t *testing.T) {
	if os.Getenv("LOOPFORGE_VERIFY_E2E") != "1" {
		t.Skip("set LOOPFORGE_VERIFY_E2E=1 to run the real acceptance test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}
	repo := testRepo(t)
	out := filepath.Join(repo, "generated")
	sidecar := New(repo)
	sidecar.AllowTestCommand = true
	server := httptest.NewServer(sidecar.Handler())
	defer server.Close()

	status, body := postJSON(t, server, "/compile-spec", map[string]any{"yaml": sidecarSpec, "outputDir": out})
	if status != http.StatusOK {
		t.Fatalf("compile-spec = %d %v", status, body)
	}
	status, body = postJSON(t, server, "/verify", map[string]any{
		"repo": repo, "spec": "spec.md",
		"acceptanceSpec":    out,
		"subjectImage":      "node:22-bookworm-slim",
		"subjectCommand":    []string{"node", "-e", `require("http").createServer((q,s)=>{if(q.url==="/healthz"){s.setHeader("Content-Type","application/json");s.end('{"ok":true,"items":[{"id":1}]}')}else{s.statusCode=404;s.end("{}")}}).listen(8080)`},
		"subjectPort":       8080,
		"subjectHealthPath": "/healthz",
	})
	if status != http.StatusOK {
		t.Fatalf("verify = %d %v", status, body)
	}
	acceptance, _ := body["acceptance"].(map[string]any)
	if acceptance["status"] != "PASS" {
		t.Fatalf("acceptance = %v", acceptance)
	}
	if junit, _ := acceptance["junit"].(map[string]any); junit["tests"] == nil {
		t.Fatalf("junit evidence missing: %v", acceptance)
	}
}
