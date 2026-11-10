package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func requireLinuxProc(t *testing.T) {
	t.Helper()
	if stdruntime.GOOS != "linux" {
		t.Skip("index lock holders are scanned through /proc on Linux")
	}
}

func agentIndexRepo(t *testing.T) (workspace, lockPath string) {
	t.Helper()
	workspace = t.TempDir()
	testRuntimeGit(t, workspace, "init", "-b", "main")
	testRuntimeGit(t, workspace, "config", "user.email", "agentflow@test.local")
	testRuntimeGit(t, workspace, "config", "user.name", "AgentFlow Test")
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testRuntimeGit(t, workspace, "add", "AGENTS.md")
	testRuntimeGit(t, workspace, "commit", "-m", "baseline")
	if err := ProtectProjectAgentGitIndex(context.Background(), workspace, "docker"); err != nil {
		t.Fatal(err)
	}
	return workspace, filepath.Join(workspace, ".git", "index.lock")
}

func TestGitIndexLockMatchesHolderByFileIdentity(t *testing.T) {
	requireLinuxProc(t)
	dir := t.TempDir()
	lockPath, alias := filepath.Join(dir, "index.lock"), filepath.Join(dir, "alias")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if inUse, confirmed, err := gitIndexLockInUseProc(lockPath); err != nil || !confirmed || inUse {
		t.Fatalf("stale lock inUse=%v confirmed=%v err=%v", inUse, confirmed, err)
	}
	if err := os.Link(lockPath, alias); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(alias, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if inUse, confirmed, err := gitIndexLockInUseProc(lockPath); err != nil || !confirmed || !inUse {
		t.Fatalf("alternate-path holder inUse=%v confirmed=%v err=%v", inUse, confirmed, err)
	}
	_ = holder.Close()
	if inUse, confirmed, err := gitIndexLockInUseProc(lockPath); err != nil || !confirmed || inUse {
		t.Fatalf("released alias inUse=%v confirmed=%v err=%v", inUse, confirmed, err)
	}
}

func TestRestoreProjectAgentGitIndexKeepsAlternatePathLock(t *testing.T) {
	requireLinuxProc(t)
	workspace, lockPath := agentIndexRepo(t)
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(lockPath)
	alias := filepath.Join(t.TempDir(), "sandbox-view-index.lock")
	if err := os.Link(lockPath, alias); err != nil {
		t.Skipf("hard link across temp dirs: %v", err)
	}
	holder, err := os.OpenFile(alias, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	err = RestoreProjectAgentGitIndex(context.Background(), workspace, "docker")
	if err == nil || !strings.Contains(err.Error(), "held by a live process") {
		t.Fatalf("restore error = %v, want live-process protection", err)
	}
	same, statErr := os.Stat(lockPath)
	if statErr != nil {
		t.Fatalf("live index lock was removed: %v", statErr)
	}
	if held, _ := holder.Stat(); !os.SameFile(same, held) {
		t.Fatal("live index lock was replaced")
	}
	if flag := testRuntimeGit(t, workspace, "ls-files", "-v", "--", "AGENTS.md"); !strings.HasPrefix(flag, "S ") {
		t.Fatalf("index changed under a live lock: %q", flag)
	}
}

// A sandbox sees the workspace through a bind mount in its own mount
// namespace, so its descriptor link names a path the host never uses.
func TestGitIndexLockMatchesBindMountedHolder(t *testing.T) {
	requireLinuxProc(t)
	if os.Geteuid() != 0 {
		t.Skip("bind mounts in a private mount namespace need root")
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare is unavailable")
	}
	workspace, view := t.TempDir(), t.TempDir()
	lockPath := filepath.Join(workspace, "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	script := `mount --bind "$1" "$2" && exec 3<"$2/index.lock" && touch "$3" && exec sleep 60`
	cmd := exec.Command(unshare, "--mount", "--propagation", "private", "sh", "-c", script, "sh", workspace, view, ready)
	if err := cmd.Start(); err != nil {
		t.Skipf("start mount namespace: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Skip("bind mount holder did not start (mount namespaces may be restricted)")
		}
		time.Sleep(20 * time.Millisecond)
	}
	target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "fd", "3"))
	if err != nil || target == lockPath {
		t.Fatalf("holder link %q (%v) does not exercise an alternate path", target, err)
	}
	if inUse, confirmed, err := gitIndexLockInUseProc(lockPath); err != nil || !confirmed || !inUse {
		t.Fatalf("bind-mounted holder inUse=%v confirmed=%v err=%v", inUse, confirmed, err)
	}
}

func TestGitIndexLockUnreadableHolderStaysUnconfirmed(t *testing.T) {
	requireLinuxProc(t)
	lockPath := filepath.Join(t.TempDir(), "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	owner := strconv.Itoa(os.Getuid())
	other := strconv.Itoa(os.Getuid() + 4242)
	// fakeProc builds one process whose descriptor table cannot be read.
	fakeProc := func(status string) string {
		root := t.TempDir()
		pid := filepath.Join(root, "4242")
		if err := os.MkdirAll(pid, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pid, "fd"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if status != "" {
			if err := os.WriteFile(filepath.Join(pid, "status"), []byte(status), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	// The lock below is owned by this test's uid. "foreign" simulates a runner
	// that is another user (e.g. a non-root runner facing a lock created by
	// root in a rootful container); "self" is the runner's own lock.
	foreign, self := uint32(os.Geteuid()+1), uint32(os.Geteuid())
	for name, tc := range map[string]struct {
		status    string
		runner    uint32
		confirmed bool
	}{
		"foreign/owner":         {"Name:\tgit\nUid:\t" + other + "\t" + owner + "\t" + other + "\t" + owner + "\n", foreign, false},
		"foreign/unparsable":    {"Name:\tgit\nUid:\tbad\n", foreign, false},
		"foreign/missing-uid":   {"Name:\tgit\n", foreign, false},
		"foreign/other-user":    {"Name:\tsshd\nUid:\t" + other + "\t" + other + "\t" + other + "\t" + other + "\n", foreign, true},
		"foreign/exited-status": {"", foreign, true},
		// Same-user processes hidden from us are non-dumpable helpers (for
		// example a browser sandbox); they must not disable stale cleanup.
		"self/non-dumpable": {"Name:\tchrome\nUid:\t" + owner + "\t" + owner + "\t" + owner + "\t" + owner + "\n", self, true},
	} {
		t.Run(name, func(t *testing.T) {
			inUse, confirmed, err := gitIndexLockHeldUnder(fakeProc(tc.status), lockPath, tc.runner)
			if err != nil || inUse || confirmed != tc.confirmed {
				t.Fatalf("inUse=%v confirmed=%v err=%v, want confirmed=%v", inUse, confirmed, err, tc.confirmed)
			}
		})
	}
	// A foreign lock nobody has written for gitIndexLockForeignStaleAfter is
	// reclaimable even though unreadable processes may run as its owner.
	ownerStatus := "Name:\tgit\nUid:\t" + owner + "\t" + owner + "\t" + owner + "\t" + owner + "\n"
	old := time.Now().Add(-gitIndexLockForeignStaleAfter - time.Minute)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	if inUse, confirmed, err := gitIndexLockHeldUnder(fakeProc(ownerStatus), lockPath, foreign); err != nil || inUse || !confirmed {
		t.Fatalf("expired foreign lock inUse=%v confirmed=%v err=%v, want confirmed stale", inUse, confirmed, err)
	}
	recent := time.Now().Add(-gitIndexLockForeignStaleAfter + time.Minute)
	if err := os.Chtimes(lockPath, recent, recent); err != nil {
		t.Fatal(err)
	}
	if _, confirmed, err := gitIndexLockHeldUnder(fakeProc(ownerStatus), lockPath, foreign); err != nil || confirmed {
		t.Fatalf("recent foreign lock confirmed=%v err=%v, want unconfirmed", confirmed, err)
	}
	// A readable live holder keeps even an old lock.
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	holder, err := os.Open(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if inUse, confirmed, err := gitIndexLockHeldUnder("/proc", lockPath, foreign); err != nil || !inUse || !confirmed {
		t.Fatalf("old lock with a live holder inUse=%v confirmed=%v err=%v", inUse, confirmed, err)
	}
	holder.Close()
	// A symlinked lock is not a lock Git created; never confirm it stale.
	link := filepath.Join(t.TempDir(), "index.lock")
	if err := os.Symlink(lockPath, link); err != nil {
		t.Fatal(err)
	}
	if _, confirmed, err := gitIndexLockInUseProc(link); confirmed || err == nil {
		t.Fatalf("symlinked lock confirmed=%v err=%v", confirmed, err)
	}
}
