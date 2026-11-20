package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceCheckpointRoundTrip(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{"README.md": []byte("uncommitted\n"), "src/tool.sh": []byte("#!/bin/sh\n"), "binary": {0, 255, 1}, ".git/HEAD": []byte("ref: refs/heads/main\n"), ".agentflow/auth/token": []byte("secret"), "node_modules/huge": []byte("cache")}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("src/tool.sh", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	archive, err := CreateWorkspaceCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if err = RestoreWorkspaceCheckpoint(dest, archive); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"README.md", "src/tool.sh", "binary", ".git/HEAD"} {
		body, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || !bytes.Equal(body, files[name]) {
			t.Fatalf("%s lost: %q %v", name, body, err)
		}
	}
	for _, name := range []string{".agentflow", "node_modules"} {
		if _, err := os.Stat(filepath.Join(dest, name)); !os.IsNotExist(err) {
			t.Fatalf("excluded path restored: %s", name)
		}
	}
	if info, err := os.Stat(filepath.Join(dest, "src/tool.sh")); err != nil || info.Mode().Perm()&0100 == 0 {
		t.Fatal("executable mode lost")
	}
	if link, err := os.Readlink(filepath.Join(dest, "link")); err != nil || link != "src/tool.sh" {
		t.Fatal("link lost")
	}
	if err = RestoreWorkspaceCheckpoint(dest, archive); err == nil {
		t.Fatal("existing destination overwritten")
	}
}
func makeCheckpoint(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	w := tar.NewWriter(z)
	for _, h := range headers {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestWorkspaceCheckpointRejectsUnsafeAndCorruptArchivesAtomically(t *testing.T) {
	cases := map[string][]byte{"traversal": makeCheckpoint(t, &tar.Header{Name: "../escape", Mode: 0600, Typeflag: tar.TypeReg}), "absolute": makeCheckpoint(t, &tar.Header{Name: "/escape", Mode: 0600, Typeflag: tar.TypeReg}), "symlink-parent": makeCheckpoint(t, &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../escape"}, &tar.Header{Name: "link/pwned", Mode: 0600, Typeflag: tar.TypeReg}), "secret": makeCheckpoint(t, &tar.Header{Name: ".agentflow/token", Typeflag: tar.TypeReg}), "duplicate": makeCheckpoint(t, &tar.Header{Name: "file", Typeflag: tar.TypeReg}, &tar.Header{Name: "file", Typeflag: tar.TypeReg}), "hardlink": makeCheckpoint(t, &tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "escape"})}
	good := EmptyWorkspaceCheckpoint()
	cases["truncated"] = good[:len(good)-4]
	bad := append([]byte{}, good...)
	bad[len(bad)-5] ^= 0xff
	cases["checksum"] = bad
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "workspace")
			if err := RestoreWorkspaceCheckpoint(dest, data); err == nil {
				t.Fatal("invalid archive accepted")
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatal("partial workspace exposed")
			}
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Fatal("staging files leaked")
			}
		})
	}
}

// Repositories may commit absolute or dangling links (for example an editor
// skills directory pointing at a developer's home). They must not block the
// save that authorizes reclamation, and restore must reproduce them exactly.
func TestWorkspaceCheckpointPreservesExternalSymlinks(t *testing.T) {
	root := t.TempDir()
	links := map[string]string{"server/.claude/skills": "/workdir/Users/dev/skills", "tools/run/app": "../../../outside/app", "local": "server"}
	for name, target := range links {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := CreateWorkspaceCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateWorkspaceCheckpoint(archive); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if err = RestoreWorkspaceCheckpoint(dest, archive); err != nil {
		t.Fatal(err)
	}
	for name, target := range links {
		if got, err := os.Readlink(filepath.Join(dest, name)); err != nil || got != target {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
}

// Restoring an external link is safe only because nothing is ever written
// through it: a later entry below the link must fail atomically.
func TestWorkspaceCheckpointRestoreNeverWritesThroughExternalSymlinks(t *testing.T) {
	for name, target := range map[string]func(outside string) string{
		"absolute": func(outside string) string { return outside },
		"relative": func(string) string { return "../outside" },
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			outside := filepath.Join(parent, "outside")
			if err := os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			for _, entry := range []*tar.Header{
				{Name: "pwned", Mode: 0600, Typeflag: tar.TypeReg},
				{Name: "sub/pwned", Mode: 0600, Typeflag: tar.TypeReg},
				{Name: "sub", Mode: 0700, Typeflag: tar.TypeDir},
			} {
				data := makeCheckpoint(t, &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: target(outside)}, &tar.Header{Name: "link/" + entry.Name, Mode: entry.Mode, Typeflag: entry.Typeflag})
				dest := filepath.Join(parent, "workspace")
				if err := RestoreWorkspaceCheckpoint(dest, data); err == nil {
					t.Fatalf("%s written through external link", entry.Name)
				}
				if _, err := os.Lstat(dest); !os.IsNotExist(err) {
					t.Fatal("partial workspace exposed")
				}
				if escaped, _ := os.ReadDir(outside); len(escaped) != 0 {
					t.Fatalf("restore escaped workspace: %v", escaped)
				}
				if entries, _ := os.ReadDir(parent); len(entries) != 1 {
					t.Fatal("staging files leaked")
				}
			}
		})
	}
}

func TestWorkspaceCheckpointPreservesTrackedFilesUnderExcludedDirsWhileSkippingUntracked(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Checkpoint Test",
			"GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=Checkpoint Test",
			"GIT_COMMITTER_EMAIL=test@example.invalid",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return string(out)
	}
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git(repoDir, "init", "-q", "-b", "main")
	write("repo/web/vscode-web/node_modules/@microsoft/1ds/dist/ms.core.min.js", "v1\n")
	write("repo/web/vscode-web/node_modules/pkg/target.txt", "target\n")
	write("repo/assets/.cache/manifest.json", "{\"ok\":true}\n")
	write("repo/py/__pycache__/keep.pyc", "pyc-tracked")
	write("repo/.agentflow/committed-secret", "must-never-capture")
	if err := os.Symlink("../pkg/target.txt", filepath.Join(repoDir, "web/vscode-web/node_modules/@microsoft/link")); err != nil {
		t.Fatal(err)
	}
	git(repoDir, "add", "-f",
		"web/vscode-web/node_modules/@microsoft/1ds/dist/ms.core.min.js",
		"web/vscode-web/node_modules/pkg/target.txt",
		"web/vscode-web/node_modules/@microsoft/link",
		"assets/.cache/manifest.json",
		"py/__pycache__/keep.pyc",
		".agentflow/committed-secret",
	)
	git(repoDir, "commit", "-qm", "track vendored assets")

	// Unstaged edit and staged new file inside node_modules must keep their live contents.
	write("repo/web/vscode-web/node_modules/@microsoft/1ds/dist/ms.core.min.js", "v2-modified\n")
	write("repo/web/vscode-web/node_modules/staged.js", "staged\n")
	git(repoDir, "add", "-f", "web/vscode-web/node_modules/staged.js")

	// Untracked dependencies/caches inside the same repo and workspace must stay excluded.
	write("repo/chatbot/node_modules/huge/index.js", "npm-dep\n")
	write("repo/web/vscode-web/node_modules/untracked/dep.js", "untracked-dep\n")
	write("repo/assets/.cache/ephemeral.bin", "tmp")
	write("repo/py/__pycache__/ephemeral.pyc", "tmp")
	write("repo/.zvec-grep/index", "idx")
	write(".agentflow/auth/token", "host-secret")

	archive, err := CreateWorkspaceCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if err := RestoreWorkspaceCheckpoint(dest, archive); err != nil {
		t.Fatal(err)
	}

	wantPreserved := map[string]string{
		"repo/web/vscode-web/node_modules/@microsoft/1ds/dist/ms.core.min.js": "v2-modified\n",
		"repo/web/vscode-web/node_modules/pkg/target.txt":                     "target\n",
		"repo/web/vscode-web/node_modules/staged.js":                          "staged\n",
		"repo/assets/.cache/manifest.json":                                    "{\"ok\":true}\n",
		"repo/py/__pycache__/keep.pyc":                                        "pyc-tracked",
	}
	for rel, want := range wantPreserved {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Fatalf("restored %s = %q (%v), want %q", rel, got, err, want)
		}
	}
	if link, err := os.Readlink(filepath.Join(dest, "repo/web/vscode-web/node_modules/@microsoft/link")); err != nil || link != "../pkg/target.txt" {
		t.Fatalf("restored symlink = %q (%v)", link, err)
	}
	for _, excluded := range []string{
		"repo/chatbot/node_modules",
		"repo/web/vscode-web/node_modules/untracked",
		"repo/assets/.cache/ephemeral.bin",
		"repo/py/__pycache__/ephemeral.pyc",
		"repo/.zvec-grep",
		"repo/.agentflow",
		".agentflow",
	} {
		if _, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(excluded))); !os.IsNotExist(err) {
			t.Fatalf("untracked or secret path %s should not be restored", excluded)
		}
	}
	status := git(filepath.Join(dest, "repo"), "status", "--short")
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "D ") && !strings.Contains(line, ".agentflow/") {
			t.Fatalf("restored repo has missing tracked file %q\nfull status:\n%s", line, status)
		}
	}
}

func TestRestoreWorkspaceCheckpointReplacesEmptyAndRuntimeOnlyDirectoryAtomically(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "app.txt"), []byte("restored"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive, err := CreateWorkspaceCheckpoint(src)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("empty-dir", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "ws")
		if err := os.MkdirAll(dest, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := RestoreWorkspaceCheckpoint(dest, archive); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dest, "app.txt"))
		if err != nil || string(got) != "restored" {
			t.Fatalf("restored app.txt = %q (%v)", got, err)
		}
	})

	t.Run("agentflow-only-dir", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "ws")
		if err := os.MkdirAll(filepath.Join(dest, ".agentflow", "hooks"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, ".agentflow", "hooks", "pre.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := RestoreWorkspaceCheckpoint(dest, archive); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dest, "app.txt"))
		if err != nil || string(got) != "restored" {
			t.Fatalf("restored app.txt = %q (%v)", got, err)
		}
		if _, err := os.Lstat(filepath.Join(dest, ".agentflow")); !os.IsNotExist(err) {
			t.Fatalf("stale .agentflow should have been replaced, stat err=%v", err)
		}
	})

	t.Run("corrupt-archive-preserves-target", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "ws")
		marker := filepath.Join(dest, ".agentflow", "hooks", "pre.json")
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := RestoreWorkspaceCheckpoint(dest, []byte("not-gzip")); err == nil {
			t.Fatal("corrupt archive accepted")
		}
		got, err := os.ReadFile(marker)
		if err != nil || string(got) != "keep" {
			t.Fatalf("corrupt archive altered target: %q (%v)", got, err)
		}
	})

	t.Run("real-file-refuses-overwrite", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "ws")
		if err := os.MkdirAll(dest, 0o700); err != nil {
			t.Fatal(err)
		}
		existing := filepath.Join(dest, "surviving.txt")
		if err := os.WriteFile(existing, []byte("survive"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := RestoreWorkspaceCheckpoint(dest, archive); err == nil {
			t.Fatal("existing workspace overwritten")
		}
		got, err := os.ReadFile(existing)
		if err != nil || string(got) != "survive" {
			t.Fatalf("surviving file altered: %q (%v)", got, err)
		}
	})
}

func TestCheckpointGitTrackerTimeoutsDegradeToUntracked(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "index"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "node_modules", "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "node_modules", "pkg", "index.js"), []byte("tracked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("app"), 0o600); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	fakeGit := filepath.Join(bin, "git")
	if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\nsleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	prevTimeout := checkpointGitIndexTimeout
	checkpointGitIndexTimeout = 100 * time.Millisecond
	t.Cleanup(func() { checkpointGitIndexTimeout = prevTimeout })

	archive, err := CreateWorkspaceCheckpointContext(context.Background(), root)
	if err != nil {
		t.Fatalf("CreateWorkspaceCheckpointContext failed: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if err := RestoreWorkspaceCheckpoint(dest, archive); err != nil {
		t.Fatalf("RestoreWorkspaceCheckpoint failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "repo", "app.txt")); err != nil || string(got) != "app" {
		t.Fatalf("expected repo/app.txt restored, got %q (%v)", got, err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "repo", "node_modules", "pkg", "index.js")); !os.IsNotExist(err) {
		t.Fatalf("expected timed-out git index lookup to degrade and skip node_modules, err=%v", err)
	}
}

func TestCheckpointGitTrackerOverflowAndContextCancel(t *testing.T) {
	buf := &checkpointLimitedBuffer{limit: 4}
	if _, err := buf.Write([]byte("hello")); err == nil || !buf.overflow {
		t.Fatalf("expected overflow error, overflow=%v err=%v", buf.overflow, err)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CreateWorkspaceCheckpointContext(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestReplaceRestoredWorkspaceRollsBackOnConcurrentWrite(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "ws")
	stage := filepath.Join(parent, "stage")
	if err := os.MkdirAll(filepath.Join(root, ".agentflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "from-snapshot.txt"), []byte("snap"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := replaceRestoredWorkspaceWithRaceHook(stage, root, func() {
		_ = os.WriteFile(filepath.Join(root, "raced.txt"), []byte("concurrent"), 0o600)
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("expected refusing to overwrite error, got %v", err)
	}
	got, readErr := os.ReadFile(filepath.Join(root, "raced.txt"))
	if readErr != nil || string(got) != "concurrent" {
		t.Fatalf("concurrent file was not preserved after rollback: %q (%v)", got, readErr)
	}
	if _, snapErr := os.Lstat(filepath.Join(root, "from-snapshot.txt")); !os.IsNotExist(snapErr) {
		t.Fatalf("stage snapshot leaked into target after rollback: %v", snapErr)
	}
}
