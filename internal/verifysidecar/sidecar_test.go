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
	server := httptest.NewServer(New(repo, "").Handler())
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
	sidecar := New(repo, "")
	server := httptest.NewServer(sidecar.Handler())
	defer server.Close()

	status, body := postJSON(t, server, "/verify", map[string]any{"repo": repo, "spec": "spec.md", "testCommand": "exit 3"})
	if status != http.StatusOK {
		t.Fatalf("verify = %d %v", status, body)
	}
	if acceptance, _ := body["acceptance"].(map[string]any); acceptance["status"] != "SKIPPED" {
		t.Fatalf("disabled test command still ran: %v", acceptance)
	}

	sidecar.AllowTestCommand = true
	status, body = postJSON(t, server, "/verify", map[string]any{"repo": repo, "spec": "spec.md", "testCommand": "exit 3"})
	if status != http.StatusOK {
		t.Fatalf("verify = %d %v", status, body)
	}
	acceptance, _ := body["acceptance"].(map[string]any)
	if acceptance["status"] != "FAIL" || acceptance["exitCode"] != float64(3) {
		t.Fatalf("acceptance = %v, want FAIL exit 3", acceptance)
	}
}

func TestVerifyRejectsRepoOutsideRoot(t *testing.T) {
	inside := testRepo(t)
	outside := testRepo(t)
	server := httptest.NewServer(New(inside, "").Handler())
	defer server.Close()

	status, body := postJSON(t, server, "/verify", map[string]any{"repo": outside, "spec": "spec.md"})
	if status != http.StatusBadRequest {
		t.Fatalf("outside repo = %d %v, want 400", status, body)
	}
}

func TestCritiqueRejectsUntilRecordedThenPasses(t *testing.T) {
	repo := testRepo(t)
	critiqueFile := filepath.Join(t.TempDir(), "critique.json")
	server := httptest.NewServer(New(repo, critiqueFile).Handler())
	defer server.Close()
	h := strings.Repeat("a", 64)
	key := "diffHash"

	status, body := postJSON(t, server, "/critique", map[string]any{key: h})
	if status != http.StatusOK || body["verdict"] != "REJECT" {
		t.Fatalf("unrecorded critique = %d %v, want REJECT", status, body)
	}
	if body[key] != h {
		t.Fatalf("critique did not echo the diff hash: %v", body)
	}

	recorded, _ := json.Marshal(map[string]any{h: map[string]string{"verdict": "pass", "summary": "no blocking findings"}})
	if err := os.WriteFile(critiqueFile, recorded, 0o644); err != nil {
		t.Fatal(err)
	}
	status, body = postJSON(t, server, "/critique", map[string]any{key: h})
	if status != http.StatusOK || body["verdict"] != "PASS" || body["summary"] != "no blocking findings" {
		t.Fatalf("recorded critique = %d %v, want PASS", status, body)
	}

	status, _ = postJSON(t, server, "/critique", map[string]any{key: "short"})
	if status != http.StatusBadRequest {
		t.Fatalf("short diff hash = %d, want 400", status)
	}
}

func TestDeliveryCheckBindsCommitToVerifiedTree(t *testing.T) {
	repo := testRepo(t)
	server := httptest.NewServer(New(repo, "").Handler())
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
