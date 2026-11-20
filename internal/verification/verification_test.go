package verification

import (
	"context"
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
	git("config", "user.email", "verify@example.com")
	git("config", "user.name", "verify")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pom.xml", "<project/>\n")
	write("spec.md", "# spec\n")
	git("add", ".")
	git("commit", "-qm", "init")
	return dir
}

func TestWorkTreeMatchesCleanHeadAndTracksChanges(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	clean, err := WorkTree(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(gitOutput(t, repo, "rev-parse", "HEAD^{tree}"))
	if clean != head {
		t.Fatalf("clean worktree tree = %s, want HEAD tree %s", clean, head)
	}

	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := WorkTree(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if dirty == clean {
		t.Fatal("worktree hash ignored an untracked file")
	}
	// The real index must stay untouched: HEAD's tree is still the committed one.
	if again := strings.TrimSpace(gitOutput(t, repo, "rev-parse", "HEAD^{tree}")); again != head {
		t.Fatalf("HEAD tree changed after WorkTree: %s", again)
	}
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", repo, "add", "staged.txt").Run()
	if real := strings.TrimSpace(gitOutput(t, repo, "write-tree")); real == dirty {
		t.Fatal("WorkTree used the repository's real index")
	}
}

func TestDiffHashChangesWithContentAndIsStable(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	first, patch, err := DiffHash(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(patch) != 0 {
		t.Fatalf("clean repo produced a patch: %q", patch)
	}
	if err := os.WriteFile(filepath.Join(repo, "spec.md"), []byte("# spec v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, patch, err := DiffHash(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if second == first || len(patch) == 0 {
		t.Fatalf("diff hash did not change: %s -> %s (%d bytes)", first, second, len(patch))
	}
	third, _, err := DiffHash(ctx, repo, "HEAD")
	if err != nil || third != second {
		t.Fatalf("diff hash is not stable: %s vs %s (%v)", second, third, err)
	}
}

func TestBuildFingerprintTracksBuildFilesOnly(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	before, err := BuildFingerprint(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 64 {
		t.Fatalf("build fingerprint = %q, want 64-hex sha256", before)
	}

	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "notes.txt")
	gitRun(t, repo, "commit", "-qm", "notes")
	same, err := BuildFingerprint(ctx, repo, "HEAD")
	if err != nil || same != before {
		t.Fatalf("non-build file changed build fingerprint: %s -> %s (%v)", before, same, err)
	}

	if err := os.WriteFile(filepath.Join(repo, "pom.xml"), []byte("<project><dependencies/></project>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "pom.xml")
	gitRun(t, repo, "commit", "-qm", "pom")
	after, err := BuildFingerprint(ctx, repo, "HEAD")
	if err != nil || after == before {
		t.Fatalf("pom.xml change did not move build fingerprint: %s -> %s (%v)", before, after, err)
	}
}

func TestToolchainFingerprintNamesTheMavenImage(t *testing.T) {
	image := "maven:3.9.11-eclipse-temurin-21"
	a := ToolchainFingerprint(image)
	b := ToolchainFingerprint(image)
	c := ToolchainFingerprint("maven:3.9.9-eclipse-temurin-17")
	if a != b || a == c || len(a) != 64 {
		t.Fatalf("toolchain fingerprints a=%s b=%s c=%s", a, b, c)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(output)
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func TestPrepareRunDirRestoresBaseTestRoots(t *testing.T) {
	repo := testRepo(t)
	testFile := filepath.Join(repo, "src", "test", "java", "ExampleTest.java")
	if err := os.MkdirAll(filepath.Dir(testFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testFile, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-qm", "tests")
	base := strings.TrimSpace(gitOutput(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(testFile, []byte("weakened\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, cleanup, err := PrepareRunDir(context.Background(), repo, base, []string{"src/test/java"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(dir, "src", "test", "java", "ExampleTest.java"))
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != "original\n" {
		t.Fatalf("run dir kept the weakened test: %q", restored)
	}
	current, err := os.ReadFile(testFile)
	if err != nil || string(current) != "weakened\n" {
		t.Fatalf("source repo was modified: %q (%v)", current, err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("run dir not removed: %v", err)
	}
}

func TestPrepareRunDirRejectsMissingBase(t *testing.T) {
	repo := testRepo(t)
	if _, _, err := PrepareRunDir(context.Background(), repo, strings.Repeat("0", 40), []string{"src/test/java"}); err == nil {
		t.Fatal("missing base commit was accepted")
	}
}

func TestPrewarmRequiresCacheDir(t *testing.T) {
	repo := testRepo(t)
	if err := (MavenRunner{}).Prewarm(context.Background(), repo); err == nil {
		t.Fatal("prewarm without a cache directory was accepted")
	}
}
