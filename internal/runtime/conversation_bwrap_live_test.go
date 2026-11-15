package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLiveBwrapConversationWorkspace runs the real bubblewrap with the
// conversation workspace arguments and checks the layout from inside. Opt in
// with LOOPFORGE_LIVE_BWRAP=1 on a machine that allows unprivileged user
// namespaces.
func TestLiveBwrapConversationWorkspace(t *testing.T) {
	if os.Getenv("LOOPFORGE_LIVE_BWRAP") == "" {
		t.Skip("set LOOPFORGE_LIVE_BWRAP=1 to run against the real bwrap")
	}
	if out, err := exec.Command("bwrap", "--ro-bind", "/", "/", "--unshare-user", "true").CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot create a sandbox here: %v %s", err, out)
	}
	daemon := fakeDockerDaemon(t)
	base, err := os.MkdirTemp("/tmp", "lfbw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root := filepath.Join(base, "conversation")
	workspace := filepath.Join(base, "workspace")
	for _, dir := range []string{filepath.Join(root, "home"), filepath.Join(root, "ws"), workspace} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	args, err := bwrapArgsRequirements(workspace, SandboxRequirements{ConversationRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	args = bwrapChdir(args, filepath.Join(root, "ws"))
	guard := filepath.Join(root, conversationGuardFiles[0])
	script := strings.Join([]string{
		"set -u",
		`[ "$(pwd)" = "` + filepath.Join(root, "ws") + `" ] || echo FAIL-cwd`,
		"echo hi > from-sandbox || echo FAIL-ws-write",
		"[ -f " + filepath.Join(workspace, "from-sandbox") + " ] || echo FAIL-host-path",
		"touch " + filepath.Join(root, "home", "note") + " || echo FAIL-home-write",
		"( echo x > " + guard + " ) 2>/dev/null && echo FAIL-guard-writable",
		"( touch /etc/lf-escape ) 2>/dev/null && echo FAIL-root-writable",
		"[ -S " + daemon + " ] || echo FAIL-host-docker-missing",
		"echo DONE",
	}, "\n")
	out, err := exec.Command("bwrap", append(args, "sh", "-c", script)...).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "DONE") || strings.Contains(string(out), "FAIL") {
		t.Fatalf("inside bwrap: %v\n%s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "from-sandbox")); err != nil || string(data) != "hi\n" {
		t.Fatalf("the cwd must be the run workspace: %q %v", data, err)
	}
}
