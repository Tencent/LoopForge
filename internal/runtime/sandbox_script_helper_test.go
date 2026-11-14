package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agentflow/internal/scriptview"
)

func TestScriptViewOwnerJoinsGenerationAndHelperIdentity(t *testing.T) {
	got := scriptViewOwner("abc123", "2026-11-14T09:00:00Z", "deadbeef")
	want := "abc123|2026-11-14T09:00:00Z|" + scriptview.Version + "|deadbeef"
	if got != want {
		t.Fatalf("scriptViewOwner() = %q, want %q", got, want)
	}
	if scriptViewOwner("abc123", "2026-11-14T09:00:01Z", "deadbeef") == got {
		t.Fatal("a restarted workspace container must produce a different owner")
	}
	if scriptViewOwner("abc123", "2026-11-14T09:00:00Z", "cafef00d") == got {
		t.Fatal("a rebuilt helper binary must produce a different owner")
	}
}

// fakeDockerArchitecture puts a docker stub on PATH that answers
// `docker image inspect` with the given output and exit code.
func fakeDockerArchitecture(t *testing.T, output string, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' '" + output + "'\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestScriptViewHelperResolvesBinaryForImageArchitecture(t *testing.T) {
	dir := t.TempDir()
	contents := []byte("helper-binary")
	binary := filepath.Join(dir, "helper-linux-arm64")
	if err := os.WriteFile(binary, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", filepath.Join(dir, "helper-{os}-{arch}"))
	fakeDockerArchitecture(t, "arm64", 0)

	gotBinary, gotDigest, err := scriptViewHelper(context.Background(), "image")
	if err != nil {
		t.Fatalf("scriptViewHelper() error = %v", err)
	}
	sum := sha256.Sum256(contents)
	if gotBinary != binary || gotDigest != hex.EncodeToString(sum[:]) {
		t.Fatalf("scriptViewHelper() = %q, %q; want %q, %q", gotBinary, gotDigest, binary, hex.EncodeToString(sum[:]))
	}
}

func TestScriptViewHelperReportsArchitectureProbeFailure(t *testing.T) {
	fakeDockerArchitecture(t, "no such image", 1)
	_, _, err := scriptViewHelper(context.Background(), "missing")
	if err == nil || !strings.HasPrefix(err.Error(), "inspect script environment architecture:") {
		t.Fatalf("scriptViewHelper() error = %v, want architecture probe failure", err)
	}
}

func TestScriptViewHelperRejectsUnsupportedArchitecture(t *testing.T) {
	fakeDockerArchitecture(t, "s390x", 0)
	_, _, err := scriptViewHelper(context.Background(), "image")
	if err == nil || !strings.HasPrefix(err.Error(), "script view helper unavailable:") {
		t.Fatalf("scriptViewHelper() error = %v, want helper unavailable", err)
	}
}
