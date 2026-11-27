package gitwork

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type patchBaselineFixture struct {
	ctx       context.Context
	t         *testing.T
	origin    string
	workspace string
	commits   int
}

func newPatchBaselineFixture(t *testing.T) (*patchBaselineFixture, string) {
	t.Helper()
	root := t.TempDir()
	f := &patchBaselineFixture{
		ctx:       context.Background(),
		t:         t,
		origin:    filepath.Join(root, "origin.git"),
		workspace: filepath.Join(root, "workspace"),
	}
	if _, err := C(f.ctx, root, "init", "--bare", "-q", "-b", "main", f.origin); err != nil {
		t.Fatal(err)
	}
	if _, err := C(f.ctx, root, "clone", "-q", f.origin, f.workspace); err != nil {
		t.Fatal(err)
	}
	base := f.commit("base")
	f.publish("main", base)
	f.git("remote", "set-head", "origin", "--auto")
	return f, base
}

func (f *patchBaselineFixture) git(args ...string) string {
	f.t.Helper()
	out, err := C(f.ctx, f.workspace, args...)
	if err != nil {
		f.t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

func (f *patchBaselineFixture) commit(message string) string {
	f.t.Helper()
	f.commits++
	name := fmt.Sprintf("commit-%02d.txt", f.commits)
	if err := os.WriteFile(filepath.Join(f.workspace, name), []byte(message+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.git("add", name)
	f.git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", message)
	return f.git("rev-parse", "HEAD")
}

func (f *patchBaselineFixture) publish(branch, sha string) {
	f.t.Helper()
	f.git("push", "-q", "origin", sha+":refs/heads/"+branch)
	f.git("fetch", "-q", "origin")
}

func checkPatchBaseline(t *testing.T, workspace, recorded, want string) {
	t.Helper()
	got := nearestPatchBaseline(workspace, recorded)
	if got != want {
		t.Fatalf("nearestPatchBaseline() = %s, want %s", got, want)
	}
	if old := nearestPatchBaselineByScan(workspace, recorded); got != old {
		t.Fatalf("nearestPatchBaseline() = %s, scan = %s", got, old)
	}
}

func TestNearestPatchBaselineKeepsRecordedWithoutNearerRef(t *testing.T) {
	f, _ := newPatchBaselineFixture(t)
	recorded := f.commit("recorded")
	f.commit("local one")
	f.commit("local two")
	checkPatchBaseline(t, f.workspace, recorded, recorded)
}

func TestNearestPatchBaselineFindsPublishedWork(t *testing.T) {
	f, recorded := newPatchBaselineFixture(t)
	middle := f.commit("published work")
	f.commit("local work")
	f.publish("progress", middle)
	checkPatchBaseline(t, f.workspace, recorded, middle)
}

func TestNearestPatchBaselineFindsMergedUpstream(t *testing.T) {
	f, recorded := newPatchBaselineFixture(t)
	f.git("checkout", "-q", "-b", "work")
	f.commit("local work")
	f.git("checkout", "-q", "main")
	upstream := f.commit("upstream work")
	f.publish("main", upstream)
	f.git("checkout", "-q", "work")
	f.git("-c", "user.name=Test", "-c", "user.email=test@example.com", "merge", "-q", "--no-ff", "-m", "merge main", "origin/main")
	checkPatchBaseline(t, f.workspace, recorded, upstream)
}

func TestNearestPatchBaselineSkipsRefAtHead(t *testing.T) {
	f, recorded := newPatchBaselineFixture(t)
	head := f.commit("published head")
	f.publish("work", head)
	checkPatchBaseline(t, f.workspace, recorded, recorded)
}

func TestNearestPatchBaselineIgnoresOriginHead(t *testing.T) {
	f, recorded := newPatchBaselineFixture(t)
	middle := f.commit("origin head only")
	f.commit("local work")
	f.git("update-ref", "--no-deref", "refs/remotes/origin/HEAD", middle)
	checkPatchBaseline(t, f.workspace, recorded, recorded)
}

func TestNearestPatchBaselineWithInvalidRecorded(t *testing.T) {
	f, _ := newPatchBaselineFixture(t)
	middle := f.commit("published work")
	f.publish("main", middle)
	f.commit("local work")
	recorded := strings.Repeat("f", 40)
	checkPatchBaseline(t, f.workspace, recorded, middle)
}

func TestNearestPatchBaselineWithManyUnrelatedRefs(t *testing.T) {
	f, old := newPatchBaselineFixture(t)
	recorded := f.commit("recorded")
	f.publish("main", recorded)
	middle := f.commit("published work")
	f.commit("local work")
	f.publish("progress", middle)
	for i := 0; i < 300; i++ {
		name := fmt.Sprintf("refs/heads/unrelated-%03d", i)
		if _, err := C(f.ctx, f.origin, "update-ref", name, old); err != nil {
			t.Fatal(err)
		}
	}
	f.git("fetch", "-q", "origin")
	checkPatchBaseline(t, f.workspace, recorded, middle)
}

func nearestPatchBaselineByScan(workspace, recorded string) string {
	headOut, err := exec.Command("git", "-C", workspace, "rev-parse", "HEAD").Output()
	if err != nil {
		return recorded
	}
	head := strings.TrimSpace(string(headOut))
	best := strings.TrimSpace(recorded)
	bestCount, bestOK := revListCount(workspace, best, head)
	if !bestOK {
		bestCount = int(^uint(0) >> 1)
	}
	refs, err := exec.Command("git", "-C", workspace, "for-each-ref", "--format=%(objectname) %(refname:short)", "refs/remotes/origin").Output()
	if err != nil {
		return best
	}
	for _, line := range strings.Split(string(refs), "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || sha == "" || sha == head {
			continue
		}
		if strings.EqualFold(name, "origin/HEAD") {
			continue
		}
		if exec.Command("git", "-C", workspace, "merge-base", "--is-ancestor", sha, head).Run() != nil {
			continue
		}
		n, ok := revListCount(workspace, sha, head)
		if !ok {
			continue
		}
		if n < bestCount {
			best, bestCount = sha, n
		}
	}
	if best == "" {
		return recorded
	}
	return best
}
