package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentflow/internal/scriptview"
)

// scriptOwnerDocker is a docker stub for ensureScriptContainer. It answers the
// workspace inspect for "box", the architecture probe only for "base-image",
// and the helper probe with SVO_HELPER (missing helper when empty); every call
// is appended to the returned log and any other command fails.
func scriptOwnerDocker(t *testing.T, arch, helper string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "docker.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$SVO_LOG"
case "$*" in
  "inspect --format {{json .}} box")
    echo '{"Id":"base-id","Image":"base-image","State":{"Running":true,"StartedAt":"started-1"}}'; exit 0 ;;
  "image inspect --format {{.Architecture}} base-image")
    echo "$SVO_ARCH"; exit 0 ;;
  "inspect --format $SVO_PROBE box-scripts")
    if [ -z "$SVO_HELPER" ]; then echo 'Error: No such object: box-scripts' >&2; exit 1; fi
    echo "$SVO_HELPER"; exit 0 ;;
  "exec box-scripts $SVO_BINARY $SVO_PROBE_ARG")
    echo "$SVO_VERSION"; exit 0 ;;
  "rm -f box-scripts"|"start box-scripts"|"cp "*|"create "*) exit 0 ;;
esac
echo "unexpected docker call: $*" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVO_LOG", log)
	t.Setenv("SVO_ARCH", arch)
	t.Setenv("SVO_HELPER", helper)
	t.Setenv("SVO_PROBE", `{{.State.Running}}|{{index .Config.Labels "`+scriptViewLabel+`"}}`)
	t.Setenv("SVO_BINARY", scriptViewBinary)
	t.Setenv("SVO_PROBE_ARG", scriptview.ProbeArgument)
	t.Setenv("SVO_VERSION", scriptview.Version)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// scriptOwnerHelper deploys an arm64 helper binary and returns its path and
// the owner label ensureScriptContainer must derive for the stubbed workspace.
func scriptOwnerHelper(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	contents := []byte("helper-build-1")
	binary := filepath.Join(dir, "helper-linux-arm64")
	if err := os.WriteFile(binary, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_GATE_MCP_BINARY", filepath.Join(dir, "helper-{os}-{arch}"))
	sum := sha256.Sum256(contents)
	return binary, "base-id|started-1|" + scriptview.Version + "|" + hex.EncodeToString(sum[:])
}

func readScriptOwnerLog(t *testing.T, log string) []string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// A running helper whose label matches the workspace generation and the
// helper build is reused: the owner is derived from the workspace image's
// architecture, the container ID, its start time and the helper digest.
func TestEnsureScriptContainerReusesHelperWithMatchingOwner(t *testing.T) {
	_, owner := scriptOwnerHelper(t)
	log := scriptOwnerDocker(t, "arm64", "true|"+owner)
	sb := &dockerSandbox{name: "box", workspace: t.TempDir(), started: true}
	if err := sb.ensureScriptContainer(context.Background()); err != nil {
		t.Fatalf("ensureScriptContainer() error = %v", err)
	}
	for _, call := range readScriptOwnerLog(t, log) {
		if strings.HasPrefix(call, "create ") || strings.HasPrefix(call, "rm ") || strings.HasPrefix(call, "cp ") {
			t.Fatalf("matching helper must be reused, got docker %q", call)
		}
	}
}

// A helper labelled for another helper build is replaced, and the new
// container carries the current owner and receives the resolved binary.
func TestEnsureScriptContainerRecreatesHelperWithStaleOwner(t *testing.T) {
	binary, owner := scriptOwnerHelper(t)
	stale := "base-id|started-1|" + scriptview.Version + "|0000"
	log := scriptOwnerDocker(t, "arm64", "true|"+stale)
	workspace := t.TempDir()
	sb := &dockerSandbox{name: "box", workspace: workspace, started: true}
	if err := sb.ensureScriptContainer(context.Background()); err != nil {
		t.Fatalf("ensureScriptContainer() error = %v", err)
	}
	calls := readScriptOwnerLog(t, log)
	want := []string{
		"rm -f box-scripts",
		strings.Join(dockerScriptCreateArgs("box-scripts", "box", workspace, "base-image", owner), " "),
		"cp " + binary + " box-scripts:" + scriptViewBinary,
		"start box-scripts",
	}
	next := 0
	for _, call := range calls {
		if next < len(want) && call == want[next] {
			next++
		}
	}
	if next != len(want) {
		t.Fatalf("docker calls = %q, missing %q in order", calls, want[next])
	}
}

// When the helper cannot be resolved for the image architecture the helper
// container is left alone and the resolution error is returned as is.
func TestEnsureScriptContainerStopsWhenHelperIsUnavailable(t *testing.T) {
	scriptOwnerHelper(t)
	log := scriptOwnerDocker(t, "s390x", "")
	sb := &dockerSandbox{name: "box", workspace: t.TempDir(), started: true}
	err := sb.ensureScriptContainer(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "script view helper unavailable:") {
		t.Fatalf("ensureScriptContainer() error = %v, want helper unavailable", err)
	}
	for _, call := range readScriptOwnerLog(t, log) {
		if strings.Contains(call, "box-scripts") {
			t.Fatalf("helper container must not be touched, got docker %q", call)
		}
	}
}
