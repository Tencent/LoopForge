package verifysidecar

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/critique"
)

func testRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	git("config", "user.email", "sidecar@example.com")
	git("config", "user.name", "sidecar")
	if err := os.WriteFile(filepath.Join(dir, "spec.md"), []byte("# acceptance\nmust pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte("<project/>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "init")
	return dir
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

func postJSON(t *testing.T, server *httptest.Server, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(server.URL+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded := map[string]any{}
	_ = json.Unmarshal(payload, &decoded)
	return response.StatusCode, decoded
}

func TestVerifyReturnsTreeSpecAndFingerprints(t *testing.T) {
	repo := testRepo(t)
	server := httptest.NewServer(New(repo).Handler())
	defer server.Close()

	status, body := postJSON(t, server, "/verify", map[string]any{"repo": repo, "ref": "HEAD", "spec": "spec.md"})
	if status != http.StatusOK {
		t.Fatalf("verify = %d %v", status, body)
	}
	wantTree := gitOutput(t, repo, "rev-parse", "HEAD^{tree}")
	if body["treeHash"] != wantTree || len(wantTree) != 40 {
		t.Fatalf("treeHash = %v, want %s", body["treeHash"], wantTree)
	}
	sum := sha256.Sum256([]byte("# acceptance\nmust pass\n"))
	if body["specHash"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("specHash = %v", body["specHash"])
	}
	for _, key := range []string{"toolchainFp", "buildFp"} {
		value, _ := body[key].(string)
		if len(value) != 64 {
			t.Fatalf("%s = %q, want 64-hex sha256", key, value)
		}
	}
	if body["buildFp"] == sha256Hex(nil) {
		t.Fatalf("buildFp ignored pom.xml: %v", body["buildFp"])
	}
	acceptance, _ := body["acceptance"].(map[string]any)
	if acceptance["status"] != "SKIPPED" {
		t.Fatalf("acceptance = %v, want SKIPPED without a test command", acceptance)
	}
}

func TestVerifyRunsTestCommandOnlyWhenAllowed(t *testing.T) {
	repo := testRepo(t)
	sidecar := New(repo)
	server := httptest.NewServer(sidecar.Handler())
	defer server.Close()

	status, body := postJSON(t, server, "/verify", map[string]any{"repo": repo, "spec": "spec.md", "testCommand": "true"})
	if status != http.StatusOK {
		t.Fatalf("verify = %d %v", status, body)
	}
	if acceptance, _ := body["acceptance"].(map[string]any); acceptance["status"] != "SKIPPED" {
		t.Fatalf("disabled test command still ran: %v", acceptance)
	}

	// Enabled commands run inside the Maven toolchain container. Without a
	// working Docker daemon the sidecar must report FAIL, never PASS.
	sidecar.AllowTestCommand = true
	status, body = postJSON(t, server, "/verify", map[string]any{"repo": repo, "spec": "spec.md", "testCommand": "true"})
	if status != http.StatusOK {
		t.Fatalf("verify = %d %v", status, body)
	}
	acceptance, _ := body["acceptance"].(map[string]any)
	if acceptance["status"] == "PASS" && acceptance["surefire"] == nil {
		t.Fatalf("PASS without surefire evidence: %v", acceptance)
	}
	if acceptance["status"] == "SKIPPED" {
		t.Fatalf("enabled test command was skipped: %v", acceptance)
	}
}

func TestVerifyWorktreeHashesUncommittedFiles(t *testing.T) {
	repo := testRepo(t)
	server := httptest.NewServer(New(repo).Handler())
	defer server.Close()

	cleanTree := gitOutput(t, repo, "rev-parse", "HEAD^{tree}")
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, body := postJSON(t, server, "/verify", map[string]any{"repo": repo, "spec": "spec.md", "worktree": true})
	if status != http.StatusOK {
		t.Fatalf("verify = %d %v", status, body)
	}
	if body["treeHash"] == cleanTree {
		t.Fatalf("worktree verify ignored an uncommitted file: %v", body)
	}
}

func TestVerifyRejectsRepoOutsideRoot(t *testing.T) {
	inside := testRepo(t)
	outside := testRepo(t)
	server := httptest.NewServer(New(inside).Handler())
	defer server.Close()

	status, body := postJSON(t, server, "/verify", map[string]any{"repo": outside, "spec": "spec.md"})
	if status != http.StatusBadRequest {
		t.Fatalf("outside repo = %d %v, want 400", status, body)
	}
}

func TestCritiqueRejectsWithoutModelThenUsesModel(t *testing.T) {
	repo := testRepo(t)
	sidecar := New(repo)
	server := httptest.NewServer(sidecar.Handler())
	defer server.Close()
	patch := "diff --git a/A.java b/A.java\n"
	sum := sha256.Sum256([]byte(patch))
	h := hex.EncodeToString(sum[:])
	key := "diffHash"

	status, body := postJSON(t, server, "/critique", map[string]any{key: h, "patch": patch})
	if status != http.StatusOK || body["verdict"] != "REJECT" {
		t.Fatalf("unconfigured critique = %d %v, want REJECT", status, body)
	}
	if !strings.Contains(body["summary"].(string), "not configured") {
		t.Fatalf("unconfigured summary = %v", body["summary"])
	}

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": `{"verdict":"PASS","summary":"reviewed"}`}}},
		})
	}))
	defer model.Close()
	sidecar.Critique = critique.Config{URL: model.URL, Key: "test-key", Model: "critic", Client: model.Client()}

	status, body = postJSON(t, server, "/critique", map[string]any{key: h, "patch": patch})
	if status != http.StatusOK || body["verdict"] != "PASS" || body["summary"] != "reviewed" {
		t.Fatalf("model critique = %d %v, want PASS", status, body)
	}

	status, _ = postJSON(t, server, "/critique", map[string]any{key: "short", "patch": patch})
	if status != http.StatusBadRequest {
		t.Fatalf("short diff hash = %d, want 400", status)
	}
}

func TestDeliveryCheckBindsCommitToVerifiedTree(t *testing.T) {
	repo := testRepo(t)
	server := httptest.NewServer(New(repo).Handler())
	defer server.Close()
	commit := gitOutput(t, repo, "rev-parse", "HEAD")
	tree := gitOutput(t, repo, "rev-parse", "HEAD^{tree}")

	status, body := postJSON(t, server, "/delivery-check", map[string]any{"repo": repo, "commit": commit, "verifiedTree": tree})
	if status != http.StatusOK || body["ok"] != true {
		t.Fatalf("delivery-check = %d %v, want ok", status, body)
	}

	status, body = postJSON(t, server, "/delivery-check", map[string]any{"repo": repo, "commit": commit, "verifiedTree": strings.Repeat("0", 40)})
	if status != http.StatusOK || body["ok"] != false {
		t.Fatalf("mismatched delivery-check = %d %v, want not ok", status, body)
	}
}
