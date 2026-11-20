package engine

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
	"agentflow/internal/verifysidecar"
)

func sidecarRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	run("init", "-q")
	run("config", "user.email", "sidecar@example.com")
	run("config", "user.name", "sidecar")
	if err := os.WriteFile(filepath.Join(dir, "spec.md"), []byte("# acceptance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "init")
	return dir
}

// TestVerifySidecarArtifactBindsApproval is the P5 wiring: a script node calls
// the same-host sidecar, its JSON answer becomes a hash-carrying artifact, and
// the human gate approval binds to that artifact (P2).
func TestVerifySidecarArtifactBindsApproval(t *testing.T) {
	sidecarRepoDir := sidecarRepo(t)
	sidecar := httptest.NewServer(verifysidecar.New(sidecarRepoDir).Handler())
	defer sidecar.Close()

	remote := sidecarRepoDir
	script := fmt.Sprintf(`curl -sS -X POST -H 'Content-Type: application/json' -d '{"repo":"%s","ref":"HEAD","spec":"spec.md"}' %s/verify > verify.json`, remote, sidecar.URL)
	wf := domain.WorkflowDefinition{
		ID: "verify-sidecar-sop", Name: "Verify sidecar SOP", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "verify", Name: "Verify", Type: "script", ScriptLanguage: "shell", Script: script, MaxAttempts: 1,
				ArtifactOutputs: []domain.ArtifactContract{{Name: "verify", Path: "verify.json", MediaType: "application/json", Required: true}}},
			{ID: "gate", Name: "Gate", Type: "approval"},
		},
		Edges: []domain.WorkflowEdge{{ID: "verify-gate", Source: "verify", Target: "gate", When: "success"}},
	}
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, run.ID, "waiting")

	stored, _ := s.Run(run.ID)
	var artifact *domain.Artifact
	for i := range stored.Artifacts {
		if stored.Artifacts[i].Name == "verify" {
			artifact = &stored.Artifacts[i]
		}
	}
	if artifact == nil {
		t.Fatalf("verify artifact missing: %+v", stored.Artifacts)
	}
	var answer map[string]any
	if err := json.Unmarshal([]byte(artifact.Content), &answer); err != nil {
		t.Fatalf("verify artifact is not JSON: %v\n%s", err, artifact.Content)
	}
	for _, key := range []string{"treeHash", "specHash", "toolchainFp", "buildFp", "acceptance"} {
		if _, ok := answer[key]; !ok {
			t.Fatalf("sidecar answer missing %q: %s", key, artifact.Content)
		}
	}

	staleErr := e.Approve(run.ID, "gate", "", NodeExpectation{
		NodeID: "gate", Status: "waiting_approval", Attempt: 1,
		ArtifactName: "verify", ArtifactSHA256: "ffff",
	})
	if staleErr == nil {
		t.Fatal("approval bound to a wrong artifact hash was accepted")
	}
	if err := e.Approve(run.ID, "gate", "", NodeExpectation{
		NodeID: "gate", Status: "waiting_approval", Attempt: 1,
		ArtifactName: "verify", ArtifactSHA256: artifact.SHA256,
	}); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Run(run.ID)
	if final.Nodes["gate"].Status != "succeeded" {
		t.Fatalf("gate status = %q, want succeeded", final.Nodes["gate"].Status)
	}
}

// TestVerifySidecarRealMavenSOP is the M1 gate end to end: a script node calls
// the sidecar, the sidecar runs Maven in the toolchain container and decides
// from surefire, the artifact is bound into the human gate, and approval
// continues the run.
func TestVerifySidecarRealMavenSOP(t *testing.T) {
	if os.Getenv("LOOPFORGE_VERIFY_E2E") != "1" {
		t.Skip("set LOOPFORGE_VERIFY_E2E=1 to run the real Maven SOP test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}
	repoDir := sidecarMavenProject(t)
	sidecar := verifysidecar.New(repoDir)
	sidecar.AllowTestCommand = true
	sidecar.MavenCacheDir = t.TempDir()
	sidecar.TestTimeout = 15 * time.Minute
	server := httptest.NewServer(sidecar.Handler())
	defer server.Close()

	script := fmt.Sprintf(`curl -sS -m 900 -X POST -H 'Content-Type: application/json' -d '{"repo":"%s","ref":"HEAD","spec":"spec.md","mavenImage":"maven:3.9.11-eclipse-temurin-21","testCommand":"mvn -B -Dmaven.test.failure.ignore=true test"}' %s/verify > verify.json`, repoDir, server.URL)
	wf := domain.WorkflowDefinition{
		ID: "verify-sidecar-real-sop", Name: "Verify sidecar real SOP", SandboxProfileID: "local",
		Nodes: []domain.WorkflowNode{
			{ID: "verify", Name: "Verify", Type: "script", ScriptLanguage: "shell", Script: script, MaxAttempts: 1,
				ArtifactOutputs: []domain.ArtifactContract{{Name: "verify", Path: "verify.json", MediaType: "application/json", Required: true}}},
			{ID: "gate", Name: "Gate", Type: "approval"},
		},
		Edges: []domain.WorkflowEdge{{ID: "verify-gate", Source: "verify", Target: "gate", When: "success"}},
	}
	s, err := repo.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutWorkflow(wf); err != nil {
		t.Fatal(err)
	}
	e := New(s, runtime.NewManagerWithLimit(t.TempDir(), 1))
	run, err := e.Start(wf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, run.ID, 10*time.Minute, func(run domain.Run) bool {
		return run.Status == "waiting" || run.Status == "failed"
	})
	stored, _ := s.Run(run.ID)
	if stored.Status != "waiting" {
		t.Fatalf("run status = %s, want waiting: %+v", stored.Status, stored.Nodes)
	}
	var artifact *domain.Artifact
	for i := range stored.Artifacts {
		if stored.Artifacts[i].Name == "verify" {
			artifact = &stored.Artifacts[i]
		}
	}
	if artifact == nil {
		t.Fatalf("verify artifact missing: %+v", stored.Artifacts)
	}
	var answer struct {
		Acceptance struct {
			Status   string `json:"status"`
			Surefire struct {
				Reports int `json:"reports"`
				Tests   int `json:"tests"`
			} `json:"surefire"`
		} `json:"acceptance"`
	}
	if err := json.Unmarshal([]byte(artifact.Content), &answer); err != nil {
		t.Fatalf("verify artifact is not JSON: %v\n%s", err, artifact.Content)
	}
	if answer.Acceptance.Status != "PASS" || answer.Acceptance.Surefire.Reports == 0 || answer.Acceptance.Surefire.Tests == 0 {
		t.Fatalf("acceptance = %+v (%s)", answer.Acceptance, artifact.Content)
	}

	if err := e.Approve(run.ID, "gate", "", NodeExpectation{
		NodeID: "gate", Status: "waiting_approval", Attempt: 1,
		ArtifactName: "verify", ArtifactSHA256: artifact.SHA256,
	}); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Run(run.ID)
	if final.Nodes["gate"].Status != "succeeded" {
		t.Fatalf("gate status = %q", final.Nodes["gate"].Status)
	}
}

func sidecarMavenProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("spec.md", "# acceptance\n")
	write("pom.xml", `<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>example</groupId>
  <artifactId>sop-e2e</artifactId>
  <version>1</version>
  <properties><maven.compiler.release>17</maven.compiler.release></properties>
  <dependencies>
    <dependency><groupId>org.junit.jupiter</groupId><artifactId>junit-jupiter</artifactId><version>5.10.2</version><scope>test</scope></dependency>
  </dependencies>
  <build><plugins><plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-surefire-plugin</artifactId><version>3.2.5</version></plugin></plugins></build>
</project>
`)
	write("src/test/java/ExampleTest.java", "import org.junit.jupiter.api.Test;\nimport static org.junit.jupiter.api.Assertions.assertEquals;\n\nclass ExampleTest {\n  @Test\n  void passes() { assertEquals(2, 1 + 1); }\n}\n")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	run("init", "-q")
	run("config", "user.email", "sidecar@example.com")
	run("config", "user.name", "sidecar")
	run("add", ".")
	run("commit", "-qm", "init")
	return dir
}
